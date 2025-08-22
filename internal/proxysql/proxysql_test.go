package proxysql

import (
	"context"
	"sync"
	"testing"

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

// Return a config for testing purposes.
// This method is used in all the test files in this directory.
func newTestConfig() *configuration.Config {
	return &configuration.Config{
		StartDelay: 0,
		Log: struct {
			Level  string `mapstructure:"level"`
			Format string `mapstructure:"format"`
		}{
			Level:  "INFO",
			Format: "text",
		},
		ProxySQL: struct {
			Address  string `mapstructure:"address"`
			Username string `mapstructure:"username"`
			Password string `mapstructure:"password"`
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
