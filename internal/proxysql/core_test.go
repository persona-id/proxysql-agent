package proxysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"gopkg.in/DATA-DOG/go-sqlmock.v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

// Define a static error for tests.
var errSQLTest = errors.New("SQL error")

func TestCore(t *testing.T) {
	t.Parallel()

	t.Run("TODO", func(t *testing.T) {
		t.Parallel()

		t.Log("TODO test")
		t.Skip("TODO test")
	})
}

func TestPodUpdated(t *testing.T) {
	t.Parallel()

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
					regexp.QuoteMeta(`INSERT INTO proxysql_servers VALUES ("10.0.0.3", 6032, 0, "new-pod")`),
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
					`DELETE FROM proxysql_servers WHERE hostname = "10.0.0.2"`,
				).WillReturnResult(
					sqlmock.NewResult(0, 1),
				)

				expectRuntimeLoads(mock)
			},
		},
		{
			name:        "pod succeeded",
			oldPodPhase: "Running",
			newPodPhase: "Succeeded",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(
					`DELETE FROM proxysql_servers WHERE hostname = "10.0.0.2"`,
				).WillReturnResult(
					sqlmock.NewResult(0, 1),
				)

				expectRuntimeLoads(mock)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("Failed to create mock database connection: %v", err)
			}
			defer db.Close()

			mock.MatchExpectationsInOrder(true)

			p := &ProxySQL{
				clientset:     nil,
				conn:          db,
				settings:      newTestConfig(),
				shutdownOnce:  sync.Once{},
				shutdownPhase: PhaseRunning,
				shutdownMu:    sync.RWMutex{},
				httpServer:    nil,
			}

			oldpod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "old-pod",
					Namespace: "test-ns",
					Labels: map[string]string{
						"component": "core",
					},
				},
				Status: v1.PodStatus{
					PodIP: "10.0.0.2",
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
					PodIP: "10.0.0.3",
					Phase: v1.PodPhase(tc.newPodPhase),
				},
			}

			tc.setupMock(mock)

			p.podUpdated(oldpod, newpod)

			err = mock.ExpectationsWereMet()
			if err != nil {
				t.Errorf("Unfulfilled expectations: %s", err)
			}
		})
	}
}

func TestPodAdded(t *testing.T) {
	t.Parallel()

	// podAdded is now async — it spawns a goroutine. Only early returns
	// (before the goroutine is spawned) are tested here. Logic tests are in
	// TestAddPodWhenReady.
	testCases := []struct {
		name      string
		setupMock func(mock sqlmock.Sqlmock)
		pod       *v1.Pod
	}{
		{
			name: "pending pod does not spawn goroutine",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels:    map[string]string{"component": "core"},
				},
				Status: v1.PodStatus{
					PodIP: "10.0.0.1",
					Phase: v1.PodPending,
				},
			},
			setupMock: func(_ sqlmock.Sqlmock) {},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("Failed to create mock database connection: %v", err)
			}
			defer db.Close()

			mock.MatchExpectationsInOrder(true)

			p := &ProxySQL{
				clientset:     nil,
				conn:          db,
				settings:      newTestConfig(),
				shutdownOnce:  sync.Once{},
				shutdownPhase: PhaseRunning,
				shutdownMu:    sync.RWMutex{},
				httpServer:    nil,
			}

			tc.setupMock(mock)

			p.podAdded(tc.pod)

			err = mock.ExpectationsWereMet()
			if err != nil {
				t.Errorf("Unfulfilled expectations: %s", err)
			}
		})
	}
}

