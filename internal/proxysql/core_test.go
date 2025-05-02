package proxysql

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"gopkg.in/DATA-DOG/go-sqlmock.v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Define a static error for tests.
var errSQLTest = errors.New("SQL error")

func TestCore(t *testing.T) {
	t.Run("TODO", func(t *testing.T) {
		t.Log("TODO test")
		t.Skip("TODO test")
	})
}

func TestPodUpdated(t *testing.T) {
	testCases := []struct {
		name        string
		setupMock   func(mock sqlmock.Sqlmock)
		oldPodPhase string
		newPodPhase string
	}{
		{
			name:        "pod started",
			oldPodPhase: "Pending",
			newPodPhase: "Running",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec("DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'").
					WillReturnResult(sqlmock.NewResult(0, 1))

				mock.ExpectExec(
					regexp.QuoteMeta(`INSERT INTO proxysql_servers VALUES ("new-pod-ip", 6032, 0, "new-pod")`),
				).WillReturnResult(
					sqlmock.NewResult(0, 1),
				)

				expectRuntimeLoads(mock)
			},
		},
		{
			name:        "pod stopped",
			oldPodPhase: "Running",
			newPodPhase: "Failed",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(
					`DELETE FROM proxysql_servers WHERE hostname = "old-pod-ip"`,
				).WillReturnResult(
					sqlmock.NewResult(0, 1),
				)

				expectRuntimeLoads(mock)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("Failed to create mock database connection: %v", err)
			}
			defer db.Close()

			mock.MatchExpectationsInOrder(true)

			p := &ProxySQL{nil, db, newTestConfig()}

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
					Phase: v1.PodPhase(tc.oldPodPhase),
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
					Phase: v1.PodPhase(tc.newPodPhase),
				},
			}

			tc.setupMock(mock)

			p.podUpdated(oldpod, newpod)

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("Unfulfilled expectations: %s", err)
			}
		})
	}
}

func TestPodAdded(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("Failed to get hostname: %v", err)
	}

	testCases := []struct {
		name      string
		setupMock func(mock sqlmock.Sqlmock)
		podExists bool
	}{
		{
			name:      "core pod already exists in cluster",
			podExists: true,
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(
					regexp.QuoteMeta(`SELECT count(*) FROM proxysql_servers WHERE hostname = ?`),
				).WithArgs("pod-ip").WillReturnRows(
					sqlmock.NewRows([]string{"count"}).AddRow(1),
				)
			},
		},
		{
			name:      "core pod does not exist in cluster",
			podExists: false,
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(
					regexp.QuoteMeta(`SELECT count(*) FROM proxysql_servers WHERE hostname = ?`),
				).WithArgs("pod-ip").WillReturnRows(
					sqlmock.NewRows([]string{"count"}).AddRow(0),
				)

				mock.ExpectExec("DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'").
					WillReturnResult(sqlmock.NewResult(0, 1))

				mock.ExpectExec(
					regexp.QuoteMeta(fmt.Sprintf(`INSERT INTO proxysql_servers VALUES ("pod-ip", 6032, 0, %q)`, hostname)),
				).WillReturnResult(
					sqlmock.NewResult(0, 1),
				)

				expectRuntimeLoads(mock)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("Failed to create mock database connection: %v", err)
			}
			defer db.Close()

			mock.MatchExpectationsInOrder(true)

			p := &ProxySQL{nil, db, newTestConfig()}

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

			tc.setupMock(mock)

			p.podAdded(pod)

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("Unfulfilled expectations: %s", err)
			}
		})
	}
}

