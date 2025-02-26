package proxysql

import (
	"errors"
	"reflect"
	"testing"

	"github.com/persona-id/proxysql-agent/internal/configuration"

	"gopkg.in/DATA-DOG/go-sqlmock.v2"
)

// Return a config for testing purposes.
// This method is used in all the test files in this directory.
func newTestConfig() *configuration.Config {
	return &configuration.Config{
		StartDelay: 0,
		Log: struct {
			Level  string `mapstructure:"level"`
			Format string `mapstructure:"format"`
		}{},
		ProxySQL: struct {
			Address  string `mapstructure:"address"`
			Username string `mapstructure:"username"`
			Password string `mapstructure:"password"`
		}{},
		RunMode: "",
		Core: struct {
			Interval    int `mapstructure:"interval"`
			PodSelector struct {
				Namespace string `mapstructure:"namespace"`
				App       string `mapstructure:"app"`
				Component string `mapstructure:"component"`
			} "mapstructure:\"podselector\""
		}{},
		Satellite: struct {
			Interval int `mapstructure:"interval"`
		}{},
		Interfaces: []string{},
	}
}

func TestPing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Error creating mock database: %v", err)
	}
	defer db.Close()

	proxy := &ProxySQL{db, newTestConfig(), nil}

	if err = proxy.Ping(); err != nil {
		t.Errorf("Ping() returned an error: %v", err)
	}

	if proxy.conn == nil {
		t.Error("Conn should not be nil")
	}

	if err = mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL expectations were not met: %v", err)
	}
}

func TestGetBackends(t *testing.T) {
	tests := []struct {
		name           string
		setupMock      func(mock sqlmock.Sqlmock)
		expectedResult map[string]int
		expectedErr    error
	}{
		{
			name: "successful query",
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"hostgroup_id", "hostname", "port"}).
					AddRow(1, "host1", 3306).
					AddRow(2, "host2", 3306).
					AddRow(1, "host3", 3307)
				mock.ExpectQuery("SELECT hostgroup_id, hostname, port FROM runtime_mysql_servers ORDER BY hostgroup_id").
					WillReturnRows(rows)
			},
			expectedResult: map[string]int{"host1": 1, "host2": 2, "host3": 1},
			expectedErr:    nil,
		},
		{
			name: "database error",
			setupMock: func(mock sqlmock.Sqlmock) {
				expectedError := errors.New("database error")
				mock.ExpectQuery("SELECT hostgroup_id, hostname, port FROM runtime_mysql_servers ORDER BY hostgroup_id").
					WillReturnError(expectedError)
			},
			expectedResult: nil,
			expectedErr:    errors.New("database error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("Error creating mock database: %v", err)
			}
			defer db.Close()

			proxy := &ProxySQL{db, newTestConfig(), nil}

			tt.setupMock(mock)

			entries, err := proxy.GetBackends()

			switch {
			case tt.expectedErr == nil && err != nil:
				t.Errorf("GetBackends() returned unexpected error: %v", err)
			case tt.expectedErr != nil && err == nil:
				t.Errorf("GetBackends() expected error: %v, got nil", tt.expectedErr)
			case tt.expectedErr != nil && err != nil && tt.expectedErr.Error() != err.Error():
				t.Errorf("GetBackends() expected error: %v, got: %v", tt.expectedErr, err)
			}

			if !reflect.DeepEqual(entries, tt.expectedResult) {
				t.Errorf("GetBackends() expected result: %v, got: %v", tt.expectedResult, entries)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("SQL expectations were not met: %v", err)
			}
		})
	}
}
