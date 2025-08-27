package proxysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/persona-id/proxysql-agent/internal/configuration"

	// Import the mysql driver functionality.
	_ "github.com/go-sql-driver/mysql"
	"k8s.io/client-go/kubernetes"
)

// ShutdownPhase represents the current shutdown state.
type ShutdownPhase int

const (
	PhaseRunning ShutdownPhase = iota
	PhaseDraining
	PhaseStopping
	PhaseStopped
)

func (p ShutdownPhase) String() string {
	switch p {
	case PhaseRunning:
		return "running"

	case PhaseDraining:
		return "draining"

	case PhaseStopping:
		return "stopping"

	case PhaseStopped:
		return "stopped"

	default:
		return "unknown"
	}
}

type ProxySQL struct {
	clientset     kubernetes.Interface
	conn          *sql.DB
	settings      *configuration.Config
	shutdownOnce  sync.Once
	shutdownPhase ShutdownPhase
	shutdownMu    sync.RWMutex
	httpServer    *http.Server
}

func (p *ProxySQL) New(configs *configuration.Config) (*ProxySQL, error) {
	settings := configs
	address := settings.ProxySQL.Address
	username := settings.ProxySQL.Username
	password := settings.ProxySQL.Password

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/", username, password, address)

	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open MySQL connection: %w", err)
	}

	err = conn.PingContext(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to ping ProxySQL: %w", err)
	}

	slog.Info("Connected to ProxySQL admin", slog.String("Host", address))

	return &ProxySQL{
		clientset:     nil,
		conn:          conn,
		settings:      settings,
		shutdownOnce:  sync.Once{},
		shutdownPhase: PhaseRunning,
		shutdownMu:    sync.RWMutex{},
		httpServer:    nil,
	}, nil
}

func (p *ProxySQL) Ping(ctx context.Context) error {
	// If connection is closed or we're shutting down, return nil
	if p.conn == nil || p.IsShuttingDown() {
		return nil
	}

	err := p.conn.PingContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to ping ProxySQL: %w", err)
	}

	return nil
}

// k8s probes

type ProbeResult struct {
	Backends *struct {
		Total   int `json:"total,omitempty"`
		Online  int `json:"online,omitempty"`
		Shunned int `json:"shunned,omitempty"`
	} `json:"backends,omitempty"`
	Status   string `json:"status,omitempty"`
	Message  string `json:"message,omitempty"`
	Probe    string `json:"probe,omitempty"`
	Clients  int    `json:"clients,omitempty"`
	Draining bool   `json:"draining,omitempty"`
}

func (p *ProxySQL) RunProbes(ctx context.Context) (ProbeResult, error) {
	total, online, shunned, err := p.probeBackends(ctx)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("failed to probe backends: %w", err)
	}

	clients, err := p.ProbeClients(ctx)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("failed to probe clients: %w", err)
	}

	results := ProbeResult{
		Clients:  clients,
		Draining: p.probeDraining(),
		Probe:    "",
		Status:   "",
		Message:  "",
		Backends: &struct {
			Total   int `json:"total,omitempty"`
			Online  int `json:"online,omitempty"`
			Shunned int `json:"shunned,omitempty"`
		}{
			Total:   total,
			Online:  online,
			Shunned: shunned,
		},
	}

	return processResults(results), nil
}

// Process the ProbeResult and set values for use in the json message the API returns.
func processResults(results ProbeResult) ProbeResult {
	switch {
	case results.Backends.Online > results.Backends.Total:
		results.Status = "ok"
		results.Message = "some backends offline"

	case results.Backends.Online == 0:
		results.Status = "unhealthy"
		results.Message = "all backends offline"

	case results.Draining:
		results.Status = "draining"
		results.Message = "draining traffic"

	default:
		results.Status = "ok"
		results.Message = "all backends online"
	}

	return results
}

