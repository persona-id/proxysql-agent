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
				httpServers:   nil,
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
		httpServers:   nil,
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

	mock.ExpectExec("PROXYSQL PAUSE").WillReturnResult(sqlmock.NewResult(0, 0))
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
		httpServers:   []*http.Server{server},
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
	// When the drain context expires, gracefulShutdown still sends PROXYSQL PAUSE
	// and closes the admin connection. It does not send PROXYSQL SHUTDOWN: that
	// command exits pid 1 while the preStop wget, running in the proxysql
	// container, is waiting on the HTTP response. The kubelet SIGTERMs proxysql
	// after the hook returns. PAUSE uses a context that survives drain-budget
	// expiry, so it still runs when the drain context is already done.
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock DB: %v", err)
	}

	defer db.Close()

	mock.MatchExpectationsInOrder(true)
	mock.ExpectExec("PROXYSQL PAUSE").WillReturnResult(sqlmock.NewResult(0, 0))
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

func TestPauseProxySQLNoopWithoutConn(t *testing.T) {
	t.Parallel()

	proxy := &ProxySQL{
		conn:          nil,
		settings:      newTestConfig(),
		shutdownPhase: PhaseDraining,
		shutdownMu:    sync.RWMutex{},
	}

	proxy.pauseProxySQL(context.Background()) // must not panic
}

const connectedClientsSQL = "SELECT Variable_Value FROM stats_mysql_global WHERE Variable_Name = 'Client_Connections_connected'"

// probeReading is one scripted response to the live connected-clients probe.
type probeReading struct {
	value string
	err   error
}

func TestGracefulShutdownDrain(t *testing.T) {
	// gracefulShutdown returns as soon as the live connected-client count reads
	// zero on two consecutive ticks, and otherwise waits out drain_timeout.
	//
	// This replaces an earlier test that asserted the drain never exited early,
	// on the grounds that returning early let the kubelet SIGTERM proxysql before
	// kube-proxy removed the endpoint (~15-20s), so new connections got RST'd.
	// That predates PROXYSQL PAUSE (1.2.3): PAUSE now stops the frontend listener
	// at the start of the drain, so a connect arriving during endpoint
	// propagation is refused whether proxysql is still running or not. An early
	// exit therefore adds no new RSTs; it only stops the pod idling once every
	// client has gone.
	t.Parallel()

	tests := []struct {
		name            string
		readings        []probeReading
		drainTimeout    int
		shutdownTimeout int
		tickInterval    time.Duration
		wantFullWait    bool
	}{
		{
			name:            "exits early once clients drain",
			readings:        []probeReading{{value: "3"}, {value: "0"}, {value: "0"}},
			drainTimeout:    10,
			shutdownTimeout: 15,
			tickInterval:    20 * time.Millisecond,
			wantFullWait:    false,
		},
		{
			name:            "single zero reading does not exit",
			readings:        []probeReading{{value: "0"}, {value: "2"}, {value: "0"}, {value: "0"}},
			drainTimeout:    10,
			shutdownTimeout: 15,
			tickInterval:    20 * time.Millisecond,
			wantFullWait:    false,
		},
		{
			name: "probe errors never exit and reset the debounce",
			readings: []probeReading{
				{err: errSQLTest},
				{value: "0"},
				{err: errSQLTest},
				{value: "not-a-number"},
				{value: "0"},
				{value: "0"},
			},
			drainTimeout:    10,
			shutdownTimeout: 15,
			tickInterval:    20 * time.Millisecond,
			wantFullWait:    false,
		},
		{
			// Ticks at ~400ms and ~800ms probe; the ~1.2s tick hits drain_timeout.
			name:            "waits for drain timeout when clients never drain",
			readings:        []probeReading{{value: "3"}, {value: "1"}},
			drainTimeout:    1,
			shutdownTimeout: 5,
			tickInterval:    400 * time.Millisecond,
			wantFullWait:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create mock DB: %v", err)
			}

			defer db.Close()

			// In-order matching: if the drain exits before the script is consumed,
			// Close arrives ahead of the remaining probe expectations and they are
			// reported unmet.
			mock.MatchExpectationsInOrder(true)
			mock.ExpectExec("PROXYSQL PAUSE").WillReturnResult(sqlmock.NewResult(0, 0))

			for _, reading := range tt.readings {
				expectation := mock.ExpectQuery(regexp.QuoteMeta(connectedClientsSQL))
				if reading.err != nil {
					expectation.WillReturnError(reading.err)
				} else {
					expectation.WillReturnRows(sqlmock.NewRows([]string{"Variable_Value"}).AddRow(reading.value))
				}
			}

			mock.ExpectClose()

			cfg := newTestConfig()
			cfg.Shutdown.DrainTimeout = tt.drainTimeout
			cfg.Shutdown.ShutdownTimeout = tt.shutdownTimeout

			proxy := &ProxySQL{
				conn:              db,
				settings:          cfg,
				shutdownPhase:     PhaseDraining,
				shutdownMu:        sync.RWMutex{},
				drainTickInterval: tt.tickInterval,
			}

			start := time.Now()

			if err := proxy.gracefulShutdown(context.Background()); err != nil {
				t.Errorf("gracefulShutdown() unexpected error: %v", err)
			}

			elapsed := time.Since(start)
			drainTime := time.Duration(tt.drainTimeout) * time.Second

			if tt.wantFullWait && elapsed < drainTime {
				t.Errorf("gracefulShutdown() exited before drain timeout: elapsed=%v, want >= %v", elapsed, drainTime)
			}

			if !tt.wantFullWait && elapsed >= drainTime {
				t.Errorf("gracefulShutdown() did not exit early: elapsed=%v, drain timeout %v", elapsed, drainTime)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("SQL expectations not met: %v", err)
			}

			proxy.shutdownMu.RLock()
			phase := proxy.shutdownPhase
			proxy.shutdownMu.RUnlock()

			if phase != PhaseStopped {
				t.Errorf("shutdownPhase = %v, want PhaseStopped", phase)
			}
		})
	}
}

