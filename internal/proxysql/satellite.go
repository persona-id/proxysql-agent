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

// satelliteLoop is the main loop for satellite mode.
func (p *ProxySQL) Satellite(ctx context.Context) error {
	interval := p.settings.Satellite.Interval

	slog.Info("satellite mode initialized, looping", slog.Int("interval", interval))

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("context cancelled, stopping satellite")

			var shutdownErr error

			p.shutdownOnce.Do(func() {
				err := p.startDraining(ctx)
				if err != nil {
					shutdownErr = fmt.Errorf("failed to start draining: %w", err)

					return
				}

				err = p.gracefulShutdown(ctx)
				if err != nil {
					shutdownErr = fmt.Errorf("graceful shutdown failed: %w", err)
				}
			})

			return shutdownErr

		case <-ticker.C:
			err := p.SatelliteResync(ctx)
			if err != nil {
				return fmt.Errorf("satellite resync failed: %w", err)
			}
		}
	}
}

// GetMissingCorePods returns the number of core pods that are missing from the cluster.
//
// FIXME(kuzmik): change this to use an informer that watches for new core pods, sleeps for 10s, and then triggers a resync.
func (p *ProxySQL) GetMissingCorePods(ctx context.Context) (int, error) {
	// If connection is closed or we're shutting down, return nil
	if p.conn == nil || p.IsShuttingDown() {
		return -1, nil
	}

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

// PreStopShutdown performs the complete graceful shutdown logic for HTTP handler.
func (p *ProxySQL) PreStopShutdown(ctx context.Context) error {
	var shutdownErr error

	p.shutdownOnce.Do(func() {
		err := p.startDraining(ctx)
		if err != nil {
			shutdownErr = fmt.Errorf("failed to start draining: %w", err)

			return
		}

		// Use a new context for graceful shutdown that's independent of the HTTP request
		err = p.gracefulShutdown(ctx)
		if err != nil {
			shutdownErr = fmt.Errorf("graceful shutdown failed: %w", err)
		}
	})

	return shutdownErr
}

// It's possible we can just use the informer here as well, but maybe it's better to just have cores do that part.
func (p *ProxySQL) SatelliteResync(ctx context.Context) error {
	if p.IsShuttingDown() {
		slog.Debug("skipping satellite resync: shutting down")

		return nil
	}

	missing, err := p.GetMissingCorePods(ctx)
	if err != nil {
		return err
	}

	if missing > 0 {
		slog.Info("resyncing pod to cluster", slog.Int("missing_cores", missing))

		commands := []string{
			"DELETE FROM proxysql_servers",
			"LOAD PROXYSQL SERVERS FROM CONFIG",
			"LOAD PROXYSQL SERVERS TO RUNTIME;",
		}

		for _, command := range commands {
			if p.IsShuttingDown() {
				slog.Debug("skipping command during shutdown", slog.String("command", command))

				return nil
			}

			_, err := p.conn.ExecContext(ctx, command)
			if err != nil {
				return fmt.Errorf("failed to execute command '%s': %w", command, err)
			}
		}
	}

	return nil
}

// DumpData dumps the data to the configured directory.
// Currently it's only dumping query digests, because that is really all we've ever needed.
func (p *ProxySQL) DumpData(ctx context.Context) {
	tmpdir, fileErr := os.MkdirTemp("/tmp", "")
	if fileErr != nil {
		slog.Error("error in DumpData()", slog.Any("error", fileErr))

		return
	}

	digestsFile, err := p.dumpQueryDigests(ctx, tmpdir)
	if err != nil {
		slog.Error("Error in DumpQueryDigests()", slog.Any("error", err))

		return
	} else if digestsFile != "" {
		slog.Info("Saved mysql query digests to file", slog.String("filename", digestsFile))
	}
}

// ProxySQL docs: https://proxysql.com/documentation/stats-statistics/#stats_mysql_query_digest
func (p *ProxySQL) dumpQueryDigests(ctx context.Context, tmpdir string) (string, error) {
	var rowCount int

	err := p.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM stats_mysql_query_digest").Scan(&rowCount)
	if err != nil {
		return "", fmt.Errorf("failed to get query digest count: %w", err)
	}

	// Don't proceed with this function if there are no entries in the table
	if rowCount <= 0 {
		slog.Debug("no query digests found, not proceeding with DumpQueryDigests()")

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
		var schemaname, username, digest, digestText string

		var hostgroup, countStar, firstSeen, lastSeen, sumTime, minTime, maxTime, sumRowsAffected, sumRowsSent int

		err := rows.Scan(
			&hostgroup,
			&schemaname,
			&username,
			&digest,
			&digestText,
			&countStar,
			&firstSeen,
			&lastSeen,
			&sumTime,
			&minTime,
			&maxTime,
			&sumRowsAffected,
			&sumRowsSent,
		)
		if err != nil {
			return "", fmt.Errorf("failed to scan digest row: %w", err)
		}

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

		err = writer.Write(values)
		if err != nil {
			return "", fmt.Errorf("failed to write digest values: %w", err)
		}
	}

	return dumpFile, nil
}

// waitForConnectionDrain monitors client connections and waits for them to drain.
// Returns when connections are drained, timeout is reached, or context is cancelled.
func (p *ProxySQL) waitForConnectionDrain(ctx context.Context, drainTime time.Duration) {
	slog.Info("monitoring connection drain", slog.Duration("max_wait", drainTime))

	drainStart := time.Now()

	ticker := time.NewTicker(2 * time.Second) //nolint:mnd
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Warn("shutdown timeout reached during connection drain")

			return

		case <-ticker.C:
			if time.Since(drainStart) >= drainTime {
				slog.Info("drain timeout reached, proceeding with shutdown")

				return
			}

			clients, err := p.ProbeClients(ctx)
			if err != nil {
				slog.Debug("failed to check client connections during drain", slog.Any("error", err))

				continue
			}

			slog.Debug("monitoring client connections", slog.Int("clients", clients))

			if clients == 0 {
				slog.Info("all client connections drained", slog.Duration("drain_time", time.Since(drainStart)))

				return
			}
		}
	}
}

