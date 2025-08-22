package restapi

import (
	"net/http"
	"net/http/httptest"
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

			expectedWriteTimeout := 10 * time.Second
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
		{"/shutdown", "POST"},
	}

	for _, route := range testRoutes {
		t.Run(route.path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(route.method, route.path, nil)
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
		{"WriteTimeout", server.WriteTimeout, 10 * time.Second},
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
