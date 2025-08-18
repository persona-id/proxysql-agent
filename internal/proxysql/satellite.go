package proxysql

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

//
// Satellite mode specific functions
//

func (p *ProxySQL) Satellite(ctx context.Context) {
	interval := p.settings.Satellite.Interval

	slog.Info("Satellite mode initialized, looping", slog.Int("interval", interval))

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Context cancelled, stopping satellite")
			p.startDraining()
			p.gracefulShutdown()

			return
		case <-ticker.C:
			err := p.SatelliteResync()
			if err != nil {
				slog.Error("Error running resync", slog.Any("error", err))
			}
		}
	}
}

func (p *ProxySQL) GetMissingCorePods() (int, error) {
	count := -1

	query := `SELECT COUNT(hostname)
			FROM stats_proxysql_servers_metrics
			WHERE last_check_ms > 30000
			AND hostname != 'proxysql-core'
			AND Uptime_s > 0`
	row := p.conn.QueryRow(query)

	err := row.Scan(&count)
	if err != nil {
		return count, fmt.Errorf("failed to scan count of missing core pods: %w", err)
	}

	return count, nil
}

// gracefulShutdown performs the graceful shutdown logic for satellite mode.
func (p *ProxySQL) gracefulShutdown() {
	// FIXME: make these configurable
	shutdownDelay := 120
	hasCSP := false

	slog.Info("Starting graceful shutdown process", slog.Int("shutdownDelay", shutdownDelay))

	// the settings in the proxysql variables are all in ms, so convert shutdownDelay over to MS
	timeouts := shutdownDelay * int(time.Millisecond)

	// disable new connections
	commands := []string{
		fmt.Sprintf("UPDATE global_variables SET variable_value = %d WHERE variable_name in ('mysql-connection_max_age_ms', 'mysql-max_transaction_idle_time', 'mysql-max_transaction_time')", timeouts),
		"UPDATE global_variables SET variable_value = 1 WHERE variable_name = 'mysql-wait_timeout'",
		"LOAD MYSQL VARIABLES TO RUNTIME",
		"PROXYSQL PAUSE;",
	}

	for _, command := range commands {
		_, err := p.conn.Exec(command)
		if err != nil {
			slog.Error("Command failed", slog.String("commands", command), slog.Any("error", err))
		}
	}

	slog.Info("Pre-stop commands ran", slog.String("commands", strings.Join(commands, "; ")))

	for {
		if p.safeToTerminate() {
			slog.Info("No connected clients remaining, proceeding with shutdown")
			break
		}

		time.Sleep(10 * time.Second)
	}

	// issue the PROXYSQL KILL command
	_, err := p.conn.Exec("PROXYSQL KILL")
	if err != nil {
		slog.Error("KILL command failed", slog.String("commands", "PROXYSQL KILL"), slog.Any("error", err))
	}

	// kill cloud-sql-proxy (CSP) if it exists
	if hasCSP {
		err = p.killCSP()
		if err != nil {
			slog.Error("Failed to kill CSP", slog.Any("error", err))
		}
	}

	time.Sleep(10 * time.Second)

	os.Exit(0)
}

// PreStopShutdown performs the complete graceful shutdown logic for HTTP handler.
func (p *ProxySQL) PreStopShutdown() {
	p.startDraining()
	p.gracefulShutdown()
}

// safeToTerminate checks if it is safe to terminate the ProxySQL instance.
// It returns true if there are no connected clients, otherwise it returns false.
func (p *ProxySQL) safeToTerminate() bool {
	// check for connected clients, and when it hits 0 return true
	clients, err := p.ProbeClients()
	if err != nil {
		slog.Error("Error in probeClients()", slog.Any("err", err))
	}

	if clients > 0 {
		slog.Info("Clients connected", slog.Int("clients", clients))
	}

	// maybe we should also return true if a specified amount of time has passed, in order to not let one rogue transaction hold us up.
	return clients == 0
}

// killCSP kills cloud-sql-proxy (CSP) if it is running; this should be optional and configurable,
// or moved into a plugin down the road.
func (p *ProxySQL) killCSP() error {
	// Make an HTTP request to localhost:9091/quitquitquit
	resp, err := http.Get("http://localhost:9091/quitquitquit")
	if err != nil {
		return fmt.Errorf("failed to make HTTP request to CSP: %w", err)
	}
	defer resp.Body.Close()

	// Check the response status
	if resp.StatusCode == http.StatusOK {
		slog.Info("Killed CSP")
	} else {
		slog.Warn("HTTP request to CSP failed", slog.String("status", resp.Status))
	}

	return nil
}

