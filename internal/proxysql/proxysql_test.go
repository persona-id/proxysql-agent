package proxysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/persona-id/proxysql-agent/internal/configuration"

	"gopkg.in/DATA-DOG/go-sqlmock.v2"
)

func TestPing(t *testing.T) {
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
		shutdownPhase: PhaseRunning,
		shutdownMu:    sync.RWMutex{},
		httpServer:    nil,
	}

	err = proxy.Ping(context.Background())
	if err != nil {
		t.Errorf("Ping() returned an error: %v", err)
	}

	if proxy.conn == nil {
		t.Error("Conn should not be nil")
	}

	err = mock.ExpectationsWereMet()
	if err != nil {
		t.Errorf("SQL expectations were not met: %v", err)
	}
}

func TestProcessResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		input          ProbeResult
		expectedStatus string
		expectedMsg    string
	}{
		{
			name: "all backends online",
			input: ProbeResult{
				Backends: &struct {
					Online  int `json:"online,omitempty"`
					Shunned int `json:"shunned,omitempty"`
					Total   int `json:"total,omitempty"`
				}{Online: 3, Shunned: 0, Total: 3},
			},
			expectedStatus: "ok",
			expectedMsg:    "all backends online",
		},
		{
			name: "some backends offline",
			input: ProbeResult{
				Backends: &struct {
					Online  int `json:"online,omitempty"`
					Shunned int `json:"shunned,omitempty"`
					Total   int `json:"total,omitempty"`
				}{Online: 2, Shunned: 0, Total: 3},
			},
			expectedStatus: "ok",
			expectedMsg:    "some backends offline",
		},
		{
			name: "all backends offline",
			input: ProbeResult{
				Backends: &struct {
					Online  int `json:"online,omitempty"`
					Shunned int `json:"shunned,omitempty"`
					Total   int `json:"total,omitempty"`
				}{Online: 0, Shunned: 0, Total: 3},
			},
			expectedStatus: "ok",
			expectedMsg:    "all backends offline",
		},
		{
			name: "draining",
			input: ProbeResult{
				Draining: true,
				Backends: &struct {
					Online  int `json:"online,omitempty"`
					Shunned int `json:"shunned,omitempty"`
					Total   int `json:"total,omitempty"`
				}{Online: 3, Shunned: 0, Total: 3},
			},
			expectedStatus: "draining",
			expectedMsg:    "draining traffic",
		},
		{
			name: "draining with all backends offline",
			input: ProbeResult{
				Draining: true,
				Backends: &struct {
					Online  int `json:"online,omitempty"`
					Shunned int `json:"shunned,omitempty"`
					Total   int `json:"total,omitempty"`
				}{Online: 0, Shunned: 0, Total: 3},
			},
			expectedStatus: "draining",
			expectedMsg:    "draining traffic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := processResults(tt.input)

			if result.Status != tt.expectedStatus {
				t.Errorf("processResults() status = %q, want %q", result.Status, tt.expectedStatus)
			}

			if result.Message != tt.expectedMsg {
				t.Errorf("processResults() message = %q, want %q", result.Message, tt.expectedMsg)
			}
		})
	}
}

func TestProbeBackends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		setupMock       func(mock sqlmock.Sqlmock)
		shutdownPhase   ShutdownPhase
		expectedTotal   int
		expectedOnline  int
		expectedShunned int
		expectedErr     bool
	}{
		{
			name: "successful query",
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"total", "online", "shunned"}).
					AddRow(5, 4, 1)
				mock.ExpectQuery("SELECT").WillReturnRows(rows)
			},
			shutdownPhase:   PhaseRunning,
			expectedTotal:   5,
			expectedOnline:  4,
			expectedShunned: 1,
			expectedErr:     false,
		},
		{
			name: "query error",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT").WillReturnError(ErrDatabase)
			},
			shutdownPhase:   PhaseRunning,
			expectedTotal:   -1,
			expectedOnline:  -1,
			expectedShunned: -1,
			expectedErr:     true,
		},
		{
			name:            "shutting down returns zeros",
			setupMock:       func(_ sqlmock.Sqlmock) {},
			shutdownPhase:   PhaseDraining,
			expectedTotal:   0,
			expectedOnline:  0,
			expectedShunned: 0,
			expectedErr:     false,
		},
		{
			name: "empty table returns zeros not scan error",
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"total", "online", "shunned"}).
					AddRow(0, nil, nil)
				mock.ExpectQuery("SELECT").WillReturnRows(rows)
			},
			shutdownPhase:   PhaseRunning,
			expectedTotal:   0,
			expectedOnline:  0,
			expectedShunned: 0,
			expectedErr:     false,
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
				shutdownPhase: tt.shutdownPhase,
			}

			tt.setupMock(mock)

			total, online, shunned, err := proxy.probeBackends(context.Background())

			if (err != nil) != tt.expectedErr {
				t.Errorf("probeBackends() error = %v, wantErr %v", err, tt.expectedErr)
			}

			if total != tt.expectedTotal {
				t.Errorf("probeBackends() total = %d, want %d", total, tt.expectedTotal)
			}

			if online != tt.expectedOnline {
				t.Errorf("probeBackends() online = %d, want %d", online, tt.expectedOnline)
			}

			if shunned != tt.expectedShunned {
				t.Errorf("probeBackends() shunned = %d, want %d", shunned, tt.expectedShunned)
			}

			mockErr := mock.ExpectationsWereMet()
			if mockErr != nil {
				t.Errorf("SQL expectations not met: %v", mockErr)
			}
		})
	}
}

