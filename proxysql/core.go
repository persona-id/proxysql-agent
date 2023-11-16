package proxysql

import (
	"context"
	"fmt"
	"io/ioutil"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/spf13/viper"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// PodInfo represents information about a Kubernetes pod.
type PodInfo struct {
	PodIP    string
	Hostname string
}

// Core is the main function for ProxySQL core operations.
func (p *ProxySQL) Core() {
	interval := viper.GetViper().GetInt("core.interval")

	p.logger.Info("Core mode initialized, running loop", slog.Int("interval (s)", interval))

	for {
		p.coreLoop()

		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func (p *ProxySQL) coreLoop() {
	pods, err := GetCorePods()
	if err != nil {
		p.logger.Error("Failed to get pod info", slog.Any("error", err))
		return
	}

	checksumFile := "/tmp/pods-cs.txt"
	digest := calculateChecksum(pods)

	// Read the previous checksum from the file
	old, err := ioutil.ReadFile(checksumFile)
	if err != nil {
		old = []byte("")
	}

	// If nothing changes, we still run LOAD PROXYSQL SERVERS TO RUNTIME in order to accept any
	// new pods that have joined the cluster
	if string(old) == digest {
		command := "LOAD PROXYSQL SERVERS TO RUNTIME"
		_, err = p.conn.Exec(command)
		if err != nil {
			p.logger.Error("Command failed to execute", slog.String("command", command), slog.Any("error", err))
		}
		return
	}

	commands := createCommands(pods)
	for _, command := range commands {
		_, err = p.conn.Exec(command)
		if err != nil {
			p.logger.Error("Commands failed", slog.String("commands", command), slog.Any("error", err))
		}
	}

	// Write the new checksum to the file for the next run
	err = ioutil.WriteFile(checksumFile, []byte(digest), 0644)
	if err != nil {
		p.logger.Error("Failed to write to checksum file", slog.String("file", checksumFile), slog.Any("error", err))
	}

	p.logger.Info("Commands ran", slog.String("commands", strings.Join(commands, "; ")))
}

func GetCorePods() ([]PodInfo, error) {
	app := viper.GetViper().GetString("core.pod_selector.app")
	component := viper.GetViper().GetString("core.pod_selector.component")

	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	pods, _ := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s,component=%s", app, component),
	})

	var corePods []PodInfo
	for _, pod := range pods.Items {
		corePods = append(corePods, PodInfo{PodIP: pod.Status.PodIP, Hostname: pod.Name})
	}

	return corePods, err
}

func calculateChecksum(pods []PodInfo) string {
	data := []string{}

	for _, pod := range pods {
		data = append(data, fmt.Sprintf("%s:%s", pod.PodIP, pod.Hostname))
	}

	sort.Strings(data)

	return fmt.Sprintf("%x", data)
}

func createCommands(pods []PodInfo) []string {
	commands := []string{"DELETE FROM proxysql_servers"}

	for _, pod := range pods {
		commands = append(commands, fmt.Sprintf("INSERT INTO proxysql_servers VALUES ('%s', 6032, 0, '%s')", pod.PodIP, pod.Hostname))
	}

	commands = append(commands,
		"LOAD PROXYSQL SERVERS TO RUNTIME",
		"LOAD ADMIN VARIABLES TO RUNTIME",
		"LOAD MYSQL VARIABLES TO RUNTIME",
		"LOAD MYSQL SERVERS TO RUNTIME",
		"LOAD MYSQL USERS TO RUNTIME",
		"LOAD MYSQL QUERY RULES TO RUNTIME",
	)

	return commands
}
