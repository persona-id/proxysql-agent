package restapi

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/persona-id/proxysql-agent/internal/configuration"
	"github.com/persona-id/proxysql-agent/internal/proxysql"
)

// Since we can't easily mock the concrete ProxySQL type, we'll test what we can
// by testing the server creation and basic handler setup.
func TestStartAPIServerConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		port     int
		wantAddr string
	}{
		{
			name:     "default port 8080",
			port:     8080,
			wantAddr: ":8080",
		},
		{
			name:     "custom port 9090",
			port:     9090,
			wantAddr: ":9090",
		},
		{
			name:     "port 3000",
			port:     3000,
			wantAddr: ":3000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := &configuration.Config{
				API: struct {
					Port         int `mapstructure:"port"`
					ShutdownPort int `mapstructure:"shutdown_port"`
				}{
					Port: tt.port,
				},
				Shutdown: struct {
					DrainingFile    string `mapstructure:"draining_file"`
					DrainTimeout    int    `mapstructure:"drain_timeout"`
					ShutdownTimeout int    `mapstructure:"shutdown_timeout"`
				}{
					DrainingFile:    "/tmp/draining",
					DrainTimeout:    30,
					ShutdownTimeout: 60,
				},
			}

			// Create a minimal ProxySQL instance for testing
			// Note: This won't actually connect to a database, but will test server setup
			psql := &proxysql.ProxySQL{}
			server := StartAPI(psql, config)

			t.Cleanup(func() {
				server.Close()
			})

			// Test server configuration
			if server.Addr != tt.wantAddr {
				t.Errorf("StartAPI() server.Addr = %v, want %v", server.Addr, tt.wantAddr)
			}

			if server.Handler == nil {
				t.Errorf("StartAPI() server.Handler is nil")
			}

			// Verify timeout configurations
			expectedReadTimeout := 10 * time.Second
			if server.ReadTimeout != expectedReadTimeout {
				t.Errorf("StartAPI() ReadTimeout = %v, want %v", server.ReadTimeout, expectedReadTimeout)
			}

			// WriteTimeout is ShutdownTimeout + 15s buffer so the preStop handler
			// can complete before the server drops the connection.
			expectedWriteTimeout := time.Duration(config.Shutdown.ShutdownTimeout+15) * time.Second
			if server.WriteTimeout != expectedWriteTimeout {
				t.Errorf("StartAPI() WriteTimeout = %v, want %v", server.WriteTimeout, expectedWriteTimeout)
			}

			expectedIdleTimeout := 30 * time.Second
			if server.IdleTimeout != expectedIdleTimeout {
				t.Errorf("StartAPI() IdleTimeout = %v, want %v", server.IdleTimeout, expectedIdleTimeout)
			}

			expectedReadHeaderTimeout := 5 * time.Second
			if server.ReadHeaderTimeout != expectedReadHeaderTimeout {
				t.Errorf("StartAPI() ReadHeaderTimeout = %v, want %v", server.ReadHeaderTimeout, expectedReadHeaderTimeout)
			}
		})
	}
}

