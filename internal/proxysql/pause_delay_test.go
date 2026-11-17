package proxysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

var errUnsupported = errors.New("not supported by execRecorder")

// recordedExec is one statement the agent sent to the admin interface.
type recordedExec struct {
	at           time.Time
	query        string
	shuttingDown bool // readiness was already 503 when the statement ran
}

// execRecorder is a minimal database/sql connector that records every Exec with the time
// it ran and whether the agent was already reporting not-ready at that moment.
type execRecorder struct {
	proxy *ProxySQL
	execs []recordedExec
	mu    sync.Mutex
}

func (r *execRecorder) Connect(_ context.Context) (driver.Conn, error) { return &recorderConn{r}, nil }
func (r *execRecorder) Driver() driver.Driver                          { return recorderDriver{r} }

func (r *execRecorder) record(query string) {
	shuttingDown := r.proxy.IsShuttingDown()

	r.mu.Lock()
	defer r.mu.Unlock()

	r.execs = append(r.execs, recordedExec{at: time.Now(), query: query, shuttingDown: shuttingDown})
}

// pauses returns every PROXYSQL PAUSE recorded so far.
func (r *execRecorder) pauses() []recordedExec {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]recordedExec, 0, len(r.execs))

	for _, e := range r.execs {
		if e.query == "PROXYSQL PAUSE" {
			out = append(out, e)
		}
	}

	return out
}

type recorderDriver struct{ r *execRecorder }

func (d recorderDriver) Open(_ string) (driver.Conn, error) { return &recorderConn{d.r}, nil }

type recorderConn struct{ r *execRecorder }

func (c *recorderConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.r.record(query)

	return driver.RowsAffected(0), nil
}

func (c *recorderConn) Prepare(_ string) (driver.Stmt, error) { return nil, errUnsupported }
func (c *recorderConn) Close() error                          { return nil }
func (c *recorderConn) Begin() (driver.Tx, error)             { return nil, errUnsupported }

func TestShutdownPauseDelay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		runMode         string
		entry           string // "prestop" calls PreStopShutdown, "sigterm" cancels Satellite's ctx
		pauseDelay      int
		shutdownTimeout int
		wantPauseAfter  time.Duration
		wantPauseBefore time.Duration
	}{
		{
			name:            "satellite prestop without delay pauses immediately",
			runMode:         "satellite",
			entry:           "prestop",
			pauseDelay:      0,
			shutdownTimeout: 5,
			wantPauseAfter:  0,
			wantPauseBefore: 500 * time.Millisecond,
		},
		{
			name:            "satellite sigterm without delay pauses immediately",
			runMode:         "satellite",
			entry:           "sigterm",
			pauseDelay:      0,
			shutdownTimeout: 5,
			wantPauseAfter:  0,
			wantPauseBefore: 500 * time.Millisecond,
		},
		{
			name:            "satellite prestop waits pause_delay",
			runMode:         "satellite",
			entry:           "prestop",
			pauseDelay:      1,
			shutdownTimeout: 5,
			wantPauseAfter:  time.Second,
			wantPauseBefore: 3 * time.Second,
		},
		{
			name:            "satellite sigterm waits pause_delay",
			runMode:         "satellite",
			entry:           "sigterm",
			pauseDelay:      1,
			shutdownTimeout: 5,
			wantPauseAfter:  time.Second,
			wantPauseBefore: 3 * time.Second,
		},
		{
			name:            "shutdown_timeout cuts the pause delay short",
			runMode:         "satellite",
			entry:           "prestop",
			pauseDelay:      30,
			shutdownTimeout: 1,
			wantPauseAfter:  time.Second,
			wantPauseBefore: 3 * time.Second,
		},
		{
			name:            "core ignores pause_delay",
			runMode:         "core",
			entry:           "prestop",
			pauseDelay:      1,
			shutdownTimeout: 5,
			wantPauseAfter:  0,
			wantPauseBefore: 500 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := newTestConfig()
			cfg.RunMode = tt.runMode
			cfg.Shutdown.DrainingFile = t.TempDir() + "/draining"
			cfg.Shutdown.DrainTimeout = 0 // the drain loop exits on its first tick
			cfg.Shutdown.PauseDelay = tt.pauseDelay
			cfg.Shutdown.ShutdownTimeout = tt.shutdownTimeout

			recorder := &execRecorder{}
			db := sql.OpenDB(recorder)

			proxy := &ProxySQL{
				conn:              db,
				settings:          cfg,
				shutdownPhase:     PhaseRunning,
				shutdownMu:        sync.RWMutex{},
				drainTickInterval: 10 * time.Millisecond,
			}
			recorder.proxy = proxy

			done := make(chan error, 1)
			start := time.Now()

			switch tt.entry {
			case "prestop":
				go func() { done <- proxy.PreStopShutdown() }()
			case "sigterm":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				go func() { done <- proxy.Satellite(ctx) }()
			default:
				t.Fatalf("unknown entry %q", tt.entry)
			}

			if tt.wantPauseAfter > 0 {
				time.Sleep(tt.wantPauseAfter / 2)

				if !proxy.IsShuttingDown() {
					t.Error("readiness should already be 503 during the pause delay")
				}

				if _, err := os.Stat(cfg.Shutdown.DrainingFile); err != nil {
					t.Errorf("drain file should exist during the pause delay: %v", err)
				}

				if got := recorder.pauses(); len(got) != 0 {
					t.Errorf("PROXYSQL PAUSE ran during the pause delay (%d times)", len(got))
				}
			}

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("shutdown returned error: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("shutdown did not finish within 10s")
			}

			// A second shutdown request must be a no-op thanks to shutdownOnce.
			if err := proxy.PreStopShutdown(); err != nil {
				t.Errorf("second PreStopShutdown() returned error: %v", err)
			}

			pauses := recorder.pauses()
			if len(pauses) != 1 {
				t.Fatalf("PROXYSQL PAUSE ran %d times, want 1", len(pauses))
			}

			elapsed := pauses[0].at.Sub(start)
			if elapsed < tt.wantPauseAfter || elapsed > tt.wantPauseBefore {
				t.Errorf("PROXYSQL PAUSE ran %v after shutdown started, want within [%v, %v]", elapsed, tt.wantPauseAfter, tt.wantPauseBefore)
			}

			if !pauses[0].shuttingDown {
				t.Error("PROXYSQL PAUSE ran before readiness turned 503")
			}
		})
	}
}

func TestPauseDelay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		runMode    string
		pauseDelay int
		want       time.Duration
	}{
		{name: "satellite without delay", runMode: "satellite", pauseDelay: 0, want: 0},
		{name: "satellite with delay", runMode: "satellite", pauseDelay: 20, want: 20 * time.Second},
		{name: "satellite with negative delay", runMode: "satellite", pauseDelay: -5, want: 0},
		{name: "core ignores delay", runMode: "core", pauseDelay: 20, want: 0},
		{name: "empty run mode ignores delay", runMode: "", pauseDelay: 20, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := newTestConfig()
			cfg.RunMode = tt.runMode
			cfg.Shutdown.PauseDelay = tt.pauseDelay

			proxy := &ProxySQL{settings: cfg}

			if got := proxy.pauseDelay(); got != tt.want {
				t.Errorf("pauseDelay() = %v, want %v", got, tt.want)
			}
		})
	}
}
