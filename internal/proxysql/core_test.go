package proxysql

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/DATA-DOG/go-sqlmock.v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// I haven't figured out how to mock k8s stuff out yet, so this function is hard to test.
func TestCore(t *testing.T) {
	t.Run("TODO", func(t *testing.T) {
		fmt.Println("TODO")
	})
}

// I haven't figured out how to mock k8s stuff out yet, so this function is hard to test.
func TestPodUpdated(t *testing.T) {
	t.Run("TODO", func(t *testing.T) {
		fmt.Println("TODO")
	})
}

func TestAddPod(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create mock database connection: %v", err)
	}
	defer db.Close()

	mock.MatchExpectationsInOrder(true)

	p := &ProxySQL{db, tmpConfig, nil}

	t.Run("single core pod", func(t *testing.T) {
		for _, cmd := range []string{
			"DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'",
			regexp.QuoteMeta("INSERT INTO proxysql_servers VALUES ('pod-ip', 6032, 0, 'test-pod')"),
			"LOAD PROXYSQL SERVERS TO RUNTIME",
			"LOAD ADMIN VARIABLES TO RUNTIME",
			"LOAD MYSQL VARIABLES TO RUNTIME",
			"LOAD MYSQL SERVERS TO RUNTIME",
			"LOAD MYSQL USERS TO RUNTIME",
			"LOAD MYSQL QUERY RULES TO RUNTIME",
		} {
			mock.ExpectExec(cmd).WillReturnResult(sqlmock.NewResult(0, 1))
		}

		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "test-ns",
				Labels: map[string]string{
					"component": "core",
				},
			},
			Status: v1.PodStatus{
				PodIP: "pod-ip",
			},
		}

		err = p.addPod(pod)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}

		assert.NoError(t, err)
	})

	t.Run("multiple core pods", func(t *testing.T) {
		// add first core pod
		for _, cmd := range []string{
			"DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'",
			regexp.QuoteMeta("INSERT INTO proxysql_servers VALUES ('pod-ip-1', 6032, 0, 'test-pod-1')"),
			"LOAD PROXYSQL SERVERS TO RUNTIME",
			"LOAD ADMIN VARIABLES TO RUNTIME",
			"LOAD MYSQL VARIABLES TO RUNTIME",
			"LOAD MYSQL SERVERS TO RUNTIME",
			"LOAD MYSQL USERS TO RUNTIME",
			"LOAD MYSQL QUERY RULES TO RUNTIME",
		} {
			mock.ExpectExec(cmd).WillReturnResult(sqlmock.NewResult(0, 1))
		}

		pod1 := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-1",
				Namespace: "test-ns",
				Labels: map[string]string{
					"component": "core",
				},
			},
			Status: v1.PodStatus{
				PodIP: "pod-ip-1",
			},
		}

		err = p.addPod(pod1)

		// add second core pod
		for _, cmd := range []string{
			"DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'",
			regexp.QuoteMeta("INSERT INTO proxysql_servers VALUES ('pod-ip-2', 6032, 0, 'test-pod-2')"),
			"LOAD PROXYSQL SERVERS TO RUNTIME",
			"LOAD ADMIN VARIABLES TO RUNTIME",
			"LOAD MYSQL VARIABLES TO RUNTIME",
			"LOAD MYSQL SERVERS TO RUNTIME",
			"LOAD MYSQL USERS TO RUNTIME",
			"LOAD MYSQL QUERY RULES TO RUNTIME",
		} {
			mock.ExpectExec(cmd).WillReturnResult(sqlmock.NewResult(0, 1))
		}

		pod2 := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-2",
				Namespace: "test-ns",
				Labels: map[string]string{
					"component": "core",
				},
			},
			Status: v1.PodStatus{
				PodIP: "pod-ip-2",
			},
		}

		err = p.addPod(pod2)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}

		assert.NoError(t, err)
	})
}

func TestRemovePod(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create mock database connection: %v", err)
	}
	defer db.Close()

	mock.MatchExpectationsInOrder(true)

	p := &ProxySQL{db, tmpConfig, nil}

	t.Run("core pod", func(t *testing.T) {
		mock.ExpectExec(fmt.Sprintf("DELETE FROM proxysql_servers WHERE hostname = '%s'", "pod-ip")).WillReturnResult(sqlmock.NewResult(0, 1))

		for _, cmd := range []string{
			"LOAD PROXYSQL SERVERS TO RUNTIME",
			"LOAD ADMIN VARIABLES TO RUNTIME",
			"LOAD MYSQL VARIABLES TO RUNTIME",
			"LOAD MYSQL SERVERS TO RUNTIME",
			"LOAD MYSQL USERS TO RUNTIME",
			"LOAD MYSQL QUERY RULES TO RUNTIME",
		} {
			mock.ExpectExec(cmd).WillReturnResult(sqlmock.NewResult(0, 1))
		}

		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "test-ns",
				Labels: map[string]string{
					"component": "core",
				},
			},
			Status: v1.PodStatus{
				PodIP: "pod-ip",
			},
		}

		err = p.removePod(pod)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}

		assert.NoError(t, err)
	})

	t.Run("satellite pod", func(t *testing.T) {
		for _, cmd := range []string{
			"LOAD PROXYSQL SERVERS TO RUNTIME",
			"LOAD ADMIN VARIABLES TO RUNTIME",
			"LOAD MYSQL VARIABLES TO RUNTIME",
			"LOAD MYSQL SERVERS TO RUNTIME",
			"LOAD MYSQL USERS TO RUNTIME",
			"LOAD MYSQL QUERY RULES TO RUNTIME",
		} {
			mock.ExpectExec(cmd).WillReturnResult(sqlmock.NewResult(0, 1))
		}

		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
				Labels: map[string]string{
					"component": "satellite",
				},
			},
		}

		err = p.removePod(pod)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}

		assert.NoError(t, err)
	})
}
