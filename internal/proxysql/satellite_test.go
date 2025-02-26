package proxysql

import (
	"bufio"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/DATA-DOG/go-sqlmock.v2"
)

func TestGetMissingCorePods(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(mock sqlmock.Sqlmock)
		expectedCount int
		expectedErr   error
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
				mock.ExpectQuery(query).WillReturnError(errors.New("database error"))
			},
			expectedCount: -1,
			expectedErr:   errors.New("database error"),
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
			case tt.expectedErr != nil && err != nil && tt.expectedErr.Error() != err.Error():
				t.Errorf("GetMissingCorePods() expected error: %v, got: %v", tt.expectedErr, err)
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

	p := &ProxySQL{conn: db}

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

func TestDumpQueryRuleStats(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(mock sqlmock.Sqlmock)
		expectFile    bool
		expectedLines []string
	}{
		{
			name: "no stats",
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"count"}).AddRow(0)
				mock.ExpectQuery(
					regexp.QuoteMeta("SELECT COUNT(*) FROM stats_mysql_query_rules"),
				).WillReturnRows(rows)
			},
			expectFile:    false,
			expectedLines: nil,
		},
		{
			name: "has stats",
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"count"}).AddRow(1)
				mock.ExpectQuery(
					regexp.QuoteMeta("SELECT COUNT(*) FROM stats_mysql_query_rules"),
				).WillReturnRows(rows)

				rows = sqlmock.NewRows([]string{"rule_id", "hits"}).AddRow(1, 100).AddRow(2, 200)
				mock.ExpectQuery(
					regexp.QuoteMeta("SELECT * FROM stats_mysql_query_rules"),
				).WillReturnRows(rows)
			},
			expectFile: true,
			expectedLines: []string{
				"rule_id,hits",
				"1,100",
				"2,200",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
			}
			defer db.Close()

			tmpdir := os.TempDir()
			p := &ProxySQL{conn: db}

			// Setup mock
			tt.setupMock(mock)

			// Call function being tested
			filePath, err := p.DumpQueryRuleStats(tmpdir)
			if err != nil {
				t.Errorf("Expected no error, but got %s instead", err)
			}

			// Verify SQL expectations
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("there were unfulfilled expectations: %s", err)
			}

			// If file expected, verify contents
			if tt.expectFile {
				if filePath == "" {
					t.Errorf("Expected a file path, but got empty string")
				}

				// verify the file content
				file, err := os.Open(filePath)
				if err != nil {
					t.Errorf("Expected file to be created, but got %s", err)
				}
				defer file.Close()

				scanner := bufio.NewScanner(file)
				lineIndex := 0

				for scanner.Scan() {
					if lineIndex >= len(tt.expectedLines) {
						t.Errorf("More lines in file than expected, got extra line: %s", scanner.Text())
						break
					}

					if strings.TrimSpace(scanner.Text()) != tt.expectedLines[lineIndex] {
						t.Errorf("Expected line %d to be '%s', got '%s'", lineIndex, tt.expectedLines[lineIndex], scanner.Text())
					}

					lineIndex++
				}

				if lineIndex < len(tt.expectedLines) {
					t.Errorf("Expected %d lines in file, but got %d", len(tt.expectedLines), lineIndex)
				}
			}
		})
	}
}