func (p *ProxySQL) SatelliteResync() error {
	missing, err := p.GetMissingCorePods()
	if err != nil {
		return err
	}

	if missing > 0 {
		slog.Info("Resyncing pod to cluster", slog.Int("missing_cores", missing))

		commands := []string{
			"DELETE FROM proxysql_servers",
			"LOAD PROXYSQL SERVERS FROM CONFIG",
			"LOAD PROXYSQL SERVERS TO RUNTIME;",
		}

		for _, command := range commands {
			_, err := p.conn.Exec(command)
			if err != nil {
				return fmt.Errorf("failed to execute command '%s': %w", command, err)
			}
		}
	}

	return nil
}

// data we eventually want to load into snowflake
//  1. stats_mysql_query_digests (maybe use _reset to reset the state)
//  2. mysql_query_rules
//  3. stats_mysql_query_rules
//
// FIXME: all these functions dump to /tmp/XXXX/Y.csv; we want the directory to be configurable at least.
func (p *ProxySQL) DumpData() {
	tmpdir, fileErr := os.MkdirTemp("/tmp", "")
	if fileErr != nil {
		slog.Error("Error in DumpData()", slog.Any("error", fileErr))

		return
	}

	digestsFile, err := p.DumpQueryDigests(tmpdir)
	if err != nil {
		slog.Error("Error in DumpQueryDigests()", slog.Any("error", err))

		return
	} else if digestsFile != "" {
		slog.Info("Saved mysql query digests to file", slog.String("filename", digestsFile))
	}
}

// ProxySQL docs: https://proxysql.com/documentation/stats-statistics/#stats_mysql_query_digest
func (p *ProxySQL) DumpQueryDigests(tmpdir string) (string, error) {
	var rowCount int

	err := p.conn.QueryRow("SELECT COUNT(*) FROM stats_mysql_query_digest").Scan(&rowCount)
	if err != nil {
		return "", fmt.Errorf("failed to get query digest count: %w", err)
	}

	// Don't proceed with this function if there are no entries in the table
	if rowCount <= 0 {
		slog.Debug("No query digests in the log, not proceeding with DumpQueryDigests()")

		return "", nil
	}

	hostname, hostnameErr := os.Hostname()
	if hostnameErr != nil {
		// os.Hostname didn't work for some reason, so try to get the hostname from the ENV
		hostname = os.Getenv("HOSTNAME")
		if hostname == "" {
			// that didn't work either, so something is really wrong
			return "", fmt.Errorf("failed to get hostname: %w", hostnameErr)
		}
	}

	dumpFile := fmt.Sprintf("%s/%s-digests.csv", tmpdir, hostname)

	file, fileErr := os.Create(dumpFile)
	if fileErr != nil {
		return "", fmt.Errorf("failed to create digest file: %w", fileErr)
	}

	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	header := []string{
		"pod_name",
		"hostgroup",
		"schemaname",
		"username",
		"digest",
		"digest_text",
		"count_star",
		"first_seen",
		"last_seen",
		"sum_time_us",
		"min_time_us",
		"max_time",
		"sum_rows_affected",
		"sum_rows_sent",
	}

	if writeErr := writer.Write(header); writeErr != nil {
		return "", fmt.Errorf("failed to write header to digest file: %w", writeErr)
	}

	rows, queryErr := p.conn.Query("SELECT * FROM stats_mysql_query_digest")
	if queryErr != nil {
		return "", fmt.Errorf("failed to query digest data: %w", queryErr)
	}

	defer rows.Close()

	for rows.Next() {
		var hostgroup int

		var schemaname, username, clientAddress, digest, digestText string

		var countStar, firstSeen, lastSeen, sumTime, minTime, maxTime, sumRowsAffected, sumRowsSent int

		err := rows.Scan(&hostgroup, &schemaname, &username, &clientAddress, &digest, &digestText, &countStar,
			&firstSeen, &lastSeen, &sumTime, &minTime, &maxTime, &sumRowsAffected, &sumRowsSent)
		if err != nil {
			return "", fmt.Errorf("failed to scan digest row: %w", err)
		}

		// Create a slice with the values
		values := []string{
			hostname,
			strconv.Itoa(hostgroup),
			schemaname,
			username,
			digest,
			`"` + digestText + `"`, // Quote the digest_text field to handle commas
			strconv.Itoa(countStar),
			time.Unix(int64(firstSeen), 0).String(),
			time.Unix(int64(lastSeen), 0).String(),
			strconv.Itoa(sumTime),
			strconv.Itoa(minTime),
			strconv.Itoa(maxTime),
			strconv.Itoa(sumRowsAffected),
			strconv.Itoa(sumRowsSent),
		}

		// Write the values to the CSV file
		if err := writer.Write(values); err != nil {
			return "", fmt.Errorf("failed to write digest values: %w", err)
		}
	}

	return dumpFile, nil
}
