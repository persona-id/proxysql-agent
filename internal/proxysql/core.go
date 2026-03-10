package proxysql

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	// This comment is reqiured to pass golint.
	_ "github.com/go-sql-driver/mysql"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

// ProxySQL core functions.
//
// The core pods need to run certain commands when specific pods joins or leaves the
// cluster, so this function sets up an informer that watches the k8s pods and runs
// functions when pods change.
//
// Joining:
//
// When a new core pod joins the cluster, one of two things happen:
//   - if it's the first core pod, it uses the podAdded callback to add itself to the proxysql_servers table
//   - if other core pods are already running, one of them will use add the new pod via the podUpdated function
//
// When a new satellite pod joins the cluster, the core pods all run the "LOAD X TO RUNTIME" commands, which
// accepts the new pod and distributes the configuration to it.
//
// Leaving:
//
//   - When a satellite pod leaves the cluster, nothing needs to be done.
//   - When a core pod leaves the cluster, the remaining core pods all delete that pod from the proxysql_servers
//     table and run all of the LOAD X TO RUNTIME commands.
//
// FIXME(kuzmik): core pods actually don't need to gracefully shutddown, so we can remove some of this code here.
func (p *ProxySQL) Core(ctx context.Context) error {
	if p.clientset == nil {
		config, err := rest.InClusterConfig()
		if err != nil {
			return fmt.Errorf("failed to get in-cluster config: %w", err)
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			return fmt.Errorf("failed to create kubernetes clientset: %w", err)
		}

		p.clientset = clientset
	}

	// stop signal for the informer
	stopper := make(chan struct{})

	defer func() {
		select {
		case <-stopper:
		default:
			close(stopper)
		}
	}()

	// Handle context cancellation
	go func() {
		select {
		case <-ctx.Done():
			slog.Info("Context cancelled, stopping core informer")

			var shutdownErr error

			p.shutdownOnce.Do(func() { //nolint:contextcheck
				err := p.startDraining()
				if err != nil {
					slog.Error("Failed to start draining", slog.Any("error", err))
					shutdownErr = err
				}

				// Wait for in-flight podAdded/reconcile goroutines before closing the
				// connection. startDraining sets IsShuttingDown()=true so they exit on
				// their next check, which happens quickly (within a single SQL round-trip).
				p.podWg.Wait()

				// Perform graceful shutdown
				err = p.gracefulShutdown(context.Background())
				if err != nil {
					slog.Error("Core graceful shutdown failed", slog.Any("error", err))

					if shutdownErr == nil {
						shutdownErr = err
					}
				}
			})

			// Shut down the HTTP server outside gracefulShutdown to avoid the
			// preStop handler deadlock (see shutdownHTTPServer for details).
			p.shutdownHTTPServer() //nolint:contextcheck

			select {
			case <-stopper:
			default:
				close(stopper)
			}

		case <-stopper:
			return
		}
	}()

	app := p.settings.Core.PodSelector.App
	namespace := p.settings.Core.PodSelector.Namespace

	labelSelector := labels.Set(map[string]string{
		"app": app,
	}).AsSelector()

	factory := informers.NewSharedInformerFactoryWithOptions(
		p.clientset,
		1*time.Second,
		informers.WithNamespace(namespace),
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.LabelSelector = labelSelector.String()
		}),
	)

	podInformer := factory.Core().V1().Pods().Informer()

	defer runtime.HandleCrash()

	go factory.Start(stopper)

	if !cache.WaitForCacheSync(stopper, podInformer.HasSynced) {
		return ErrCacheTimeout
	}

	_, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    p.podAdded,
		UpdateFunc: p.podUpdated,
		DeleteFunc: p.podDeleted,
	})
	if err != nil {
		return fmt.Errorf("failed to add event handler to pod informer: %w", err)
	}

	// Spawn a goroutine that reconciles proxysql_servers against currently-running pods.
	// First performs an initial reconciliation with retries (to clean up stale entries from
	// previous deployments), then continues running periodically to catch entries that become
	// stale after startup (e.g. during rolling deployments where old pods terminate after
	// the initial reconciliation completes).
	p.podWg.Go(func() { //nolint:contextcheck
		p.reconcileLoop(context.Background(), time.Duration(p.settings.Core.Interval)*time.Second)
	})

	// block the main go routine from exiting
	<-stopper

	// Wait for all in-flight podAdded goroutines to complete before returning.
	p.podWg.Wait()

	return nil
}

