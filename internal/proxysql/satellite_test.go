package proxysql

import (
	"errors"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/DATA-DOG/go-sqlmock.v2"
)

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
