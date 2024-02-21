package proxysql

import (
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
func (p *ProxySQL) Core() {
	if p.clientset == nil {
		config, err := rest.InClusterConfig()
		if err != nil {
			slog.Error("error", slog.Any("err", err))
			panic(err)
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			slog.Error("error", slog.Any("err", err))
			panic(err)
		}

		p.clientset = clientset
	}

	// stop signal for the informer
	stopper := make(chan struct{})
	defer close(stopper)

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
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	_, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    p.podAdded,
		UpdateFunc: p.podUpdated,
	})
	if err != nil {
		slog.Error("Error creating Informer", slog.Any("err", err))
		panic(err)
	}

	// block the main go routine from exiting
	<-stopper
}

// This function is needed to do bootstrapping. At first I was using podUpdated to do adds, but we would never
// get the first pod to come up. This function will only be useful on the first core pod to come up, the rest will
// be handled via podUpdated.
//
// This feels a bit clumsy.
func (p *ProxySQL) podAdded(object interface{}) {
	pod, ok := object.(*v1.Pod)
	if !ok {
		return
	}

	// if the new pod is not THIS pod, bail out of this function. the rest of this function should only apply
	// to the first core pod to come up in the cluster.
	if hostname, _ := os.Hostname(); pod.Name != hostname {
		return
	}

	// check if pod is already in the proxysql_servers table; this can happen when core pods add
	// other core pods.
	var count int

	cmd := fmt.Sprintf("SELECT count(*) FROM proxysql_servers WHERE hostname = %q", pod.Status.PodIP)

	err := p.conn.QueryRow(cmd).Scan(&count)
	if err != nil {
		slog.Error("Error in podAdded()", slog.String("command", cmd), slog.Any("err", err))
	}

	if count > 0 {
		return
	}

	err = p.addPodToCluster(pod)
	if err != nil {
		slog.Error("Error in podAdded().addPodToCluster()", slog.Any("err", err))
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
func (p *ProxySQL) podUpdated(oldobject interface{}, newobject interface{}) {
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
		err := p.addPodToCluster(newpod)
		if err != nil {
			slog.Error("Error in addPodToCluster()", slog.Any("err", err))
		}
	}

	// Pod is shutting down. Only run this for core pods, as satellites don't need special considerations when
	// they leave the cluster.
	if oldpod.Status.Phase == "Running" && newpod.Status.Phase == "Failed" {
		err := p.removePodFromCluster(oldpod)
		if err != nil {
			slog.Error("Error in removePodFromCluster()", slog.Any("err", err))
		}
	}
}

// Add the new pod to the cluster.
//   - If it's a core pod, add it to the proxysql_servers table
//   - if it's a satellite pod, run the commands to accept it to the cluster
func (p *ProxySQL) addPodToCluster(pod *v1.Pod) error {
	slog.Info("Pod joined the cluster", slog.String("name", pod.Name), slog.String("ip", pod.Status.PodIP))

	commands := []string{"DELETE FROM proxysql_servers WHERE hostname = 'proxysql-core'"}

	// If the new pod is a core pod, delete the default entries in the proxysql_server list and add the new pod to it.
	if pod.Labels["component"] == "core" {
		// TODO: maybe make this configurable, not everyone will name the service this.
		commands = append(commands, fmt.Sprintf("INSERT INTO proxysql_servers VALUES (%q, 6032, 0, %q)", pod.Status.PodIP, pod.Name))
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
		_, err := p.conn.Exec(command)
		if err != nil {
			// FIXME: wrap error with extra info and return
			slog.Error("Command failed in addPodToCluster()", slog.String("command", command), slog.Any("error", err))
			return err
		}
	}

	slog.Debug("Ran commands", slog.Any("commands", strings.Join(commands, ", ")))

	return nil
}

// Remove a core pod from the cluster when it leaves. This function just deletes the pod from
// proxysql_servers based on the hostname (PodIP here, technically). The function then runs all the
// LOAD TO RUNTIME commands required to sync state to the rest of the cluster.
func (p *ProxySQL) removePodFromCluster(pod *v1.Pod) error {
	slog.Info("Pod left the cluster", slog.String("name", pod.Name), slog.String("ip", pod.Status.PodIP))

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
		_, err := p.conn.Exec(command)
		if err != nil {
			slog.Error("Command failed in removePodFromCluster()", slog.Any("command", command), slog.Any("error", err))
			return err
		}
	}

	slog.Debug("Ran commands", slog.Any("commands", strings.Join(commands, ", ")))

	return nil
}