func TestAddPodWhenReady(t *testing.T) {
	t.Parallel()

	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("Failed to get hostname: %v", err)
	}

	testCases := []struct {
		name      string
		setupMock func(mock sqlmock.Sqlmock)
		pod       *v1.Pod
		ctxFn     func() (context.Context, context.CancelFunc)
	}{
		{
			name: "pod already exists in cluster",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      hostname,
					Namespace: "test-ns",
					Labels:    map[string]string{"component": "core"},
				},
				Status: v1.PodStatus{
					PodIP: "10.0.0.1",
					Phase: v1.PodRunning,
				},
			},
			ctxFn: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 5*time.Second)
			},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(
					regexp.QuoteMeta(`SELECT count(*) FROM proxysql_servers WHERE hostname = "10.0.0.1"`),
				).WillReturnRows(
					sqlmock.NewRows([]string{"count"}).AddRow(1),
				)
			},
		},
		{
			name: "pod does not exist in cluster",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      hostname,
					Namespace: "test-ns",
					Labels:    map[string]string{"component": "core"},
				},
				Status: v1.PodStatus{
					PodIP: "10.0.0.1",
					Phase: v1.PodRunning,
				},
			},
			ctxFn: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 5*time.Second)
			},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(
					regexp.QuoteMeta(`SELECT count(*) FROM proxysql_servers WHERE hostname = "10.0.0.1"`),
				).WillReturnRows(
					sqlmock.NewRows([]string{"count"}).AddRow(0),
				)

				mock.ExpectExec("DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'").
					WillReturnResult(sqlmock.NewResult(0, 1))

				mock.ExpectExec(
					regexp.QuoteMeta(fmt.Sprintf(`INSERT INTO proxysql_servers VALUES ("10.0.0.1", 6032, 0, %q)`, hostname)),
				).WillReturnResult(
					sqlmock.NewResult(0, 1),
				)

				expectRuntimeLoads(mock)
			},
		},
		{
			name: "pod with a different name is added to cluster",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "other-core-pod",
					Namespace: "test-ns",
					Labels:    map[string]string{"component": "core"},
				},
				Status: v1.PodStatus{
					PodIP: "10.0.0.4",
					Phase: v1.PodRunning,
				},
			},
			ctxFn: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 5*time.Second)
			},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(
					regexp.QuoteMeta(`SELECT count(*) FROM proxysql_servers WHERE hostname = "10.0.0.4"`),
				).WillReturnRows(
					sqlmock.NewRows([]string{"count"}).AddRow(0),
				)

				mock.ExpectExec("DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'").
					WillReturnResult(sqlmock.NewResult(0, 1))

				mock.ExpectExec(
					regexp.QuoteMeta(`INSERT INTO proxysql_servers VALUES ("10.0.0.4", 6032, 0, "other-core-pod")`),
				).WillReturnResult(
					sqlmock.NewResult(0, 1),
				)

				expectRuntimeLoads(mock)
			},
		},
		{
			name: "retries more than 3 times until ProxySQL is ready",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels:    map[string]string{"component": "core"},
				},
				Status: v1.PodStatus{
					PodIP: "10.0.0.1",
					Phase: v1.PodRunning,
				},
			},
			ctxFn: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 10*time.Second)
			},
			setupMock: func(mock sqlmock.Sqlmock) {
				// Fail 5 times — more than the old 3-retry maximum.
				for range 5 {
					mock.ExpectQuery(
						regexp.QuoteMeta(`SELECT count(*) FROM proxysql_servers WHERE hostname = "10.0.0.1"`),
					).WillReturnError(errSQLTest)
				}

				mock.ExpectQuery(
					regexp.QuoteMeta(`SELECT count(*) FROM proxysql_servers WHERE hostname = "10.0.0.1"`),
				).WillReturnRows(
					sqlmock.NewRows([]string{"count"}).AddRow(0),
				)

				mock.ExpectExec("DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'").
					WillReturnResult(sqlmock.NewResult(0, 1))

				mock.ExpectExec(
					regexp.QuoteMeta(`INSERT INTO proxysql_servers VALUES ("10.0.0.1", 6032, 0, "test-pod")`),
				).WillReturnResult(
					sqlmock.NewResult(0, 1),
				)

				expectRuntimeLoads(mock)
			},
		},
		{
			name: "stops retrying when context is cancelled",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels:    map[string]string{"component": "core"},
				},
				Status: v1.PodStatus{
					PodIP: "10.0.0.1",
					Phase: v1.PodRunning,
				},
			},
			ctxFn: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel() // already cancelled — no queries should execute

				return ctx, cancel
			},
			setupMock: func(_ sqlmock.Sqlmock) {},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("Failed to create mock database connection: %v", err)
			}
			defer db.Close()

			mock.MatchExpectationsInOrder(true)

			p := &ProxySQL{
				clientset:     nil,
				conn:          db,
				settings:      newTestConfig(),
				shutdownOnce:  sync.Once{},
				shutdownPhase: PhaseRunning,
				shutdownMu:    sync.RWMutex{},
				httpServer:    nil,
				retryDelay:    0, // no delay in tests
			}

			ctx, cancel := tc.ctxFn()
			defer cancel()

			tc.setupMock(mock)

			p.addPodWhenReady(ctx, tc.pod)

			if err = mock.ExpectationsWereMet(); err != nil {
				t.Errorf("Unfulfilled expectations: %s", err)
			}
		})
	}
}

