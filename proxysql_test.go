package main

import (
	"errors"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/DATA-DOG/go-sqlmock.v2"
)

var tmpConfig = &config{
	StartDelay: 0,
	LogLevel:   "",
	ProxySQL: struct {
		Address  string `mapstructure:"address"`
		Username string `mapstructure:"username"`
		Password string `mapstructure:"password"`
	}{},
	RunMode: "",
	Core: struct {
		Interval    int `mapstructure:"interval"`
		PodSelector struct {
			App       string `mapstructure:"app"`
			Component string `mapstructure:"component"`
		} "mapstructure:\"podselector\""
	}{},
	Satellite: struct {
		Interval int `mapstructure:"interval"`
	}{},
	Interfaces: []string{},
}

func TestPing(t *testing.T) {
	db, mock, err := sqlmock.New()
	assert.NoError(t, err, "Error creating mock database")
	defer db.Close()

	// FIXME: this doesn't exist now apparently, idk.
	// mock.ExpectPing()

	proxy := &ProxySQL{db, tmpConfig}
	err = proxy.Ping()

	assert.NoError(t, err, "Ping() should not return an error")
	assert.NotNil(t, proxy.conn, "Conn should not return nil")
	assert.NoError(t, mock.ExpectationsWereMet(), "SQL expectations were not met")
}

func TestGetMissingCorePods(t *testing.T) {
	db, mock, err := sqlmock.New()
	assert.NoError(t, err, "Error creating mock database")
	defer db.Close()

	query := regexp.QuoteMeta("SELECT COUNT(hostname) FROM stats_proxysql_servers_metrics WHERE last_check_ms > 30000 AND hostname != 'proxysql-core' AND Uptime_s > 0")

	proxy := &ProxySQL{db, tmpConfig}

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

func TestGetBackends(t *testing.T) {
	db, mock, err := sqlmock.New()
	assert.NoError(t, err, "Error creating mock database")
	defer db.Close()

	proxy := &ProxySQL{db, tmpConfig}

	t.Run("no error", func(t *testing.T) {
		expectedRows := sqlmock.NewRows([]string{"hostgroup_id", "hostname", "port"}).
			AddRow(1, "host1", 3306).
			AddRow(2, "host2", 3306).
			AddRow(1, "host3", 3307)

		mock.ExpectQuery("SELECT hostgroup_id, hostname, port FROM runtime_mysql_servers ORDER BY hostgroup_id").
			WillReturnRows(expectedRows)

		entries, err := proxy.GetBackends()
		assert.NoError(t, err, "GetBackends should not return an error")

		expectedEntries := map[string]int{"host1": 1, "host2": 2, "host3": 1}

		assert.Equal(t, expectedEntries, entries, "Entries should match the expected values")
		assert.NoError(t, mock.ExpectationsWereMet(), "SQL expectations were not met")
	})

	t.Run("returns error", func(t *testing.T) {
		expectedError := errors.New("database error")
		mock.ExpectQuery("SELECT hostgroup_id, hostname, port FROM runtime_mysql_servers ORDER BY hostgroup_id").
			WillReturnError(expectedError)

		_, err = proxy.GetBackends()

		assert.EqualError(t, err, expectedError.Error(), "GetBackends should return the expected error")
		assert.NoError(t, mock.ExpectationsWereMet(), "SQL expectations were not met")
	})
}

func TestCreateCommands(t *testing.T) {
	t.Run("one pod", func(t *testing.T) {
		singlePod := []PodInfo{{PodIP: "192.168.0.1", Hostname: "host1", UID: "testuid1"}}
		singleCommands := createCommands(singlePod)

		expectedSingleCommands := []string{
			"DELETE FROM proxysql_servers",
			"INSERT INTO proxysql_servers VALUES ('192.168.0.1', 6032, 0, 'host1')",
			"LOAD PROXYSQL SERVERS TO RUNTIME",
			"LOAD ADMIN VARIABLES TO RUNTIME",
			"LOAD MYSQL VARIABLES TO RUNTIME",
			"LOAD MYSQL SERVERS TO RUNTIME",
			"LOAD MYSQL USERS TO RUNTIME",
			"LOAD MYSQL QUERY RULES TO RUNTIME",
		}
		assert.Equal(t, expectedSingleCommands, singleCommands, "Single pod should generate expected commands")
	})

	t.Run("several pods", func(t *testing.T) {
		// making these out of order, to test the sort functions
		multiplePods := []PodInfo{
			{PodIP: "192.168.0.2", Hostname: "host2", UID: "testuid2"},
			{PodIP: "192.168.0.1", Hostname: "host1", UID: "testuid1"},
		}
		multipleCommands := createCommands(multiplePods)

		expectedMultipleCommands := []string{
			"DELETE FROM proxysql_servers",
			"INSERT INTO proxysql_servers VALUES ('192.168.0.1', 6032, 0, 'host1')",
			"INSERT INTO proxysql_servers VALUES ('192.168.0.2', 6032, 0, 'host2')",
			"LOAD PROXYSQL SERVERS TO RUNTIME",
			"LOAD ADMIN VARIABLES TO RUNTIME",
			"LOAD MYSQL VARIABLES TO RUNTIME",
			"LOAD MYSQL SERVERS TO RUNTIME",
			"LOAD MYSQL USERS TO RUNTIME",
			"LOAD MYSQL QUERY RULES TO RUNTIME",
		}
		assert.Equal(t, expectedMultipleCommands, multipleCommands, "Multiple pods should generate expected commands")
	})
}
