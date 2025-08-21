package proxysql

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
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
			p.shutdownOnce.Do(func() {
				p.startDraining()
				p.gracefulShutdown(ctx)
			})

			return
		case <-ticker.C:
			err := p.SatelliteResync(ctx)
			if err != nil {
				slog.Error("Error running resync", slog.Any("error", err))
			}
		}
	}
}

func (p *ProxySQL) GetMissingCorePods(ctx context.Context) (int, error) {
	count := -1

	query := `SELECT COUNT(hostname)
			FROM stats_proxysql_servers_metrics
			WHERE last_check_ms > 30000
			AND hostname != 'proxysql-core'
			AND Uptime_s > 0`
	row := p.conn.QueryRowContext(ctx, query)

	err := row.Scan(&count)
	if err != nil {
		return count, fmt.Errorf("failed to scan count of missing core pods: %w", err)
	}

	return count, nil
}

// gracefulShutdown performs the graceful shutdown logic for satellite mode.
func (p *ProxySQL) gracefulShutdown(ctx context.Context) {
	slog.Info("Starting graceful shutdown process")

	// Step 1: Pause ProxySQL to stop accepting new database connections
	// (HTTP server continues serving shutdown responses)
	if p.conn != nil {
		_, err := p.conn.ExecContext(ctx, "PROXYSQL PAUSE;")
		if err != nil {
			slog.Error("Failed to pause ProxySQL", slog.Any("error", err))
		} else {
			slog.Info("ProxySQL paused successfully")
		}

		p.conn.Close()
		p.conn = nil
	}

	// Step 2: Wait for existing connections to drain (reasonable fixed time)
	drainTime := 30 * time.Second //nolint:mnd
	slog.Info("Waiting for connections to drain", slog.Duration("waitTime", drainTime))
	time.Sleep(drainTime)

	// Step 3: Stop HTTP server after connections have drained
	if p.httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second) //nolint:mnd

		slog.Info("Shutting down HTTP server")

		err := p.httpServer.Shutdown(shutdownCtx)
		if err != nil {
			slog.Error("Error shutting down HTTP server", slog.Any("err", err))
		}

		cancel()
	}

	slog.Info("Graceful shutdown completed")
	os.Exit(0)
}

// PreStopShutdown performs the complete graceful shutdown logic for HTTP handler.
func (p *ProxySQL) PreStopShutdown(ctx context.Context) {
	p.shutdownOnce.Do(func() {
		p.startDraining()
		p.gracefulShutdown(ctx)
	})
}

func (p *ProxySQL) SatelliteResync(ctx context.Context) error {
	missing, err := p.GetMissingCorePods(ctx)
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
			_, err := p.conn.ExecContext(ctx, command)
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
func (p *ProxySQL) DumpData(ctx context.Context) {
	tmpdir, fileErr := os.MkdirTemp("/tmp", "")
	if fileErr != nil {
		slog.Error("Error in DumpData()", slog.Any("error", fileErr))

		return
	}

	digestsFile, err := p.DumpQueryDigests(ctx, tmpdir)
	if err != nil {
		slog.Error("Error in DumpQueryDigests()", slog.Any("error", err))

		return
	} else if digestsFile != "" {
		slog.Info("Saved mysql query digests to file", slog.String("filename", digestsFile))
	}
}

// ProxySQL docs: https://proxysql.com/documentation/stats-statistics/#stats_mysql_query_digest
func (p *ProxySQL) DumpQueryDigests(ctx context.Context, tmpdir string) (string, error) {
	var rowCount int

	err := p.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM stats_mysql_query_digest").Scan(&rowCount)
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

	writeErr := writer.Write(header)
	if writeErr != nil {
		return "", fmt.Errorf("failed to write header to digest file: %w", writeErr)
	}

	rows, queryErr := p.conn.QueryContext(ctx, "SELECT * FROM stats_mysql_query_digest")
	if queryErr != nil && !errors.Is(rows.Err(), sql.ErrNoRows) {
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
		err = writer.Write(values)
		if err != nil {
			return "", fmt.Errorf("failed to write digest values: %w", err)
		}
	}

	return dumpFile, nil
}