func TestAddPodToCluster(t *testing.T) {
	t.Parallel()

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
					regexp.QuoteMeta(`INSERT INTO proxysql_servers VALUES ("10.0.0.1", 6032, 0, "test-pod")`),
				).WillReturnResult(
					sqlmock.NewResult(0, 1),
				)

				expectRuntimeLoads(mock)
			},
			expectFunc: func(t *testing.T, err error) {
				t.Helper()

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
				t.Helper()

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
				t.Helper()

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
			t.Parallel()

			p, mock, pod := setupPodTest(t, tc.namespace, tc.component)

			tc.setupMock(mock)

			err := p.addPodToCluster(context.Background(), pod)
			tc.expectFunc(t, err)

			err = mock.ExpectationsWereMet()
			if err != nil {
				t.Errorf("Unfulfilled expectations: %s", err)
			}
		})
	}
}

func TestRemovePodFromCluster(t *testing.T) {
	t.Parallel()

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
					`DELETE FROM proxysql_servers WHERE hostname = "10.0.0.1"`,
				).WillReturnResult(
					sqlmock.NewResult(0, 1),
				)

				expectRuntimeLoads(mock)
			},
			expectFunc: func(t *testing.T, err error) {
				t.Helper()

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
				t.Helper()

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
					`DELETE FROM proxysql_servers WHERE hostname = "10.0.0.1"`,
				).WillReturnError(
					errSQLTest,
				)
			},
			expectFunc: func(t *testing.T, err error) {
				t.Helper()

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
			t.Parallel()

			p, mock, pod := setupPodTest(t, tc.namespace, tc.component)

			tc.setupMock(mock)

			err := p.removePodFromCluster(context.Background(), pod)
			tc.expectFunc(t, err)

			err = mock.ExpectationsWereMet()
			if err != nil {
				t.Errorf("Unfulfilled expectations: %s", err)
			}
		})
	}
}

func TestReconcileCluster(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		pods        []*v1.Pod
		setupMock   func(mock sqlmock.Sqlmock)
		expectedErr bool
	}{
		{
			name: "no stale entries: nothing deleted",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "proxysql",
						Labels:    map[string]string{"app": "proxysql", "component": "core"},
					},
					Status: v1.PodStatus{Phase: v1.PodRunning, PodIP: "10.0.0.1"},
				},
			},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT hostname FROM proxysql_servers").
					WillReturnRows(sqlmock.NewRows([]string{"hostname"}).AddRow("10.0.0.1"))
			},
		},
		{
			name: "stale entries are removed",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "proxysql",
						Labels:    map[string]string{"app": "proxysql", "component": "core"},
					},
					Status: v1.PodStatus{Phase: v1.PodRunning, PodIP: "10.0.0.1"},
				},
			},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT hostname FROM proxysql_servers").
					WillReturnRows(sqlmock.NewRows([]string{"hostname"}).
						AddRow("10.0.0.1").
						AddRow("10.0.0.2").
						AddRow("10.0.0.3"))

				mock.ExpectExec(`DELETE FROM proxysql_servers WHERE hostname = "10.0.0.2"`).
					WillReturnResult(sqlmock.NewResult(1, 1))

				mock.ExpectExec(`DELETE FROM proxysql_servers WHERE hostname = "10.0.0.3"`).
					WillReturnResult(sqlmock.NewResult(1, 1))

				expectRuntimeLoads(mock)
			},
		},
		{
			name: "proxysql-core placeholder is never deleted",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "proxysql",
						Labels:    map[string]string{"app": "proxysql", "component": "core"},
					},
					Status: v1.PodStatus{Phase: v1.PodRunning, PodIP: "10.0.0.1"},
				},
			},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT hostname FROM proxysql_servers").
					WillReturnRows(sqlmock.NewRows([]string{"hostname"}).
						AddRow("proxysql-core").
						AddRow("10.0.0.1"))
			},
		},
		{
			name: "non-running pods treated as absent",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pending-pod",
						Namespace: "proxysql",
						Labels:    map[string]string{"app": "proxysql", "component": "core"},
					},
					Status: v1.PodStatus{Phase: v1.PodPending, PodIP: "10.0.0.1"},
				},
			},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT hostname FROM proxysql_servers").
					WillReturnRows(sqlmock.NewRows([]string{"hostname"}).AddRow("10.0.0.1"))

				mock.ExpectExec(`DELETE FROM proxysql_servers WHERE hostname = "10.0.0.1"`).
					WillReturnResult(sqlmock.NewResult(1, 1))

				expectRuntimeLoads(mock)
			},
		},
		{
			name: "error querying proxysql_servers returns error",
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "proxysql",
						Labels:    map[string]string{"app": "proxysql", "component": "core"},
					},
					Status: v1.PodStatus{Phase: v1.PodRunning, PodIP: "10.0.0.1"},
				},
			},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT hostname FROM proxysql_servers").
					WillReturnError(errSQLTest)
			},
			expectedErr: true,
		},
		{
			name: "error deleting stale entry returns error",
			pods: []*v1.Pod{},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT hostname FROM proxysql_servers").
					WillReturnRows(sqlmock.NewRows([]string{"hostname"}).AddRow("10.0.0.1"))

				mock.ExpectExec(`DELETE FROM proxysql_servers WHERE hostname = "10.0.0.1"`).
					WillReturnError(errSQLTest)
			},
			expectedErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("Failed to create mock database connection: %v", err)
			}

			t.Cleanup(func() { db.Close() })

			mock.MatchExpectationsInOrder(true)

			objs := make([]runtime.Object, len(tc.pods))
			for i, pod := range tc.pods {
				objs[i] = pod
			}

			p := &ProxySQL{
				clientset:     k8sfake.NewClientset(objs...),
				conn:          db,
				settings:      newTestConfig(),
				shutdownOnce:  sync.Once{},
				shutdownPhase: PhaseRunning,
				shutdownMu:    sync.RWMutex{},
			}

			tc.setupMock(mock)

			err = p.reconcileCluster(context.Background())

			if (err != nil) != tc.expectedErr {
				t.Errorf("reconcileCluster() error = %v, wantErr %v", err, tc.expectedErr)
			}

			if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
				t.Errorf("Unfulfilled expectations: %s", mockErr)
			}
		})
	}
}