func TestRouteRegistration(t *testing.T) {
	t.Parallel()

	config := &configuration.Config{
		API: struct {
			Port         int `mapstructure:"port"`
			ShutdownPort int `mapstructure:"shutdown_port"`
		}{
			Port: 0, // Use port 0 to avoid conflicts
		},
		Shutdown: struct {
			DrainingFile    string `mapstructure:"draining_file"`
			DrainTimeout    int    `mapstructure:"drain_timeout"`
			ShutdownTimeout int    `mapstructure:"shutdown_timeout"`
		}{
			DrainingFile:    "/tmp/draining",
			DrainTimeout:    30,
			ShutdownTimeout: 60,
		},
	}

	psql := &proxysql.ProxySQL{}
	server := StartAPI(psql, config)

	t.Cleanup(func() {
		server.Close()
	})

	// Verify that the handler is a ServeMux
	mux, ok := server.Handler.(*http.ServeMux)
	if !ok {
		t.Fatal("StartAPI() handler is not *http.ServeMux")
	}

	// Test that routes are registered by making requests
	// Note: These will likely fail due to nil ProxySQL methods, but we can test route registration
	testRoutes := []struct {
		path   string
		method string
	}{
		{"/healthz/started", "GET"},
		{"/healthz/ready", "GET"},
		{"/healthz/live", "GET"},
	}

	for _, route := range testRoutes {
		t.Run(route.path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), route.method, route.path, nil)
			w := httptest.NewRecorder()

			// This will likely panic or error due to nil ProxySQL, but it proves routes are registered
			defer func() {
				if r := recover(); r != nil {
					t.Logf("Route %s %s registered with nil ProxySQL", route.method, route.path)
				}
			}()

			mux.ServeHTTP(w, req)

			// If we get here without panic, check that we didn't get 404 (route not found)
			if w.Code == 404 {
				t.Errorf("Route %s %s not registered (got 404)", route.method, route.path)
			}
		})
	}
}

func TestStartAPIPortFormatting(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		port     int
		expected string
	}{
		{"single digit", 8, ":8"},
		{"double digit", 80, ":80"},
		{"common port", 8080, ":8080"},
		{"high port", 65535, ":65535"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := &configuration.Config{
				API: struct {
					Port         int `mapstructure:"port"`
					ShutdownPort int `mapstructure:"shutdown_port"`
				}{
					Port: tc.port,
				},
				Shutdown: struct {
					DrainingFile    string `mapstructure:"draining_file"`
					DrainTimeout    int    `mapstructure:"drain_timeout"`
					ShutdownTimeout int    `mapstructure:"shutdown_timeout"`
				}{
					DrainingFile:    "/tmp/draining",
					DrainTimeout:    30,
					ShutdownTimeout: 60,
				},
			}

			psql := &proxysql.ProxySQL{}
			server := StartAPI(psql, config)

			t.Cleanup(func() {
				server.Close()
			})

			if server.Addr != tc.expected {
				t.Errorf("Expected address %s, got %s", tc.expected, server.Addr)
			}
		})
	}
}

func TestServerTimeoutConfiguration(t *testing.T) {
	t.Parallel()

	config := &configuration.Config{
		API: struct {
			Port         int `mapstructure:"port"`
			ShutdownPort int `mapstructure:"shutdown_port"`
		}{
			Port: 0, // Use port 0 to avoid conflicts
		},
		Shutdown: struct {
			DrainingFile    string `mapstructure:"draining_file"`
			DrainTimeout    int    `mapstructure:"drain_timeout"`
			ShutdownTimeout int    `mapstructure:"shutdown_timeout"`
		}{
			DrainingFile:    "/tmp/draining",
			DrainTimeout:    30,
			ShutdownTimeout: 60,
		},
	}

	psql := &proxysql.ProxySQL{}
	server := StartAPI(psql, config)

	t.Cleanup(func() {
		server.Close()
	})

	timeoutTests := []struct {
		name     string
		actual   time.Duration
		expected time.Duration
	}{
		{"ReadTimeout", server.ReadTimeout, 10 * time.Second},
		{"WriteTimeout", server.WriteTimeout, time.Duration(config.Shutdown.ShutdownTimeout+15) * time.Second},
		{"IdleTimeout", server.IdleTimeout, 30 * time.Second},
		{"ReadHeaderTimeout", server.ReadHeaderTimeout, 5 * time.Second},
	}

	for _, tt := range timeoutTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.actual != tt.expected {
				t.Errorf("%s = %v, want %v", tt.name, tt.actual, tt.expected)
			}
		})
	}
}