const (
	// podAddedRetryTimeout is the maximum time podAdded will retry waiting for
	// ProxySQL's cluster tables to become ready. Observed startup times are ~30s,
	// so 60s gives comfortable headroom.
	podAddedRetryTimeout = 60 * time.Second

	// podAddedRetryDelay is the wait between retries when ProxySQL is not yet ready.
	podAddedRetryDelay = 500 * time.Millisecond

	// defaultReconcileInterval is used when the configured core.interval is zero or negative.
	defaultReconcileInterval = 10 * time.Second
)

// reconcileLoop performs an initial reconciliation with retries, then runs
// reconcileCluster periodically at the given interval until the context is
// cancelled or the agent begins shutting down.
func (p *ProxySQL) reconcileLoop(ctx context.Context, interval time.Duration) {
	// Guard against zero/negative interval which would panic in time.NewTicker.
	if interval <= 0 {
		interval = defaultReconcileInterval
	}

	// Phase 1: Initial reconciliation with retries (bounded timeout).
	initCtx, initCancel := context.WithTimeout(ctx, podAddedRetryTimeout)

	for {
		if initCtx.Err() != nil {
			slog.Error("startup reconciliation timed out")
			initCancel()

			return
		}

		if p.IsShuttingDown() {
			initCancel()

			return
		}

		if err := p.reconcileCluster(initCtx); err != nil {
			slog.Debug("startup reconciliation failed, retrying", slog.Any("error", err))
			time.Sleep(p.retryDelay)

			continue
		}

		break
	}

	initCancel()

	// Phase 2: Periodic reconciliation.
	slog.Info("initial reconciliation complete, starting periodic reconciliation",
		slog.Duration("interval", interval),
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if p.IsShuttingDown() {
				return
			}

			if err := p.reconcileCluster(ctx); err != nil {
				slog.Debug("periodic reconciliation failed", slog.Any("error", err))
			}
		}
	}
}

// podAdded handles bootstrapping when core pods start. It fires for all pods visible during
// the initial informer cache sync. By handling all Running pods (not just own hostname), it covers
// the case where multiple core pods start simultaneously — each pod adds all peers it sees, so the
// cluster forms even when no Pending→Running transitions are observed by podUpdated.
//
// The actual retry logic runs in a goroutine so the informer is not blocked while waiting for
// ProxySQL's cluster tables to become ready (which can take ~30s after startup).
func (p *ProxySQL) podAdded(object any) {
	pod, ok := object.(*v1.Pod)
	if !ok {
		return
	}

	// Only handle Running pods; Pending→Running transitions are caught by podUpdated.
	if pod.Status.Phase != v1.PodRunning {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), podAddedRetryTimeout)

	p.podWg.Go(func() {
		defer cancel()
		p.addPodWhenReady(ctx, pod)
	})
}

// addPodWhenReady retries the proxysql_servers availability check until the context expires,
// then adds the pod to the cluster if it is not already present. It is called from a goroutine
// spawned by podAdded and can be called directly in tests.
func (p *ProxySQL) addPodWhenReady(ctx context.Context, pod *v1.Pod) {
	cmd := fmt.Sprintf("SELECT count(*) FROM proxysql_servers WHERE hostname = %q", pod.Status.PodIP) //nolint:gosec

	for {
		if ctx.Err() != nil {
			slog.Error("error in podAdded()",
				slog.String("pod", pod.Name),
				slog.String("reason", "timed out waiting for proxysql_servers to be ready"),
			)

			return
		}

		if p.IsShuttingDown() {
			return
		}

		var count int

		err := p.conn.QueryRowContext(ctx, cmd).Scan(&count)
		if err != nil {
			if ctx.Err() != nil {
				slog.Error("error in podAdded()",
					slog.String("pod", pod.Name),
					slog.String("reason", "timed out waiting for proxysql_servers to be ready"),
				)

				return
			}

			slog.Debug("error in podAdded(), retrying", slog.String("pod", pod.Name), slog.Any("err", err))

			time.Sleep(p.retryDelay)

			continue
		}

		if count > 0 {
			return
		}

		if err = p.addPodToCluster(ctx, pod); err != nil {
			slog.Error("error in podAdded()", slog.Any("err", err))
		}

		return
	}
}