// gracefulShutdown performs the graceful shutdown logic for satellite mode.
func (p *ProxySQL) gracefulShutdown(ctx context.Context) error {
	slog.Info("starting graceful shutdown process")

	drainTime := time.Duration(p.settings.Shutdown.DrainTimeout) * time.Second
	shutdownTimeout := time.Duration(p.settings.Shutdown.ShutdownTimeout) * time.Second

	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()

	// Step 1: start draining (already done in PreStopShutdown)

	// Step 2: Monitor connection draining
	p.waitForConnectionDrain(shutdownCtx, drainTime)

	// Step 3: Enter stopping phase
	p.setShutdownPhase(PhaseStopping)

	// Step 4: Stop ProxySQL after connections have drained
	if p.conn != nil {
		slog.Info("shutting down ProxySQL")

		_, err := p.conn.ExecContext(shutdownCtx, "PROXYSQL SHUTDOWN SLOW")
		if err != nil {
			slog.Error("failed to shutdown ProxySQL",
				slog.String("command", "PROXYSQL SHUTDOWN SLOW"),
				slog.Any("conn", p.conn.Stats().OpenConnections),
				slog.Any("error", err),
			)

			// Continue with cleanup even if ProxySQL shutdown fails
		} else {
			slog.Info("ProxySQL shutdown command completed")
		}

		// Step 4: Close database connection
		slog.Info("closing database connection")

		err = p.conn.Close()
		if err != nil {
			slog.Error("failed to close database connection", slog.Any("error", err))
		} else {
			slog.Info("database connection closed")
		}

		p.conn = nil
	}

	// Step 5: Stop HTTP server
	if p.httpServer != nil {
		slog.Info("shutting down HTTP server")

		serverShutdownCtx, serverCancel := context.WithTimeout(shutdownCtx, 10*time.Second) //nolint:mnd
		defer serverCancel()

		err := p.httpServer.Shutdown(serverShutdownCtx)
		if err != nil {
			slog.Error("failed to shutdown HTTP server", slog.Any("error", err))
		} else {
			slog.Info("HTTP server shutdown completed")
		}
	}

	// Step 6: Mark as fully stopped
	p.setShutdownPhase(PhaseStopped)
	slog.Info("graceful shutdown completed successfully")

	return nil
}
