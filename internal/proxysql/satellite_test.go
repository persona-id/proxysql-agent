package proxysql

import (
	"errors"
	"regexp"
	"sync"
	"testing"

	"gopkg.in/DATA-DOG/go-sqlmock.v2"
)

func TestGetMissingCorePods(t *testing.T) {
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
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("Error creating mock database: %v", err)
			}
			defer db.Close()

			proxy := &ProxySQL{
			clientset:    nil,
			conn:         db,
			settings:     newTestConfig(),
			shutdownOnce: sync.Once{},
			shuttingDown: false,
			shutdownMu:   sync.RWMutex{},
			httpServer:   nil,
		}

			// Setup the mock
			tt.setupMock(mock)

			// Call the function being tested
			count, err := proxy.GetMissingCorePods()

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
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("SQL expectations were not met: %v", err)
			}
		})
	}
}

func TestSatelliteResync(t *testing.T) {
	// Mock database connection
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("An error '%s' was not expected when opening a mock database connection", err)
	}
	defer db.Close()

	mock.MatchExpectationsInOrder(true)

	p := &ProxySQL{
		clientset:    nil,
		conn:         db,
		settings:     newTestConfig(),
		shutdownOnce: sync.Once{},
		shuttingDown: false,
		shutdownMu:   sync.RWMutex{},
		httpServer:   nil,
	}

	query := regexp.QuoteMeta("SELECT COUNT(hostname) FROM stats_proxysql_servers_metrics WHERE last_check_ms > 30000 AND hostname != 'proxysql-core' AND Uptime_s > 0")
	mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	commands := []string{
		"DELETE FROM proxysql_servers",
		"LOAD PROXYSQL SERVERS FROM CONFIG",
		"LOAD PROXYSQL SERVERS TO RUNTIME;",
	}
	for _, command := range commands {
		mock.ExpectExec(command).WillReturnResult(sqlmock.NewResult(1, 1))
	}

	err = p.SatelliteResync()
	if err != nil {
		t.Errorf("Expected no error, but got %s", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("There were unfulfilled expectations: %s", err)
	}
}
