package proxysql

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	_ "github.com/go-sql-driver/mysql"
	"github.com/spf13/viper"
)

type ProxySQL struct {
	dsn    string
	conn   *sql.DB
	logger *slog.Logger
}

func New() (*ProxySQL, error) {
	opts := &slog.HandlerOptions{
		AddSource: true,
		Level:     slog.LevelDebug,
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, opts))

	slog.SetDefault(logger)

	address := viper.GetViper().GetString("proxysql.address")
	username := viper.GetViper().GetString("proxysql.username")
	password := viper.GetViper().GetString("proxysql.password")

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
