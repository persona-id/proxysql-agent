package proxysql

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"gopkg.in/DATA-DOG/go-sqlmock.v2"
)

func TestGetMissingCorePods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		expectedCount int
		expectedErr   error
		setupMock     func(mock sqlmock.Sqlmock)
	}{
		{
			name: "successful query",
			setupMock: func(mock sqlmock.Sqlmock) {
				query := regexp.QuoteMeta("SELECT COUNT(hostname) FROM stats_proxysql_servers_metrics WHERE last_check_ms > 30000 AND hostname != 'proxysql-core' AND Uptime_s > 0")
				mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			},
			expectedCount: 1,
			expectedErr:   nil,
		},
		{
			name: "database error",
			setupMock: func(mock sqlmock.Sqlmock) {
				query := regexp.QuoteMeta("SELECT COUNT(hostname) FROM stats_proxysql_servers_metrics WHERE last_check_ms > 30000 AND hostname != 'proxysql-core' AND Uptime_s > 0")
				mock.ExpectQuery(query).WillReturnError(ErrDatabase)
			},
			expectedCount: -1,
			expectedErr:   ErrDatabase,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("Error creating mock database: %v", err)
			}
			defer db.Close()

			proxy := &ProxySQL{
				clientset:     nil,
				conn:          db,
				settings:      newTestConfig(),
				shutdownOnce:  sync.Once{},
				shutdownMu:    sync.RWMutex{},
				shutdownPhase: PhaseRunning,
				httpServer:    nil,
			}

			// Setup the mock
			tt.setupMock(mock)

			// Call the function being tested
			count, err := proxy.GetMissingCorePods(context.Background())

			// Check error
			switch {
			case tt.expectedErr == nil && err != nil:
				t.Errorf("GetMissingCorePods() returned unexpected error: %v", err)
			case tt.expectedErr != nil && err == nil:
				t.Errorf("GetMissingCorePods() expected error: %v, got nil", tt.expectedErr)
			case tt.expectedErr != nil && err != nil:
				// Check if the wrapped error contains the expected error
				if !errors.Is(err, tt.expectedErr) {
					t.Errorf("GetMissingCorePods() expected error to wrap: %v, got: %v", tt.expectedErr, err)
				}
			}

			// Check count
			if count != tt.expectedCount {
				t.Errorf("GetMissingCorePods() expected count: %v, got: %v", tt.expectedCount, count)
			}

			// Verify all expectations were met
			err = mock.ExpectationsWereMet()
			if err != nil {
				t.Errorf("SQL expectations were not met: %v", err)
			}
		})
	}
}

func TestSatelliteResync(t *testing.T) {
	t.Parallel()

	// Mock database connection
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("An error '%s' was not expected when opening a mock database connection", err)
	}
	defer db.Close()

	mock.MatchExpectationsInOrder(true)

	p := &ProxySQL{
		clientset:     nil,
		conn:          db,
		httpServer:    nil,
		settings:      newTestConfig(),
		shutdownMu:    sync.RWMutex{},
		shutdownOnce:  sync.Once{},
		shutdownPhase: PhaseRunning,
	}

	query := regexp.QuoteMeta("SELECT COUNT(hostname) FROM stats_proxysql_servers_metrics WHERE last_check_ms > 30000 AND hostname != 'proxysql-core' AND Uptime_s > 0")
	mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	commands := []string{
		"DELETE FROM proxysql_servers",
		"LOAD PROXYSQL SERVERS FROM CONFIG",
		"LOAD PROXYSQL SERVERS TO RUNTIME",
	}
	for _, command := range commands {
		mock.ExpectExec(command).WillReturnResult(sqlmock.NewResult(1, 1))
	}

	err = p.SatelliteResync(context.Background())
	if err != nil {
		t.Errorf("Expected no error, but got %s", err)
	}

	err = mock.ExpectationsWereMet()
	if err != nil {
		t.Errorf("There were unfulfilled expectations: %s", err)
	}
}

func TestGracefulShutdownDoesNotCloseHTTPServer(t *testing.T) {
	// gracefulShutdown should NOT shut down the HTTP server.
	// The preStop handler calls gracefulShutdown from within an active HTTP request.
	// If gracefulShutdown calls httpServer.Shutdown(), it deadlocks: Shutdown waits
	// for the in-flight preStop request to complete, but that request is blocked waiting
	// for gracefulShutdown to return. The HTTP server is shut down by Satellite() instead,
	// which runs outside of any HTTP handler.
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock DB: %v", err)
	}

	defer db.Close()

	mock.ExpectExec("PROXYSQL SHUTDOWN SLOW").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	// Short shutdown timeout so the drain loop exits quickly via context expiry.
	cfg := newTestConfig()
	cfg.Shutdown.ShutdownTimeout = 1

	// Start a real HTTP server on a random port to detect whether it gets shut down.
	ln, listenErr := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatalf("failed to create listener: %v", listenErr)
	}

	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() { _ = server.Serve(ln) }()

	defer server.Close()

	proxy := &ProxySQL{
		conn:          db,
		settings:      cfg,
		shutdownPhase: PhaseDraining,
		shutdownMu:    sync.RWMutex{},
		httpServer:    server,
	}

	if err := proxy.gracefulShutdown(context.Background()); err != nil {
		t.Errorf("gracefulShutdown() returned unexpected error: %v", err)
	}

	// The HTTP server must still be accepting connections after gracefulShutdown returns.
	addr := ln.Addr().String()

	conn, dialErr := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "tcp", addr)
	if dialErr != nil {
		t.Errorf("HTTP server should still be running after gracefulShutdown(), got: %v", dialErr)
	} else {
		conn.Close()
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL expectations not met: %v", err)
	}
}