func TestPodDeleted(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		setupMock func(mock sqlmock.Sqlmock)
		object    any
	}{
		{
			name: "core pod deleted removes from cluster",
			object: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels:    map[string]string{"component": "core"},
				},
				Status: v1.PodStatus{PodIP: "10.0.0.1"},
			},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(
					`DELETE FROM proxysql_servers WHERE hostname = "10.0.0.1"`,
				).WillReturnResult(sqlmock.NewResult(0, 1))

				expectRuntimeLoads(mock)
			},
		},
		{
			name: "satellite pod deleted only runs runtime loads",
			object: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels:    map[string]string{"component": "satellite"},
				},
				Status: v1.PodStatus{PodIP: "10.0.0.1"},
			},
			setupMock: func(mock sqlmock.Sqlmock) {
				expectRuntimeLoads(mock)
			},
		},
		{
			name:      "invalid object type is ignored",
			object:    "not-a-pod",
			setupMock: func(_ sqlmock.Sqlmock) {},
		},
		{
			name: "tombstone with core pod removes from cluster",
			object: cache.DeletedFinalStateUnknown{
				Key: "test-ns/test-pod",
				Obj: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-ns",
						Labels:    map[string]string{"component": "core"},
					},
					Status: v1.PodStatus{PodIP: "10.0.0.1"},
				},
			},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(
					`DELETE FROM proxysql_servers WHERE hostname = "10.0.0.1"`,
				).WillReturnResult(sqlmock.NewResult(0, 1))

				expectRuntimeLoads(mock)
			},
		},
		{
			name: "tombstone with invalid object type is ignored",
			object: cache.DeletedFinalStateUnknown{
				Key: "test-ns/test-pod",
				Obj: "not-a-pod",
			},
			setupMock: func(_ sqlmock.Sqlmock) {},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p, mock, _ := setupPodTest(t, "test-ns", "core")

			tc.setupMock(mock)

			p.podDeleted(tc.object)

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

// Helper function to set up common test infrastructure for pod operations.
// It's fine to return an interface here, that's what we want to do.
//
//nolint:ireturn
func setupPodTest(t *testing.T, namespace, component string) (*ProxySQL, sqlmock.Sqlmock, *v1.Pod) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create mock database connection: %v", err)
	}

	t.Cleanup(func() { db.Close() })

	mock.MatchExpectationsInOrder(true)

	p := &ProxySQL{
		clientset:     nil,
		conn:          db,
		settings:      newTestConfig(),
		shutdownOnce:  sync.Once{},
		shutdownPhase: PhaseRunning,
		shutdownMu:    sync.RWMutex{},
		httpServer:    nil,
	}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: namespace,
			Labels: map[string]string{
				"component": component,
			},
		},
		Status: v1.PodStatus{
			PodIP: "10.0.0.1",
		},
	}

	return p, mock, pod
}
