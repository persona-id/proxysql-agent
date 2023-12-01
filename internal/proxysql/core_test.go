package proxysql

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
