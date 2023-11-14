package proxysql

import (
	"database/sql"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/rs/zerolog"
)

type ProxySQL struct {
	dsn    string
	conn   *sql.DB
	logger zerolog.Logger
}

func New(dsn string) (*ProxySQL, error) {
	// FIXME: this should probably be JSON
	logger := zerolog.New(
		zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339},
	).Level(zerolog.TraceLevel).With().Timestamp().Caller().Logger()

	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}

	err = conn.Ping()
	if err != nil {
		return nil, err
	}

	// FIXME: dont log full dsn, it has passwords in it
	logger.Info().Str("Host", dsn).Msg("Connected to ProxySQL admin")

	return &ProxySQL{dsn, conn, logger}, nil
}

func (p *ProxySQL) Conn() *sql.DB {
	return p.conn
}

func (p *ProxySQL) Ping() error {
	return p.conn.Ping()
}

func (p *ProxySQL) Close() {
	p.conn.Close()
}

func (p *ProxySQL) GetBackends() (map[string]int, error) {
	entries := make(map[string]int)

	rows, err := p.conn.Query("SELECT hostgroup_id, hostname, port FROM runtime_mysql_servers ORDER BY hostgroup_id")
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var hostgroup int
		var hostname string
		var port int

		err := rows.Scan(&hostgroup, &hostname, &port)
		if err != nil {
			return nil, err
		}

		entries[hostname] = hostgroup
		if rows.Err() != nil && rows.Err() != sql.ErrNoRows {
			return nil, rows.Err()
		}
	}

	return entries, nil
}
