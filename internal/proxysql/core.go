package proxysql

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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

//
// Core mode specific functions
//

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

			p.shutdownOnce.Do(func() {
				err := p.startDraining(ctx)
				if err != nil {
					slog.Error("Failed to start draining", slog.Any("error", err))
					shutdownErr = err
				}

				// Perform graceful shutdown
				err = p.gracefulShutdown(ctx)
				if err != nil {
					slog.Error("Core graceful shutdown failed", slog.Any("error", err))

					if shutdownErr == nil {
						shutdownErr = err
					}
				}
			})

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
		DeleteFunc: nil,
	})
	if err != nil {
		return fmt.Errorf("failed to add event handler to pod informer: %w", err)
	}

	// block the main go routine from exiting
	<-stopper

	return nil
}

// This function is needed to do bootstrapping. At first I was using podUpdated to do adds, but we would never
// get the first pod to come up. This function will only be useful on the first core pod to come up, the rest will
// be handled via podUpdated.
//
// This feels a bit clumsy.
func (p *ProxySQL) podAdded(object any) {
	ctx := context.Background() // Use background context for event handlers

	pod, ok := object.(*v1.Pod)
	if !ok {
		return
	}

	// if the new pod is not THIS pod, bail out of this function. the rest of this function should only apply
	// to the first core pod to come up in the cluster.
	hostname, osErr := os.Hostname()
	if osErr != nil || pod.Name != hostname {
		return
	}

	// check if pod is already in the proxysql_servers table; this can happen when core pods add
	// other core pods.
	var count int

	cmd := "SELECT count(*) FROM proxysql_servers WHERE hostname = ?"

	err := p.conn.QueryRowContext(ctx, cmd, pod.Status.PodIP).Scan(&count)
	if err != nil {
		// Log the error but continue execution since this is a callback function
		slog.Error("error in podAdded()",
			slog.String("command", cmd),
			slog.Any("err", fmt.Errorf("failed to query proxysql_servers: %w", err)),
		)

		return
	}

	if count > 0 {
		return
	}

	err = p.addPodToCluster(ctx, pod)
	if err != nil {
		// Log the error but continue execution since this is a callback function
		slog.Error("error in addPodToCluster()", slog.Any("err", err))
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
	if oldpod.Status.Phase == "Pending" && newpod.Status.Phase == "Running" {
		err := p.addPodToCluster(ctx, newpod)
		if err != nil {
			// Log the error but continue execution since this is a callback function
			slog.Error("error in addPod()", slog.Any("err", err))
		}
	}

	// Pod is shutting down. Only run this for core pods, as satellites don't need special considerations when
	// they leave the cluster.
	if oldpod.Status.Phase == "Running" && newpod.Status.Phase == "Failed" {
		err := p.removePodFromCluster(ctx, oldpod)
		if err != nil {
			// Log the error but continue execution since this is a callback function
			slog.Error("error in removePod()", slog.Any("err", err))
		}
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
