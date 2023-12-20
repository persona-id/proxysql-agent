package proxysql

import (
	"bufio"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/DATA-DOG/go-sqlmock.v2"
)

func TestGetMissingCorePods(t *testing.T) {
	db, mock, err := sqlmock.New()
	assert.NoError(t, err, "Error creating mock database")

	defer db.Close()

	query := regexp.QuoteMeta("SELECT COUNT(hostname) FROM stats_proxysql_servers_metrics WHERE last_check_ms > 30000 AND hostname != 'proxysql-core' AND Uptime_s > 0")

	proxy := &ProxySQL{db, tmpConfig, nil}

	t.Run("no error", func(t *testing.T) {
		expectedCount := 1
		mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(expectedCount))

		count, err := proxy.GetMissingCorePods()
		assert.NoError(t, err, "GetMissingCorePods should not return an error")

		assert.Equal(t, expectedCount, count, "Count should match the expected value")
		assert.NoError(t, mock.ExpectationsWereMet(), "SQL expectations were not met")
	})

	t.Run("returns error", func(t *testing.T) {
		expectedError := errors.New("database error")
		mock.ExpectQuery(query).WillReturnError(expectedError)

		count, err := proxy.GetMissingCorePods()

		assert.Equal(t, -1, count)
		assert.EqualError(t, err, expectedError.Error(), "GetBackends should return the expected error")
		assert.NoError(t, mock.ExpectationsWereMet(), "SQL expectations were not met")
	})
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
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	tmpdir := os.TempDir()

	p := &ProxySQL{conn: db}

	// No stats in table, nothing is done.
	t.Run("no stats", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"count"}).AddRow(0)
		mock.ExpectQuery(
			regexp.QuoteMeta("SELECT COUNT(*) FROM stats_mysql_query_rules"),
		).WillReturnRows(rows)

		_, err := p.DumpQueryRuleStats(tmpdir)
		if err != nil {
			t.Errorf("Expected no error, but got %s instead", err)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	// Has stats in table, so the file should have data
	t.Run("has stats", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"count"}).AddRow(1)
		mock.ExpectQuery(
			regexp.QuoteMeta("SELECT COUNT(*) FROM stats_mysql_query_rules"),
		).WillReturnRows(rows)

		rows = sqlmock.NewRows([]string{"rule_id", "hits"}).AddRow(1, 100).AddRow(2, 200)
		mock.ExpectQuery(
			regexp.QuoteMeta("SELECT * FROM stats_mysql_query_rules"),
		).WillReturnRows(rows)

		filePath, err := p.DumpQueryRuleStats(tmpdir)
		if err != nil {
			t.Errorf("Expected no error, but got %s instead", err)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}

		// verify the file content
		file, err := os.Open(filePath)
		if err != nil {
			t.Errorf("Expected file to be created, but got %s", err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		header := "rule_id,hits"

		if scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) != header {
				t.Errorf("Expected file header to be '%s', got '%s'", header, scanner.Text())
			}
		}

		if scanner.Scan() {
			line := "1,100"
			if strings.TrimSpace(scanner.Text()) != line {
				t.Errorf("Expected first line to be '%s', got '%s'", line, scanner.Text())
			}
		}

		if scanner.Scan() {
			line := "2,200"
			if strings.TrimSpace(scanner.Text()) != line {
				t.Errorf("Expected last line to be '%s', got '%s'", line, scanner.Text())
			}
		}
	})
}
