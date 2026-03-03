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
	"time"

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
	clientset         kubernetes.Interface
	conn              *sql.DB
	settings          *configuration.Config
	shutdownOnce      sync.Once
	shutdownPhase     ShutdownPhase
	shutdownMu        sync.RWMutex
	httpServer        *http.Server
	retryDelay        time.Duration  // delay between podAdded retries; defaults to podAddedRetryDelay
	drainTickInterval time.Duration  // interval for drain polling; 0 means use 2s default
	podWg             sync.WaitGroup // tracks in-flight podAdded goroutines for clean shutdown
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
		retryDelay:    podAddedRetryDelay,
		podWg:         sync.WaitGroup{},
	}, nil
}

func (p *ProxySQL) Ping(ctx context.Context) error {
	err := p.conn.PingContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to ping ProxySQL: %w", err)
	}

	return nil
}

// k8s probes

type ProbeResult struct {
	Backends *struct {
		Online  int `json:"online,omitempty"`
		Shunned int `json:"shunned,omitempty"`
		Total   int `json:"total,omitempty"`
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
			Online  int `json:"online,omitempty"`
			Shunned int `json:"shunned,omitempty"`
			Total   int `json:"total,omitempty"`
		}{
			Online:  online,
			Shunned: shunned,
			Total:   total,
		},
	}

	return processResults(results), nil
}

// Process the ProbeResult and set values for use in the json message the API returns.
func processResults(results ProbeResult) ProbeResult {
	switch {
	case results.Draining:
		results.Status = "draining"
		results.Message = "draining traffic"

	case results.Backends.Online == 0:
		results.Status = "ok"
		results.Message = "all backends offline"

	case results.Backends.Online < results.Backends.Total:
		results.Status = "ok"
		results.Message = "some backends offline"

	default:
		results.Status = "ok"
		results.Message = "all backends online"
	}

	return results
}

func (p *ProxySQL) ProbeClients(ctx context.Context) (int /* clients connected */, error) {
	// If connection is nil, return 0 clients
	if p.conn == nil {
		return 0, nil
	}

	var online sql.NullInt32

	query := "SELECT Client_Connections_connected FROM mysql_connections ORDER BY timestamp DESC LIMIT 1"

	err := p.conn.QueryRowContext(ctx, query).Scan(&online)
	if err != nil {
		return -1, fmt.Errorf("failed to query connection pool stats: %w", err)
	}

	if online.Valid {
		return int(online.Int32), nil
	}

	return 0, nil
}

// IsShuttingDown returns true if the ProxySQL instance is in shutdown process.
func (p *ProxySQL) IsShuttingDown() bool {
	p.shutdownMu.RLock()
	defer p.shutdownMu.RUnlock()

	return p.shutdownPhase != PhaseRunning
}

// SetHTTPServer sets the HTTP server reference for graceful shutdown.
func (p *ProxySQL) SetHTTPServer(server *http.Server) {
	p.shutdownMu.Lock()
	defer p.shutdownMu.Unlock()

	p.httpServer = server
}

// shutdownHTTPServer gracefully stops the HTTP server if one is set.
// It must be called from outside any active HTTP handler to avoid deadlocking:
// httpServer.Shutdown waits for in-flight requests to complete, so calling it
// from within an HTTP handler would wait forever for itself.
func (p *ProxySQL) shutdownHTTPServer() {
	p.shutdownMu.RLock()
	httpServer := p.httpServer
	p.shutdownMu.RUnlock()

	if httpServer == nil {
		return
	}

	slog.Info("shutting down HTTP server")

	httpCtx, httpCancel := context.WithTimeout(context.Background(), 10*time.Second) //nolint:mnd
	defer httpCancel()

	if err := httpServer.Shutdown(httpCtx); err != nil {
		slog.Error("failed to shutdown HTTP server", slog.Any("error", err))
	} else {
		slog.Info("HTTP server shutdown completed")
	}
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
func (p *ProxySQL) startDraining() error {
	p.setShutdownPhase(PhaseDraining)

	drainFile := p.settings.Shutdown.DrainingFile

	f, err := os.Create(drainFile)
	if err != nil {
		return fmt.Errorf("failed to create drain file %s: %w", drainFile, err)
	}

	f.Close()

	slog.Info("created drain file", slog.String("path", drainFile))

	return nil
}

func (p *ProxySQL) probeBackends(ctx context.Context) (int /* backends total */, int /* backends online */, int /* backends shunned */, error) {
	// If connection is closed or we're shutting down, return default values
	if p.conn == nil || p.IsShuttingDown() {
		return 0, 0, 0, nil
	}

	var total int
	var online, shunned sql.NullInt64

	// COALESCE guards against NULL when the table is empty, but we also scan into
	// NullInt64 as a defensive measure in case the database returns NULL anyway.
	query := `SELECT COUNT(*) AS total,
		COALESCE(SUM(CASE WHEN status = 'ONLINE' THEN 1 ELSE 0 END), 0) AS online,
		COALESCE(SUM(CASE WHEN status = 'SHUNNED' THEN 1 ELSE 0 END), 0) AS shunned
		FROM runtime_mysql_servers`

	err := p.conn.QueryRowContext(ctx, query).Scan(&total, &online, &shunned)
	if err != nil {
		return -1, -1, -1, fmt.Errorf("failed to query backend status: %w", err)
	}

	return total, int(online.Int64), int(shunned.Int64), nil
}