func TestGracefulShutdownWithoutConnReturnsImmediately(t *testing.T) {
	// With no admin connection there is nothing to drain, so the drain returns
	// without waiting for drain_timeout.
	t.Parallel()

	cfg := newTestConfig()
	cfg.Shutdown.DrainTimeout = 5
	cfg.Shutdown.ShutdownTimeout = 10

	proxy := &ProxySQL{
		conn:              nil,
		settings:          cfg,
		shutdownPhase:     PhaseDraining,
		shutdownMu:        sync.RWMutex{},
		drainTickInterval: 50 * time.Millisecond,
	}

	start := time.Now()

	if err := proxy.gracefulShutdown(context.Background()); err != nil {
		t.Errorf("gracefulShutdown() unexpected error: %v", err)
	}

	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("gracefulShutdown() with nil conn took %v, want immediate return", elapsed)
	}
}

func TestProbeConnectedClients(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock sqlmock.Sqlmock)
		want      int
		wantErr   bool
	}{
		{
			name: "parses string value",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(connectedClientsSQL)).WillReturnRows(sqlmock.NewRows([]string{"Variable_Value"}).AddRow("42"))
			},
			want:    42,
			wantErr: false,
		},
		{
			name: "zero",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(connectedClientsSQL)).WillReturnRows(sqlmock.NewRows([]string{"Variable_Value"}).AddRow("0"))
			},
			want:    0,
			wantErr: false,
		},
		{
			name: "trims whitespace",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(connectedClientsSQL)).WillReturnRows(sqlmock.NewRows([]string{"Variable_Value"}).AddRow(" 7 "))
			},
			want:    7,
			wantErr: false,
		},
		{
			name: "integer value",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(connectedClientsSQL)).WillReturnRows(sqlmock.NewRows([]string{"Variable_Value"}).AddRow(int64(5)))
			},
			want:    5,
			wantErr: false,
		},
		{
			name: "unparseable value",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(connectedClientsSQL)).WillReturnRows(sqlmock.NewRows([]string{"Variable_Value"}).AddRow("abc"))
			},
			want:    -1,
			wantErr: true,
		},
		{
			name: "no row",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(connectedClientsSQL)).WillReturnRows(sqlmock.NewRows([]string{"Variable_Value"}))
			},
			want:    -1,
			wantErr: true,
		},
		{
			name: "query error",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(connectedClientsSQL)).WillReturnError(ErrDatabase)
			},
			want:    -1,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create mock DB: %v", err)
			}

			defer db.Close()

			tt.setupMock(mock)

			proxy := &ProxySQL{
				conn:          db,
				settings:      newTestConfig(),
				shutdownPhase: PhaseRunning,
				shutdownMu:    sync.RWMutex{},
			}

			got, err := proxy.probeConnectedClients(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("probeConnectedClients() error = %v, wantErr %v", err, tt.wantErr)
			}

			if got != tt.want {
				t.Errorf("probeConnectedClients() = %d, want %d", got, tt.want)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("SQL expectations not met: %v", err)
			}
		})
	}
}

func TestProbeConnectedClientsNilConn(t *testing.T) {
	t.Parallel()

	proxy := &ProxySQL{
		conn:          nil,
		settings:      newTestConfig(),
		shutdownPhase: PhaseRunning,
		shutdownMu:    sync.RWMutex{},
	}

	got, err := proxy.probeConnectedClients(context.Background())
	if err != nil || got != 0 {
		t.Errorf("probeConnectedClients() with nil conn = (%d, %v), want (0, nil)", got, err)
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
