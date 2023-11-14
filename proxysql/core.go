package proxysql

import (
	"context"
	"fmt"
	"io/ioutil"
	"sort"
	"strings"
	"time"

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
	p.logger.Info().Msg("Core mode initialized; looping every 10s")

	for {
		p.coreLoop()
		time.Sleep(10 * time.Second)
	}
}

func (p *ProxySQL) coreLoop() {
	pods, err := GetCorePods()
	if err != nil {
		p.logger.Error().Err(err).Msg("Failed to get pod info")
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
			p.logger.Error().Err(err).Str("command", command).Msg("command failed")
		}
		return
	}

	commands := createCommands(pods)
	for _, command := range commands {
		_, err = p.conn.Exec(command)
		if err != nil {
			p.logger.Error().Err(err).Str("commands", command).Msg("command failed")
		}
	}

	// Write the new checksum to the file for the next run
	err = ioutil.WriteFile(checksumFile, []byte(digest), 0644)
	if err != nil {
		p.logger.Error().Err(err).Str("file", checksumFile).Msg("Failed to write to checksum file")
	}

	p.logger.Debug().Str("commands", strings.Join(commands, "; ")).Send()
}

func GetCorePods() ([]PodInfo, error) {
	// FIXME: make this, and labels, configurable
	app := "proxysql"

	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	pods, _ := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s,component=core", app),
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
		"LOAD MYSQL VARIABLES TO RUNTIME",
		"LOAD MYSQL SERVERS TO RUNTIME",
		"LOAD MYSQL USERS TO RUNTIME",
		"LOAD MYSQL QUERY RULES TO RUNTIME",
	)

	return commands
}