// We aren't using podAdded here when other core pods exist because that function doesn't always get the PodIP,
// and this one does. Using this function doesn't work when bootstrapping a cluster, because the pod has started
// before the informer has started. In other words, the pod starts before the pod can detect itself joining the
// cluster.
//
// Example pod (scaled up core-1, then scaled it back down):
//
//	OLD POD NAME	OLD POD IP			OLD STATUS	NEW POD NAME		NEW POD IP			NEW STATUS
//	proxysql-core-1						Pending 	proxysql-core-1 	192.168.194.102 	Running
//	proxysql-core-1	192.168.194.102 	Running 	proxysql-core-1  						Failed
func (p *ProxySQL) podUpdated(oldobject, newobject any) {
	ctx := context.Background()

	// cast both objects into Pods, and if that fails leave the function
	oldpod, ok := oldobject.(*v1.Pod)
	if !ok {
		return
	}

	newpod, ok := newobject.(*v1.Pod)
	if !ok {
		return
	}

	// Pod is new and transitioned to running, so we add that to the proxysql_servers table.
	if oldpod.Status.Phase == v1.PodPending && newpod.Status.Phase == v1.PodRunning {
		err := p.addPodToCluster(ctx, newpod)
		if err != nil {
			// Log the error but continue execution since this is a callback function
			slog.Error("error in addPod()", slog.Any("err", err))
		}
	}

	// Pod is shutting down. Only run this for core pods, as satellites don't need special considerations when
	// they leave the cluster.
	if oldpod.Status.Phase == v1.PodRunning && (newpod.Status.Phase == v1.PodFailed || newpod.Status.Phase == v1.PodSucceeded) {
		err := p.removePodFromCluster(ctx, oldpod)
		if err != nil {
			// Log the error but continue execution since this is a callback function
			slog.Error("error in podDeleted()", slog.Any("err", err))
		}
	}
}