func TestAddPodToCluster(t *testing.T) {
	testCases := []struct {
		name       string
		expectFunc func(t *testing.T, err error)
		setupMock  func(mock sqlmock.Sqlmock)
		component  string
		namespace  string
	}{
		{
			name:      "core pod",
			component: "core",
			namespace: "test-ns",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec("DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'").
					WillReturnResult(sqlmock.NewResult(0, 1))

				mock.ExpectExec(
					regexp.QuoteMeta(`INSERT INTO proxysql_servers VALUES ("pod-ip", 6032, 0, "test-pod")`),
				).WillReturnResult(
					sqlmock.NewResult(0, 1),
				)

				expectRuntimeLoads(mock)
			},
			expectFunc: func(t *testing.T, err error) {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			},
		},
		{
			name:      "satellite pod",
			component: "satellite",
			namespace: "default",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec("DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'").
					WillReturnResult(sqlmock.NewResult(0, 1))

				expectRuntimeLoads(mock)
			},
			expectFunc: func(t *testing.T, err error) {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			},
		},
		{
			name:      "error executing SQL",
			component: "core",
			namespace: "test-ns",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec("DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'").
					WillReturnError(errSQLTest)
			},
			expectFunc: func(t *testing.T, err error) {
				if err == nil {
					t.Errorf("expected error, got nil")
					return
				}

				// Check for the wrapped error message
				if !strings.Contains(err.Error(), "SQL error") || !strings.Contains(err.Error(), "failed to execute command") {
					t.Errorf("expected error to contain both 'SQL error' and 'failed to execute command', got %v", err)
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("Failed to create mock database connection: %v", err)
			}
			defer db.Close()

			mock.MatchExpectationsInOrder(true)

			p := &ProxySQL{nil, db, newTestConfig()}

			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: tc.namespace,
					Labels: map[string]string{
						"component": tc.component,
					},
				},
				Status: v1.PodStatus{
					PodIP: "pod-ip",
				},
			}

			tc.setupMock(mock)

			err = p.addPodToCluster(pod)
			tc.expectFunc(t, err)

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("Unfulfilled expectations: %s", err)
			}
		})
	}
}

func TestRemovePodFromCluster(t *testing.T) {
	testCases := []struct {
		name       string
		expectFunc func(t *testing.T, err error)
		setupMock  func(mock sqlmock.Sqlmock)
		component  string
		namespace  string
	}{
		{
			name:      "core pod",
			component: "core",
			namespace: "test-ns",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(
					`DELETE FROM proxysql_servers WHERE hostname = "pod-ip"`,
				).WillReturnResult(
					sqlmock.NewResult(0, 1),
				)

				expectRuntimeLoads(mock)
			},
			expectFunc: func(t *testing.T, err error) {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			},
		},
		{
			name:      "satellite pod",
			component: "satellite",
			namespace: "default",
			setupMock: func(mock sqlmock.Sqlmock) {
				expectRuntimeLoads(mock)
			},
			expectFunc: func(t *testing.T, err error) {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			},
		},
		{
			name:      "error executing SQL",
			component: "core",
			namespace: "test-ns",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(
					`DELETE FROM proxysql_servers WHERE hostname = "pod-ip"`,
				).WillReturnError(
					errSQLTest,
				)
			},
			expectFunc: func(t *testing.T, err error) {
				if err == nil {
					t.Errorf("expected error, got nil")
					return
				}

				// Check for the wrapped error message
				if !strings.Contains(err.Error(), "SQL error") || !strings.Contains(err.Error(), "failed to execute command") {
					t.Errorf("expected error to contain both 'SQL error' and 'failed to execute command', got %v", err)
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("Failed to create mock database connection: %v", err)
			}
			defer db.Close()

			mock.MatchExpectationsInOrder(true)

			p := &ProxySQL{nil, db, newTestConfig()}

			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: tc.namespace,
					Labels: map[string]string{
						"component": tc.component,
					},
				},
				Status: v1.PodStatus{
					PodIP: "pod-ip",
				},
			}

			tc.setupMock(mock)

			err = p.removePodFromCluster(pod)
			tc.expectFunc(t, err)

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("Unfulfilled expectations: %s", err)
			}
		})
	}
}

// Helper function to set up common runtime load expectations.
func expectRuntimeLoads(mock sqlmock.Sqlmock) {
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
}