func TestStartDraining(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		drainFile   func(t *testing.T) string
		expectedErr bool
	}{
		{
			name: "creates drain file and sets phase without sql",
			drainFile: func(t *testing.T) string {
				t.Helper()

				return t.TempDir() + "/draining"
			},
			expectedErr: false,
		},
		{
			name: "returns error when drain file path is invalid",
			drainFile: func(_ *testing.T) string {
				return "/nonexistent/path/draining"
			},
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			drainFile := tt.drainFile(t)
			cfg := newTestConfig()
			cfg.Shutdown.DrainingFile = drainFile

			proxy := &ProxySQL{
				conn:          nil, // no SQL should be executed during startDraining
				settings:      cfg,
				shutdownPhase: PhaseRunning,
				shutdownMu:    sync.RWMutex{},
			}

			err := proxy.startDraining()

			if (err != nil) != tt.expectedErr {
				t.Errorf("startDraining() error = %v, wantErr %v", err, tt.expectedErr)
			}

			if !tt.expectedErr {
				if _, statErr := os.Stat(drainFile); statErr != nil {
					t.Errorf("drain file should exist: %v", statErr)
				}

				proxy.shutdownMu.RLock()
				phase := proxy.shutdownPhase
				proxy.shutdownMu.RUnlock()

				if phase != PhaseDraining {
					t.Errorf("shutdownPhase = %v, want PhaseDraining", phase)
				}
			}
		})
	}
}

func TestGracefulShutdownCallsProxySQLShutdown(t *testing.T) {
	// When the drain context expires (the common case — drain timeout fires before
	// all clients disconnect), gracefulShutdown must still send "PROXYSQL SHUTDOWN SLOW"
	// to ProxySQL so it can drain its own active sessions gracefully.
	// The bug: ExecContext(shutdownCtx, ...) fails immediately with context.DeadlineExceeded
	// when shutdownCtx is already expired, silently skipping the shutdown command.
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock DB: %v", err)
	}

	defer db.Close()

	mock.MatchExpectationsInOrder(true)
	mock.ExpectExec("PROXYSQL SHUTDOWN SLOW").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	proxy := &ProxySQL{
		conn:          db,
		settings:      newTestConfig(),
		shutdownPhase: PhaseDraining,
		shutdownMu:    sync.RWMutex{},
	}

	// Use a pre-cancelled context so shutdownCtx (derived from it) is immediately expired.
	// This simulates the common case where the drain timeout fires.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := proxy.gracefulShutdown(ctx); err != nil {
		t.Errorf("gracefulShutdown() unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL expectations not met: %v", err)
	}
}

func TestGracefulShutdownWaitsForDrainTimeout(t *testing.T) {
	// gracefulShutdown must NOT exit early when clients == 0.
	// With 64 pods, any given pod frequently has zero active queries at T=2s.
	// Exiting early means PROXYSQL SHUTDOWN SLOW fires before kube-proxy removes
	// the endpoint (~15-20s), so new connections get RST'd during TCP/SSL handshake.
	t.Parallel()

	cfg := newTestConfig()
	cfg.Shutdown.DrainTimeout = 1 // 1 second minimum drain time
	cfg.Shutdown.ShutdownTimeout = 5

	proxy := &ProxySQL{
		conn:              nil, // ProbeClients returns 0 with nil conn — simulates zero active clients
		settings:          cfg,
		shutdownPhase:     PhaseDraining,
		shutdownMu:        sync.RWMutex{},
		drainTickInterval: 50 * time.Millisecond,
	}

	start := time.Now()

	if err := proxy.gracefulShutdown(context.Background()); err != nil {
		t.Errorf("gracefulShutdown() unexpected error: %v", err)
	}

	elapsed := time.Since(start)
	drainTime := time.Duration(cfg.Shutdown.DrainTimeout) * time.Second

	if elapsed < drainTime {
		t.Errorf("gracefulShutdown exited before drainTime: elapsed=%v, want >= %v (early clients==0 exit still present)", elapsed, drainTime)
	}
}

func TestDumpQueryDigests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupMock   func(mock sqlmock.Sqlmock)
		expectedErr bool
	}{
		{
			name: "query error returns error without panic",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT COUNT").WillReturnRows(
					sqlmock.NewRows([]string{"count"}).AddRow(1),
				)
				mock.ExpectQuery("SELECT \\* FROM stats_mysql_query_digest").
					WillReturnError(errSQLTest)
			},
			expectedErr: true,
		},
		{
			name: "no digests returns empty string",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT COUNT").WillReturnRows(
					sqlmock.NewRows([]string{"count"}).AddRow(0),
				)
			},
			expectedErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("Error creating mock database: %v", err)
			}
			defer db.Close()

			proxy := &ProxySQL{
				conn:          db,
				settings:      newTestConfig(),
				shutdownPhase: PhaseRunning,
			}

			tt.setupMock(mock)

			tmpdir := t.TempDir()

			_, err = proxy.dumpQueryDigests(context.Background(), tmpdir)

			if (err != nil) != tt.expectedErr {
				t.Errorf("dumpQueryDigests() error = %v, wantErr %v", err, tt.expectedErr)
			}

			mockErr := mock.ExpectationsWereMet()
			if mockErr != nil {
				t.Errorf("SQL expectations not met: %v", mockErr)
			}
		})
	}
}