func TestProbeClients(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupMock     func(mock sqlmock.Sqlmock)
		shutdownPhase ShutdownPhase
		expectedCount int
		expectedErr   bool
	}{
		{
			name: "returns connected client count",
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"Client_Connections_connected"}).AddRow(5)
				mock.ExpectQuery("SELECT").WillReturnRows(rows)
			},
			shutdownPhase: PhaseRunning,
			expectedCount: 5,
			expectedErr:   false,
		},
		{
			name: "null result returns zero not minus one",
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"Client_Connections_connected"}).AddRow(nil)
				mock.ExpectQuery("SELECT").WillReturnRows(rows)
			},
			shutdownPhase: PhaseRunning,
			expectedCount: 0,
			expectedErr:   false,
		},
		{
			name: "returns actual count while draining",
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"Client_Connections_connected"}).AddRow(15)
				mock.ExpectQuery("SELECT").WillReturnRows(rows)
			},
			shutdownPhase: PhaseDraining,
			expectedCount: 15,
			expectedErr:   false,
		},
		{
			name: "query error returns error",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT").WillReturnError(ErrDatabase)
			},
			shutdownPhase: PhaseRunning,
			expectedCount: -1,
			expectedErr:   true,
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
				shutdownPhase: tt.shutdownPhase,
			}

			tt.setupMock(mock)

			count, err := proxy.ProbeClients(context.Background())

			if (err != nil) != tt.expectedErr {
				t.Errorf("ProbeClients() error = %v, wantErr %v", err, tt.expectedErr)
			}

			if count != tt.expectedCount {
				t.Errorf("ProbeClients() count = %d, want %d", count, tt.expectedCount)
			}

			mockErr := mock.ExpectationsWereMet()
			if mockErr != nil {
				t.Errorf("SQL expectations not met: %v", mockErr)
			}
		})
	}
}

// Return a config for testing purposes.
// This method is used in all the test files in this directory.
func newTestConfig() *configuration.Config {
	return &configuration.Config{
		StartDelay: 0,
		Log: struct {
			Level  string `mapstructure:"level"`
			Format string `mapstructure:"format"`
			Source bool   `mapstructure:"source"`
			Probes bool   `mapstructure:"probes"`
		}{
			Level:  "INFO",
			Format: "text",
			Source: false,
			Probes: false,
		},
		ProxySQL: struct {
			Address        string `mapstructure:"address"`
			Username       string `mapstructure:"username"`
			Password       string `mapstructure:"password"`
			ConnectTimeout int    `mapstructure:"connect_timeout"`
		}{
			Address:  "127.0.0.1:6032",
			Username: "radmin",
			Password: "",
		},
		RunMode: "",
		Core: struct {
			PodSelector struct {
				Namespace string `mapstructure:"namespace"`
				App       string `mapstructure:"app"`
				Component string `mapstructure:"component"`
			} `mapstructure:"podselector"`
			Interval int `mapstructure:"interval"`
		}{
			PodSelector: struct {
				Namespace string `mapstructure:"namespace"`
				App       string `mapstructure:"app"`
				Component string `mapstructure:"component"`
			}{
				Namespace: "proxysql",
				App:       "proxysql",
				Component: "core",
			},
			Interval: 10,
		},
		Satellite: struct {
			Interval int `mapstructure:"interval"`
		}{
			Interval: 10,
		},
		Interfaces: []string{},
	}
}

