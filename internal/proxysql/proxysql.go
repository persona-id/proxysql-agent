package proxysql

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/persona-id/proxysql-agent/internal/configuration"

	// Import the mysql driver functionality.
	_ "github.com/go-sql-driver/mysql"
	"k8s.io/client-go/kubernetes"
)

type ProxySQL struct {
	clientset kubernetes.Interface
	conn      *sql.DB
	settings  *configuration.Config
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

	err = conn.Ping()
	if err != nil {
		return nil, fmt.Errorf("failed to ping ProxySQL: %w", err)
	}

	slog.Info("Connected to ProxySQL admin", slog.String("Host", address))

	return &ProxySQL{nil, conn, settings}, nil
}

func (p *ProxySQL) Conn() *sql.DB {
	return p.conn
}

func (p *ProxySQL) Ping() error {
	err := p.conn.Ping()
	if err != nil {
		return fmt.Errorf("failed to ping ProxySQL: %w", err)
	}

	return nil
}

func (p *ProxySQL) GetBackends() (map[string]int, error) {
	entries := make(map[string]int)

	rows, err := p.conn.Query("SELECT hostgroup_id, hostname, port FROM runtime_mysql_servers ORDER BY hostgroup_id")
	if err != nil {
		return nil, fmt.Errorf("failed to query runtime_mysql_servers: %w", err)
	}

	defer rows.Close()

	for rows.Next() {
		var hostgroup, port int

		var hostname string

		err := rows.Scan(&hostgroup, &hostname, &port)
		if err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		entries[hostname] = hostgroup

		if rows.Err() != nil && errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("error iterating rows: %w", rows.Err())
		}
	}

	return entries, nil
}

// k8s probes

type ProbeResult struct {
	// Pointer types (8 bytes) first
	Backends *struct {
		Total  int `json:"total,omitempty"`
		Online int `json:"online,omitempty"`
	} `json:"backends,omitempty"`

	// String types (16 bytes)
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
	Probe   string `json:"probe,omitempty"`

	// Integer types (8 bytes)
	Clients int `json:"clients,omitempty"`

	// Boolean types (1 byte)
	Draining bool `json:"draining,omitempty"`
}

func (p *ProxySQL) RunProbes() (ProbeResult, error) {
	total, online, err := p.probeBackends()
	if err != nil {
		return ProbeResult{}, fmt.Errorf("failed to probe backends: %w", err)
	}

	clients, err := p.ProbeClients()
	if err != nil {
		return ProbeResult{}, fmt.Errorf("failed to probe clients: %w", err)
	}

	results := ProbeResult{
		Clients:  clients,
		Draining: probeDraining(),
		Probe:    "",
		Status:   "",
		Message:  "",
		Backends: &struct {
			Total  int `json:"total,omitempty"`
			Online int `json:"online,omitempty"`
		}{
			Total:  total,
			Online: online,
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

func (p *ProxySQL) ProbeClients() (int /* clients connected */, error) {
	var online sql.NullInt32

	// this one doesnt appear to do what we want
	// query := "SELECT Client_Connections_connected FROM mysql_connections ORDER BY timestamp DESC LIMIT 1"

	query := "select sum(ConnUsed) from stats_mysql_connection_pool"

	err := p.conn.QueryRow(query).Scan(&online)
	if err != nil {
		return -1, fmt.Errorf("failed to query connection pool stats: %w", err)
	}

	if online.Valid {
		return int(online.Int32), nil
	}

	return -1, nil
}

// if the file /var/lib/proxysql/draining exists, we're in maint mode or draining traffic
// for a shutdown, and should return unhealthy.
func probeDraining() bool {
	// FIXME: make this configurable?
	filename := "/var/lib/proxysql/draining"

	_, err := os.Stat(filename)

	switch {
	case os.IsNotExist(err):
		return false
	case err != nil:
		return false
	default:
		return true
	}
}

// startDraining creates the drain file to signal that the pod is draining.
func (p *ProxySQL) startDraining() {
	drainFile := "/var/lib/proxysql/draining"
	slog.Info("Creating drain file to signal draining state", slog.String("path", drainFile))

	_, err := os.Create(drainFile)
	if err != nil {
		slog.Error("Error creating drainFile", slog.String("path", drainFile), slog.Any("err", err))
	}
}

func (p *ProxySQL) probeBackends() (int /* backends total */, int /* backends online */, error) {
	var total, online int

	err := p.conn.QueryRow("SELECT COUNT(*) FROM runtime_mysql_servers").Scan(&total)
	if err != nil {
		return -1, -1, fmt.Errorf("failed to query total backends: %w", err)
	}

	err = p.conn.QueryRow("SELECT COUNT(*) FROM runtime_mysql_servers WHERE status = 'ONLINE'").Scan(&online)
	if err != nil {
		return -1, -1, fmt.Errorf("failed to query online backends: %w", err)
	}

	return online, total, nil
}
