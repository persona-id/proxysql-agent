package proxysql

import (
	"errors"
	"testing"

	"github.com/kuzmik/proxysql-agent/internal/configuration"

	"github.com/stretchr/testify/assert"
	"gopkg.in/DATA-DOG/go-sqlmock.v2"
)

//nolint:gochecknoglobals
var tmpConfig = &configuration.Config{
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
