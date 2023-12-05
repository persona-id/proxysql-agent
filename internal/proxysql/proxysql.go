package proxysql

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/kuzmik/proxysql-agent/internal/configuration"

	// Import the mysql driver functionality.
	_ "github.com/go-sql-driver/mysql"
)

type ProxySQL struct {
	conn     *sql.DB
	settings *configuration.Config
}

func (p *ProxySQL) New(configs *configuration.Config) (*ProxySQL, error) {
	settings := configs
	address := settings.ProxySQL.Address
	username := settings.ProxySQL.Username
	password := settings.ProxySQL.Password

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/", username, password, address)

	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}

	err = conn.Ping()
	if err != nil {
		return nil, err
	}

	slog.Info("Connected to ProxySQL admin", slog.String("Host", address))

	return &ProxySQL{conn, settings}, nil
}

func (p *ProxySQL) Conn() *sql.DB {
	return p.conn
}

func (p *ProxySQL) Ping() error {
	return p.conn.Ping()
}

func (p *ProxySQL) GetBackends() (map[string]int, error) {
	entries := make(map[string]int)

	rows, err := p.conn.Query("SELECT hostgroup_id, hostname, port FROM runtime_mysql_servers ORDER BY hostgroup_id")
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var hostgroup, port int

		var hostname string

		err := rows.Scan(&hostgroup, &hostname, &port)
		if err != nil {
			return nil, err
		}

		entries[hostname] = hostgroup

		if rows.Err() != nil && errors.Is(err, sql.ErrNoRows) {
			return nil, rows.Err()
		}
	}

	return entries, nil
}

// k8s probes

type ProbeResult struct {
	Status   string `json:"status,omitempty"`
	Message  string `json:"message,omitempty"`
	Clients  int    `json:"clients,omitempty"`
	Draining bool   `json:"draining,omitempty"`
	Probe    string `json:"probe,omitempty"`
	Backends struct {
		Total  int `json:"total,omitempty"`
		Online int `json:"online,omitempty"`
	} `json:"backends,omitempty"`
}

func (p *ProxySQL) RunProbes() (ProbeResult, error) {
	total, online, err := p.probeBackends()
	if err != nil {
		return ProbeResult{}, err
	}

	clients, err := p.ProbeClients()
	if err != nil {
		return ProbeResult{}, err
	}

	results := ProbeResult{
		Clients:  clients,
		Draining: probeDraining(),
	}

	results.Backends.Total = total
	results.Backends.Online = online

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

func (p *ProxySQL) probeBackends() (int /* backends total */, int /* backends online */, error) {
	var total, online int

	err := p.conn.QueryRow("SELECT COUNT(*) FROM runtime_mysql_servers").Scan(&total)
	if err != nil {
		return -1, -1, err
	}

	err = p.conn.QueryRow("SELECT COUNT(*) FROM runtime_mysql_servers WHERE status = 'ONLINE'").Scan(&online)
	if err != nil {
		return -1, -1, err
	}

	return online, total, nil
}

func (p *ProxySQL) ProbeClients() (int /* clients connected */, error) {
	var online sql.NullInt32

	// this one doesnt appear to do what we want
	// query := "SELECT Client_Connections_connected FROM mysql_connections ORDER BY timestamp DESC LIMIT 1"

	query := "select sum(ConnUsed) from stats_mysql_connection_pool"

	err := p.conn.QueryRow(query).Scan(&online)
	if err != nil {
		return -1, err
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
