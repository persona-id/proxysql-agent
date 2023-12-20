package proxysql

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"gopkg.in/DATA-DOG/go-sqlmock.v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCore(t *testing.T) {
	t.Run("TODO", func(t *testing.T) {
		fmt.Println("TODO")
	})
}

func TestPodUpdated(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create mock database connection: %v", err)
	}
	defer db.Close()

	mock.MatchExpectationsInOrder(true)

	p := &ProxySQL{db, tmpConfig, nil}

	oldpod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "old-pod",
			Namespace: "test-ns",
			Labels: map[string]string{
				"component": "core",
			},
		},
		Status: v1.PodStatus{
			PodIP: "old-pod-ip",
		},
	}

	newpod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "new-pod",
			Namespace: "test-ns",
			Labels: map[string]string{
				"component": "core",
			},
		},
		Status: v1.PodStatus{
			PodIP: "new-pod-ip",
		},
	}

	t.Run("pod started", func(t *testing.T) {
		oldpod.Status.Phase = "Pending"
		newpod.Status.Phase = "Running"

		mock.ExpectExec("DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'").WillReturnResult(sqlmock.NewResult(0, 1))

		mock.ExpectExec(
			regexp.QuoteMeta("INSERT INTO proxysql_servers VALUES (?, 6032, 0, ?)"),
		).WithArgs(
			"new-pod-ip", "new-pod",
		).WillReturnResult(
			sqlmock.NewResult(0, 1),
		)

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

		p.podUpdated(oldpod, newpod)
	})

	t.Run("pod stopped", func(t *testing.T) {
		oldpod.Status.Phase = "Running"
		newpod.Status.Phase = "Failed"

		mock.ExpectExec(
			"DELETE FROM proxysql_servers WHERE hostname = ?",
		).WithArgs(
			"old-pod-ip",
		).WillReturnResult(
			sqlmock.NewResult(0, 1),
		)

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

		p.podUpdated(oldpod, newpod)
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("Unfulfilled expectations: %s", err)
	}

	assert.NoError(t, err)
}

func TestPodAdded(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create mock database connection: %v", err)
	}
	defer db.Close()

	mock.MatchExpectationsInOrder(true)

	p := &ProxySQL{db, tmpConfig, nil}

	// we have to do a little hostname trickery for this test, as podAdded will immediately return for any pods
	// that aren't processing themselves.
	hostname, _ := os.Hostname()

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hostname,
			Namespace: "test-ns",
			Labels: map[string]string{
				"component": "core",
			},
		},
		Status: v1.PodStatus{
			PodIP: "pod-ip",
		},
	}

	t.Run("core pod already exists in cluster", func(t *testing.T) {
		// Expect the query and return the row set
		mock.ExpectQuery(
			regexp.QuoteMeta("SELECT count(*) FROM proxysql_servers WHERE hostname = ?"),
		).WithArgs(
			"pod-ip",
		).WillReturnRows(
			sqlmock.NewRows([]string{"count"}).AddRow(1),
		)

		p.podAdded(pod)
	})

	t.Run("core pod does not exist in cluster", func(t *testing.T) {
		// Expect the query and return the row set
		mock.ExpectQuery(
			regexp.QuoteMeta("SELECT count(*) FROM proxysql_servers WHERE hostname = ?"),
		).WithArgs(
			"pod-ip",
		).WillReturnRows(
			sqlmock.NewRows([]string{"count"}).AddRow(0),
		)

		mock.ExpectExec("DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'").WillReturnResult(sqlmock.NewResult(0, 1))

		hostname, _ := os.Hostname()
		mock.ExpectExec(
			regexp.QuoteMeta("INSERT INTO proxysql_servers VALUES (?, 6032, 0, ?)"),
		).WithArgs(
			"pod-ip", hostname,
		).WillReturnResult(
			sqlmock.NewResult(0, 1),
		)

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

		p.podAdded(pod)
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("Unfulfilled expectations: %s", err)
	}

	assert.NoError(t, err)
}

func TestRemovePodFromCluster(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create mock database connection: %v", err)
	}
	defer db.Close()

	mock.MatchExpectationsInOrder(true)

	p := &ProxySQL{db, tmpConfig, nil}

	t.Run("core pod", func(t *testing.T) {
		mock.ExpectExec(
			"DELETE FROM proxysql_servers WHERE hostname = ?",
		).WithArgs(
			"pod-ip",
		).WillReturnResult(
			sqlmock.NewResult(0, 1),
		)

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

		err = p.removePodFromCluster(pod)

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

		err = p.removePodFromCluster(pod)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}

		assert.NoError(t, err)
	})
}