// pingSequenceDriver hands out connections whose Ping fails a fixed number of times
// before succeeding. The vendored sqlmock cannot mock pings, and a refused ping is the
// exact condition waitForProxySQL exists to ride out.
type pingSequenceDriver struct {
	remainingFailures *atomic.Int32
}

func (d pingSequenceDriver) Open(_ string) (driver.Conn, error) {
	return pingSequenceConn(d), nil
}

type pingSequenceConn struct {
	remainingFailures *atomic.Int32
}

func (c pingSequenceConn) Ping(_ context.Context) error {
	if c.remainingFailures.Add(-1) >= 0 {
		return errConnectionRefused
	}

	return nil
}

func (c pingSequenceConn) Prepare(_ string) (driver.Stmt, error) { return nil, errNotImplemented }
func (c pingSequenceConn) Close() error                          { return nil }
func (c pingSequenceConn) Begin() (driver.Tx, error)             { return nil, errNotImplemented }

var (
	errConnectionRefused = errors.New("connect: connection refused")
	errNotImplemented    = errors.New("not implemented")
)

// openPingSequenceDB registers a uniquely named driver whose first `failures` pings fail.
func openPingSequenceDB(t *testing.T, failures int32) *sql.DB {
	t.Helper()

	remaining := &atomic.Int32{}
	remaining.Store(failures)

	name := "ping-sequence-" + t.Name()
	sql.Register(name, pingSequenceDriver{remainingFailures: remaining})

	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open() returned an error: %v", err)
	}

	t.Cleanup(func() { db.Close() })

	return db
}

func TestWaitForProxySQLSucceedsImmediately(t *testing.T) {
	t.Parallel()

	db := openPingSequenceDB(t, 0)

	err := waitForProxySQL(context.Background(), db, time.Second, time.Millisecond, "127.0.0.1:6032")
	if err != nil {
		t.Errorf("waitForProxySQL() returned an error: %v", err)
	}
}

func TestWaitForProxySQLRetriesUntilReady(t *testing.T) {
	t.Parallel()

	db := openPingSequenceDB(t, 3)

	err := waitForProxySQL(context.Background(), db, time.Second, time.Millisecond, "127.0.0.1:6032")
	if err != nil {
		t.Errorf("waitForProxySQL() returned an error after retrying: %v", err)
	}
}

func TestWaitForProxySQLGivesUpAfterTimeout(t *testing.T) {
	t.Parallel()

	// Never succeeds, so the loop has to be bounded by the timeout.
	db := openPingSequenceDB(t, math.MaxInt32)

	start := time.Now()

	err := waitForProxySQL(context.Background(), db, 50*time.Millisecond, time.Millisecond, "127.0.0.1:6032")
	if err == nil {
		t.Fatal("waitForProxySQL() should have returned an error when ProxySQL never came up")
	}

	if !errors.Is(err, ErrProxySQLUnreachable) {
		t.Errorf("error should wrap ErrProxySQLUnreachable, got: %v", err)
	}

	// The underlying ping failure should survive in the chain for debugging.
	if !errors.Is(err, errConnectionRefused) {
		t.Errorf("error should wrap the ping failure, got: %v", err)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waitForProxySQL() overran its timeout, took %s", elapsed)
	}
}

// A zero timeout preserves the original single-attempt behavior, so callers that want to
// fail fast still can.
func TestWaitForProxySQLZeroTimeoutMakesOneAttempt(t *testing.T) {
	t.Parallel()

	db := openPingSequenceDB(t, math.MaxInt32)

	err := waitForProxySQL(context.Background(), db, 0, time.Millisecond, "127.0.0.1:6032")
	if err == nil {
		t.Fatal("waitForProxySQL() should have returned an error with a zero timeout")
	}

	if !errors.Is(err, ErrProxySQLUnreachable) {
		t.Errorf("error should wrap ErrProxySQLUnreachable, got: %v", err)
	}

	if !strings.Contains(err.Error(), "after 1 attempts") {
		t.Errorf("a zero timeout should make exactly one attempt, got: %v", err)
	}
}

func TestWaitForProxySQLStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	db := openPingSequenceDB(t, math.MaxInt32)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitForProxySQL(ctx, db, time.Hour, time.Millisecond, "127.0.0.1:6032")
	if err == nil {
		t.Fatal("waitForProxySQL() should have returned an error on a cancelled context")
	}

	if !errors.Is(err, ErrProxySQLUnreachable) {
		t.Errorf("error should wrap ErrProxySQLUnreachable, got: %v", err)
	}
}
