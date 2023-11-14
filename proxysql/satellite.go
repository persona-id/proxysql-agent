package proxysql

import (
	"time"
)

func (p *ProxySQL) Satellite() {
	p.logger.Info().Msg("Satellite mode initialized; looping every 10s")

	for {
		p.SatelliteResync()
		time.Sleep(10 * time.Second)
	}
}

// GetMissingCorePods returns the count of backend servers that are considered missing
// based on specified criteria.
// It queries the "stats_proxysql_servers_metrics" table to count servers with:
//  1. last_check_ms greater than 30000.
//  2. hostname not equal to 'proxysql-core'.
//  3. Uptime_s greater than 0.
//
// The count of such servers is returned along with any encountered error.
//
// Parameters:
// - p: A pointer to the ProxySQL instance with an active database connection.
//
// Returns:
// - int: The count of missing core pods.
// - error: An error, if any occurred during the database query.
func (p *ProxySQL) GetMissingCorePods() (int, error) {
	var count int = -1

	row := p.conn.QueryRow("SELECT COUNT(hostname) FROM stats_proxysql_servers_metrics WHERE last_check_ms > 30000 AND hostname != 'proxysql-core' AND Uptime_s > 0")

	err := row.Scan(&count)
	if err != nil {
		return count, err
	}

	return count, nil
}

// SatelliteResync performs a resynchronization of the ProxySQL server configuration when there are missing core pods.
// It checks for missing core pods by calling the GetMissingCorePods function and resynchronizes the configuration
// by executing predefined SQL commands.
//
// If there are missing core pods, it logs the number of missing cores and resyncs the configuration using the following commands:
//  1. "DELETE FROM proxysql_servers"
//  2. "LOAD PROXYSQL SERVERS FROM CONFIG"
//  3. "LOAD PROXYSQL SERVERS TO RUNTIME"
//
// If there are no missing core pods, it logs that no resync is necessary.
//
// Parameters:
// - p: A pointer to the ProxySQL instance with an active database connection.
//
// Returns:
// - error: An error, if any occurred during the resynchronization process.
func (p *ProxySQL) SatelliteResync() error {
	var missing = -1
	var err error

	missing, err = p.GetMissingCorePods()
	if err != nil {
		return err
	}

	if missing > 0 {
		p.logger.Debug().Int("missing_cores", missing).Msg("Resyncing pod to cluster")

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

	} else {
		p.logger.Debug().Msg("No missing core pods, resync not necessary")
	}

	return nil
}