// reconcileCluster removes stale entries from proxysql_servers that don't correspond
// to any currently-running core pod. It's called once at startup to clean up entries
// left over from previous deployments whose delete events were never processed.
func (p *ProxySQL) reconcileCluster(ctx context.Context) error {
	app := p.settings.Core.PodSelector.App
	component := p.settings.Core.PodSelector.Component
	namespace := p.settings.Core.PodSelector.Namespace

	podList, err := p.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(map[string]string{
			"app":       app,
			"component": component,
		}).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	runningIPs := make(map[string]bool)

	for i := range podList.Items {
		pod := &podList.Items[i]

		if pod.Status.Phase == v1.PodRunning && pod.Status.PodIP != "" {
			runningIPs[pod.Status.PodIP] = true
		}
	}

	rows, err := p.conn.QueryContext(ctx, "SELECT hostname FROM proxysql_servers")
	if err != nil {
		return fmt.Errorf("failed to query proxysql_servers: %w", err)
	}

	defer rows.Close()

	var stale []string

	for rows.Next() {
		var hostname string

		if err := rows.Scan(&hostname); err != nil {
			return fmt.Errorf("failed to scan hostname: %w", err)
		}

		if hostname == "proxysql-core" {
			continue
		}

		if !runningIPs[hostname] {
			stale = append(stale, hostname)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error reading proxysql_servers rows: %w", err)
	}

	if len(stale) == 0 {
		return nil
	}

	slog.Info("reconciliation: removing stale entries", slog.Int("count", len(stale)))

	for _, hostname := range stale {
		slog.Info("removing stale proxysql_servers entry", slog.String("hostname", hostname))

		if _, err := p.conn.ExecContext(ctx, fmt.Sprintf("DELETE FROM proxysql_servers WHERE hostname = %q", hostname)); err != nil {
			return fmt.Errorf("failed to delete stale entry %q: %w", hostname, err)
		}
	}

	commands := []string{
		"LOAD PROXYSQL SERVERS TO RUNTIME",
		"LOAD ADMIN VARIABLES TO RUNTIME",
		"LOAD MYSQL VARIABLES TO RUNTIME",
		"LOAD MYSQL SERVERS TO RUNTIME",
		"LOAD MYSQL USERS TO RUNTIME",
		"LOAD MYSQL QUERY RULES TO RUNTIME",
	}

	for _, cmd := range commands {
		if _, err := p.conn.ExecContext(ctx, cmd); err != nil {
			return fmt.Errorf("failed to execute %q: %w", cmd, err)
		}
	}

	return nil
}

// podDeleted handles pod deletion events from the informer. It removes the pod from
// the proxysql_servers table so ProxySQL's cluster monitor stops trying to reach it.
// The object may be a *v1.Pod or a cache.DeletedFinalStateUnknown tombstone (used when
// the informer missed the delete event during a resync).
func (p *ProxySQL) podDeleted(object any) {
	pod, ok := object.(*v1.Pod)
	if !ok {
		tombstone, ok := object.(cache.DeletedFinalStateUnknown)
		if !ok {
			slog.Error("error in podDeleted(): unexpected type", slog.String("type", fmt.Sprintf("%T", object)))

			return
		}

		pod, ok = tombstone.Obj.(*v1.Pod)
		if !ok {
			slog.Error("error in podDeleted(): tombstone unexpected type", slog.String("type", fmt.Sprintf("%T", tombstone.Obj)))

			return
		}
	}

	ctx := context.Background()

	if err := p.removePodFromCluster(ctx, pod); err != nil {
		slog.Error("error in removePodFromCluster()", slog.Any("err", err))
	}
}

// Add the new pod to the cluster.
//   - If it's a core pod, add it to the proxysql_servers table
//   - if it's a satellite pod, run the commands to accept it to the cluster
func (p *ProxySQL) addPodToCluster(ctx context.Context, pod *v1.Pod) error {
	if p.IsShuttingDown() {
		slog.Debug("skipping add pod to cluster: shutting down")

		return nil
	}

	slog.Info("pod joined cluster",
		slog.String("name", pod.Name),
		slog.String("ip", pod.Status.PodIP),
	)

	commands := []string{"DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'"}

	// If the new pod is a core pod, delete the default entries in the proxysql_server list and add the new pod to it.
	if pod.Labels["component"] == "core" {
		port, err := p.settings.ClusterPort()
		if err != nil {
			return fmt.Errorf("failed to get cluster port: %w", err)
		}

		commands = append(commands, fmt.Sprintf("INSERT INTO proxysql_servers VALUES (%q, %d, 0, %q)", pod.Status.PodIP, port, pod.Name))
	}

	commands = append(commands,
		"LOAD PROXYSQL SERVERS TO RUNTIME",
		"LOAD ADMIN VARIABLES TO RUNTIME",
		"LOAD MYSQL VARIABLES TO RUNTIME",
		"LOAD MYSQL SERVERS TO RUNTIME",
		"LOAD MYSQL USERS TO RUNTIME",
		"LOAD MYSQL QUERY RULES TO RUNTIME",
	)

	for _, command := range commands {
		if p.IsShuttingDown() {
			slog.Debug("skipping command during shutdown", slog.String("command", command))

			return nil
		}

		_, err := p.conn.ExecContext(ctx, command)
		if err != nil {
			return fmt.Errorf("failed to execute command '%s': %w", command, err)
		}
	}

	slog.Debug("ran commands", slog.Any("commands", strings.Join(commands, ", ")))

	return nil
}

// Remove a core pod from the cluster when it leaves. This function just deletes the pod from
// proxysql_servers based on the hostname (PodIP here, technically). The function then runs all the
// LOAD TO RUNTIME commands required to sync state to the rest of the cluster.
func (p *ProxySQL) removePodFromCluster(ctx context.Context, pod *v1.Pod) error {
	if p.IsShuttingDown() {
		slog.Debug("skipping remove pod from cluster: shutting down")

		return nil
	}

	slog.Info("pod exited cluster",
		slog.String("name", pod.Name),
		slog.String("ip", pod.Status.PodIP),
	)

	commands := []string{}

	if pod.Labels["component"] == "core" {
		commands = append(commands, fmt.Sprintf("DELETE FROM proxysql_servers WHERE hostname = %q", pod.Status.PodIP))
	}

	commands = append(commands,
		"LOAD PROXYSQL SERVERS TO RUNTIME",
		"LOAD ADMIN VARIABLES TO RUNTIME",
		"LOAD MYSQL VARIABLES TO RUNTIME",
		"LOAD MYSQL SERVERS TO RUNTIME",
		"LOAD MYSQL USERS TO RUNTIME",
		"LOAD MYSQL QUERY RULES TO RUNTIME",
	)

	for _, command := range commands {
		if p.IsShuttingDown() {
			slog.Debug("skipping command during shutdown", slog.String("command", command))

			return nil
		}

		_, err := p.conn.ExecContext(ctx, command)
		if err != nil {
			return fmt.Errorf("failed to execute command '%s': %w", command, err)
		}
	}

	slog.Debug("ran commands", slog.Any("commands", strings.Join(commands, ", ")))

	return nil
}