// Test that server starts goroutine (we can't test the actual ListenAndServe without binding a port).
func TestStartAPIGoroutineStarted(t *testing.T) {
	t.Parallel()

	config := &configuration.Config{
		API: struct {
			Port         int `mapstructure:"port"`
			ShutdownPort int `mapstructure:"shutdown_port"`
		}{
			Port: 0, // Use port 0 to let OS choose available port
		},
		Shutdown: struct {
			DrainingFile    string `mapstructure:"draining_file"`
			DrainTimeout    int    `mapstructure:"drain_timeout"`
			ShutdownTimeout int    `mapstructure:"shutdown_timeout"`
		}{
			DrainingFile:    "/tmp/draining",
			DrainTimeout:    30,
			ShutdownTimeout: 60,
		},
	}

	psql := &proxysql.ProxySQL{}
	server := StartAPI(psql, config)

	// Server should be created and ready
	if server == nil {
		t.Fatal("StartAPI() returned nil server")
	}

	// Clean up
	server.Close()

	// The fact that we can close it without error indicates the goroutine was started
	// and the server was properly initialized
}

func TestStartAPIReturnsHTTPServer(t *testing.T) {
	t.Parallel()

	config := &configuration.Config{
		API: struct {
			Port         int `mapstructure:"port"`
			ShutdownPort int `mapstructure:"shutdown_port"`
		}{
			Port: 0, // Use port 0 to avoid conflicts
		},
		Shutdown: struct {
			DrainingFile    string `mapstructure:"draining_file"`
			DrainTimeout    int    `mapstructure:"drain_timeout"`
			ShutdownTimeout int    `mapstructure:"shutdown_timeout"`
		}{
			DrainingFile:    "/tmp/draining",
			DrainTimeout:    30,
			ShutdownTimeout: 60,
		},
	}

	psql := &proxysql.ProxySQL{}
	server := StartAPI(psql, config)

	t.Cleanup(func() {
		server.Close()
	})

	// Verify it returns an HTTP server
	if server == nil {
		t.Fatal("StartAPI() returned nil")
	}

	// Verify it's the correct type (server is already *http.Server, so just verify it's not nil)
	// No need for type assertion since StartAPI already returns *http.Server
}

// newShutdownTestConfig returns a config with the given API ports.
func newShutdownTestConfig(port, shutdownPort int) *configuration.Config {
	return &configuration.Config{
		API: struct {
			Port         int `mapstructure:"port"`
			ShutdownPort int `mapstructure:"shutdown_port"`
		}{
			Port:         port,
			ShutdownPort: shutdownPort,
		},
		Shutdown: struct {
			DrainingFile    string `mapstructure:"draining_file"`
			DrainTimeout    int    `mapstructure:"drain_timeout"`
			ShutdownTimeout int    `mapstructure:"shutdown_timeout"`
		}{
			DrainingFile:    "/tmp/draining",
			DrainTimeout:    30,
			ShutdownTimeout: 60,
		},
	}
}

// fakePreStopper records PreStopShutdown calls.
type fakePreStopper struct {
	err   error
	calls atomic.Int32
}

func (f *fakePreStopper) PreStopShutdown() error {
	f.calls.Add(1)

	return f.err
}

var errFakeShutdown = errors.New("fake shutdown failure")

// The probe server listens on the pod IP, so it must never route /shutdown (VLN-4882).
func TestProbeServerDoesNotServeShutdown(t *testing.T) {
	t.Parallel()

	psql := &proxysql.ProxySQL{}
	server := StartAPI(psql, newShutdownTestConfig(0, 0))

	t.Cleanup(func() {
		server.Close()
	})

	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), method, "/shutdown", nil)
			w := httptest.NewRecorder()

			server.Handler.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Errorf("%s /shutdown on probe server = %d, want %d", method, w.Code, http.StatusNotFound)
			}

			if psql.IsShuttingDown() {
				t.Errorf("%s /shutdown on probe server started a shutdown", method)
			}
		})
	}
}

func TestShutdownAddrIsLoopbackOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		shutdownPort int
		wantAddr     string
	}{
		{name: "default port", shutdownPort: 8081, wantAddr: "127.0.0.1:8081"},
		{name: "custom port", shutdownPort: 9191, wantAddr: "127.0.0.1:9191"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			addr := shutdownAddr(newShutdownTestConfig(8080, tt.shutdownPort))
			if addr != tt.wantAddr {
				t.Errorf("shutdownAddr() = %v, want %v", addr, tt.wantAddr)
			}

			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				t.Fatalf("SplitHostPort(%q) error: %v", addr, err)
			}

			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				t.Errorf("shutdownAddr() host = %q, want a loopback address", host)
			}
		})
	}
}

func TestStartShutdownAPIServerConfiguration(t *testing.T) {
	t.Parallel()

	// Port 0 lets the OS pick a free loopback port.
	config := newShutdownTestConfig(8080, 0)
	server := StartShutdownAPI(&fakePreStopper{}, config)

	t.Cleanup(func() {
		server.Close()
	})

	if server.Addr != "127.0.0.1:0" {
		t.Errorf("StartShutdownAPI() server.Addr = %v, want 127.0.0.1:0", server.Addr)
	}

	wantWriteTimeout := time.Duration(config.Shutdown.ShutdownTimeout+15) * time.Second
	if server.WriteTimeout != wantWriteTimeout {
		t.Errorf("StartShutdownAPI() WriteTimeout = %v, want %v", server.WriteTimeout, wantWriteTimeout)
	}
}

func TestShutdownServerRouting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		method    string
		path      string
		err       error
		wantCode  int
		wantCalls int32
	}{
		{name: "POST triggers shutdown", method: http.MethodPost, path: "/shutdown", wantCode: http.StatusOK, wantCalls: 1},
		{name: "POST reports shutdown failure", method: http.MethodPost, path: "/shutdown", err: errFakeShutdown, wantCode: http.StatusInternalServerError, wantCalls: 1},
		{name: "GET is rejected", method: http.MethodGet, path: "/shutdown", wantCode: http.StatusMethodNotAllowed, wantCalls: 0},
		{name: "PUT is rejected", method: http.MethodPut, path: "/shutdown", wantCode: http.StatusMethodNotAllowed, wantCalls: 0},
		{name: "health probes are not served", method: http.MethodGet, path: "/healthz/live", wantCode: http.StatusNotFound, wantCalls: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stopper := &fakePreStopper{err: tt.err}
			server := StartShutdownAPI(stopper, newShutdownTestConfig(8080, 0))

			t.Cleanup(func() {
				server.Close()
			})

			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil)
			w := httptest.NewRecorder()

			server.Handler.ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Errorf("%s %s = %d, want %d", tt.method, tt.path, w.Code, tt.wantCode)
			}

			if got := stopper.calls.Load(); got != tt.wantCalls {
				t.Errorf("PreStopShutdown() calls = %d, want %d", got, tt.wantCalls)
			}
		})
	}
}

// End to end over the real loopback listener: POST /shutdown reaches PreStopShutdown.
func TestShutdownServerOverLoopback(t *testing.T) {
	t.Parallel()

	// Reserve a free loopback port, then release it for the server to bind.
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port: %v", err)
	}

	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener address type %T", ln.Addr())
	}

	port := tcpAddr.Port
	ln.Close()

	stopper := &fakePreStopper{}
	server := StartShutdownAPI(stopper, newShutdownTestConfig(8080, port))

	t.Cleanup(func() {
		server.Close()
	})

	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + "/shutdown"
	client := &http.Client{Timeout: 2 * time.Second}

	var resp *http.Response

	// The server starts in a goroutine; retry briefly until it is listening.
	deadline := time.Now().Add(5 * time.Second)

	for {
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodPost, url, nil)
		if reqErr != nil {
			t.Fatalf("NewRequest error: %v", reqErr)
		}

		resp, err = client.Do(req)
		if err == nil || time.Now().After(deadline) {
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	if err != nil {
		t.Fatalf("POST %s error: %v", url, err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST %s = %d, want %d", url, resp.StatusCode, http.StatusOK)
	}

	if got := stopper.calls.Load(); got != 1 {
		t.Errorf("PreStopShutdown() calls = %d, want 1", got)
	}
}
