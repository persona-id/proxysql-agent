package proxysql

import (
	"log/slog"
	"time"

	"github.com/spf13/viper"
)

func (p *ProxySQL) Satellite() {
	interval := viper.GetViper().GetInt("satellite.interval")

	p.logger.Info("Satellite mode initialized, looping", slog.Int("interval (s)", interval))

	for {
		err := p.SatelliteResync()
		if err != nil {
			p.logger.Error("Error running resync", slog.Any("error", err))
		}

		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func (p *ProxySQL) GetMissingCorePods() (int, error) {
	var count int = -1

	row := p.conn.QueryRow("SELECT COUNT(hostname) FROM stats_proxysql_servers_metrics WHERE last_check_ms > 30000 AND hostname != 'proxysql-core' AND Uptime_s > 0")

	err := row.Scan(&count)
	if err != nil {
		return count, err
	}

	return count, nil
}

func (p *ProxySQL) SatelliteResync() error {
	var missing = -1
	var err error

	missing, err = p.GetMissingCorePods()
	if err != nil {
		return err
	}

	if missing > 0 {
		p.logger.Info("Resyncing pod to cluster", slog.Int("missing_cores", missing))

		commands := []string{
			"DELETE FROM proxysql_servers",
			"LOAD PROXYSQL SERVERS FROM CONFIG",
			"LOAD PROXYSQL SERVERS TO RUNTIME;",
		}

		for _, command := range commands {
			_, err := p.conn.Exec(command)
			if err != nil {
				return err
			}
		}
	}

	return nil
}
