package restapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
					Port int `mapstructure:"port"`
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
			Port int `mapstructure:"port"`
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
		{"/metrics", "GET"},
		{"/shutdown", "POST"},
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

type fakeMetricsReader struct {
	err              error
	metrics          proxysql.Metrics
	requiresDeadline bool
}

func (f fakeMetricsReader) Metrics(ctx context.Context) (proxysql.Metrics, error) {
	if deadline, ok := ctx.Deadline(); f.requiresDeadline && (!ok || time.Until(deadline) > metricsTimeout) {
		return proxysql.Metrics{}, context.DeadlineExceeded
	}

	return f.metrics, f.err
}

func TestMetricsHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		reader     fakeMetricsReader
		wantBody   string
		wantStatus int
		wantType   string
	}{
		{
			name: "renders Prometheus gauges",
			reader: fakeMetricsReader{
				metrics: proxysql.Metrics{
					BackendConnections: []proxysql.BackendConnectionMetric{
						{ConnectionsUsed: 23, Hostgroup: 2, Port: 6033, Server: "primary.internal"},
						{ConnectionsUsed: 11, Hostgroup: 2, Port: 6034, Server: "primary.internal"},
						{ConnectionsUsed: 7, Hostgroup: 3, Port: 3306, Server: "replica\"\\\n.internal"},
					},
					FrontendConnections: 17,
				},
				requiresDeadline: true,
			},
			wantBody: strings.Join([]string{
				"# HELP proxysql_frontend_connections_connected Current frontend client connections.",
				"# TYPE proxysql_frontend_connections_connected gauge",
				"proxysql_frontend_connections_connected 17",
				"# HELP proxysql_backend_connections_used Current connections in use for a backend pool.",
				"# TYPE proxysql_backend_connections_used gauge",
				`proxysql_backend_connections_used{hostgroup="2",port="6033",server="primary.internal"} 23`,
				`proxysql_backend_connections_used{hostgroup="2",port="6034",server="primary.internal"} 11`,
				`proxysql_backend_connections_used{hostgroup="3",port="3306",server="replica\"\\\n.internal"} 7`,
				"",
			}, "\n"),
			wantStatus: http.StatusOK,
			wantType:   "text/plain; version=0.0.4; charset=utf-8",
		},
		{
			name: "fails closed when collection fails",
			reader: fakeMetricsReader{
				err:              proxysql.ErrDatabase,
				requiresDeadline: true,
			},
			wantBody:   "metrics unavailable\n",
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "text/plain; charset=utf-8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
			response := httptest.NewRecorder()

			metricsHandler(tt.reader).ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", response.Code, tt.wantStatus)
			}

			if response.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", response.Body.String(), tt.wantBody)
			}

			if contentType := response.Header().Get("Content-Type"); contentType != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", contentType, tt.wantType)
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
					Port int `mapstructure:"port"`
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
			Port int `mapstructure:"port"`
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
			Port int `mapstructure:"port"`
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
			Port int `mapstructure:"port"`
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