func (p *ProxySQL) ProbeClients(ctx context.Context) (int /* clients connected */, error) {
	// If connection is closed or we're shutting down, return 0 clients
	if p.conn == nil || p.IsShuttingDown() {
		return 0, nil
	}

	var online sql.NullInt32

	query := "SELECT Client_Connections_connected FROM mysql_connections ORDER BY timestamp DESC LIMIT 1"

	err := p.conn.QueryRowContext(ctx, query).Scan(&online)
	if err != nil {
		return -1, fmt.Errorf("failed to query connection pool stats: command: %s, error: %w", query, err)
	}

	if online.Valid {
		return int(online.Int32), nil
	}

	return -1, nil
}

// IsShuttingDown returns true if the ProxySQL instance is in shutdown process.
func (p *ProxySQL) IsShuttingDown() bool {
	p.shutdownMu.RLock()
	defer p.shutdownMu.RUnlock()

	return p.shutdownPhase != PhaseRunning
}

// SetHTTPServer sets the HTTP server reference for graceful shutdown.
func (p *ProxySQL) SetHTTPServer(server *http.Server) {
	p.httpServer = server
}

// setShutdownPhase updates the shutdown phase with logging.
func (p *ProxySQL) setShutdownPhase(phase ShutdownPhase) {
	p.shutdownMu.Lock()
	defer p.shutdownMu.Unlock()

	oldPhase := p.shutdownPhase
	p.shutdownPhase = phase

	if oldPhase != phase {
		slog.Info("shutdown phase changed",
			slog.String("from", oldPhase.String()),
			slog.String("to", phase.String()),
		)
	}
}

// probeDraining checks if the draining file exists, indicating that the pod is in maintenance mode
// or draining traffic for a shutdown, and should return unhealthy.
func (p *ProxySQL) probeDraining() bool {
	filename := p.settings.Shutdown.DrainingFile

	_, err := os.Stat(filename)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return false

	case err != nil:
		return false

	default:
		return true
	}
}

// startDraining creates the drain file to signal that the pod is draining.
func (p *ProxySQL) startDraining(ctx context.Context) error {
	p.setShutdownPhase(PhaseDraining)

	drainFile := p.settings.Shutdown.DrainingFile

	_, err := os.Create(drainFile)
	if err != nil {
		return fmt.Errorf("failed to create drain file %s: %w", drainFile, err)
	}

	slog.Info("created drain file", slog.String("path", drainFile))

	cmd := "PROXYSQL PAUSE"

	_, execErr := p.conn.ExecContext(ctx, cmd)
	if execErr != nil {
		// Continue with shutdown even if pause fails
		slog.Error("failed to pause ProxySQL",
			slog.String("command", cmd),
			slog.Any("error", execErr),
		)
	} else {
		slog.Info("ProxySQL paused")
	}

	return nil
}

func (p *ProxySQL) probeBackends(ctx context.Context) (int /* backends total */, int /* backends online */, int /* backends shunned */, error) {
	// If connection is closed or we're shutting down, return default values
	if p.conn == nil || p.IsShuttingDown() {
		return 0, 0, 0, nil
	}

	var total, online, shunned int

	// all backends
	cmd1 := "SELECT COUNT(*) FROM runtime_mysql_servers"

	err := p.conn.QueryRowContext(ctx, cmd1).Scan(&total)
	if err != nil {
		return -1, -1, -1, fmt.Errorf("failed to query total backends: command: %s, error: %w", cmd1, err)
	}

	// online backends
	cmd2 := "SELECT COUNT(*) FROM runtime_mysql_servers WHERE status = 'ONLINE'"

	err = p.conn.QueryRowContext(ctx, cmd2).Scan(&online)
	if err != nil {
		return -1, -1, -1, fmt.Errorf("failed to query online backends: command: %s, error: %w", cmd2, err)
	}

	// shunned backends
	cmd3 := "SELECT COUNT(*) FROM runtime_mysql_servers WHERE status = 'SHUNNED'"

	err = p.conn.QueryRowContext(ctx, cmd3).Scan(&shunned)
	if err != nil {
		return -1, -1, -1, fmt.Errorf("failed to query shunned backends: command: %s, error: %w", cmd3, err)
	}

	return total, online, shunned, nil
}
