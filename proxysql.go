package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type ProxySQL struct {
	conn     *sql.DB
	settings *config
}

func (p *ProxySQL) New(configs *config) (*ProxySQL, error) {
	settings := configs
	address := settings.ProxySQL.Address
	username := settings.ProxySQL.Username
	password := settings.ProxySQL.Password

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/", username, password, address)

	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}

	err = conn.Ping()
	if err != nil {
		return nil, err
	}

	slog.Info("Connected to ProxySQL admin", slog.String("Host", address))

	return &ProxySQL{conn, settings}, nil
}

func (p *ProxySQL) Ping() error {
	return p.conn.Ping()
}

func (p *ProxySQL) GetBackends() (map[string]int, error) {
	entries := make(map[string]int)

	rows, err := p.conn.Query("SELECT hostgroup_id, hostname, port FROM runtime_mysql_servers ORDER BY hostgroup_id")
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var hostgroup int
		var hostname string
		var port int

		err := rows.Scan(&hostgroup, &hostname, &port)
		if err != nil {
			return nil, err
		}

		entries[hostname] = hostgroup
		if rows.Err() != nil && rows.Err() != sql.ErrNoRows {
			return nil, rows.Err()
		}
	}

	return entries, nil
}

//
// Satellite mode specific functons
//

func (p *ProxySQL) Satellite() {
	interval := p.settings.Satellite.Interval

	slog.Info("Satellite mode initialized, looping", slog.Int("interval", interval))

	for {
		err := p.SatelliteResync()
		if err != nil {
			slog.Error("Error running resync", slog.Any("error", err))
		}

		p.Ping()
		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func (p *ProxySQL) GetMissingCorePods() (int, error) {
	var count int = -1

	row := p.conn.QueryRow("SELECT COUNT(hostname) FROM stats_proxysql_servers_metrics WHERE last_check_ms > 30000 AND hostname != 'proxysql-core' AND Uptime_s > 0")

	err := row.Scan(&count)
	if err != nil {
		return count, err
	}

	return count, nil
}

func (p *ProxySQL) SatelliteResync() error {
	var missing = -1
	var err error

	missing, err = p.GetMissingCorePods()
	if err != nil {
		return err
	}

	if missing > 0 {
		slog.Info("Resyncing pod to cluster", slog.Int("missing_cores", missing))

		commands := []string{
			"DELETE FROM proxysql_servers",
			"LOAD PROXYSQL SERVERS FROM CONFIG",
			"LOAD PROXYSQL SERVERS TO RUNTIME;",
		}

		for _, command := range commands {
			_, err := p.conn.Exec(command)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

//
// Core mode specific settings
//

type PodInfo struct {
	PodIP    string
	Hostname string
	UID      string
}

// Define a custom type to implement the Sort interface
type ByPodIP []PodInfo

func (a ByPodIP) Len() int           { return len(a) }
func (a ByPodIP) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByPodIP) Less(i, j int) bool { return a[i].PodIP < a[j].PodIP }

func (p *ProxySQL) Core() {
	interval := p.settings.Core.Interval

	slog.Info("Core mode initialized, running loop", slog.Int("interval", interval))

	for {
		p.coreLoop()

		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func (p *ProxySQL) coreLoop() {
	pods, err := GetCorePods(p.settings)
	if err != nil {
		slog.Error("Failed to get pod info", slog.Any("error", err))
		return
	}

	if len(pods) == 0 {
		slog.Error("No pods returned")
		return
	}

	checksumFile := "/tmp/pods-cs.txt"
	digest := calculateChecksum(pods)

	// Read the previous checksum from the file
	old, err := os.ReadFile(checksumFile)
	if err != nil {
		old = []byte("")
	}

	// If nothing changes, we still run LOAD PROXYSQL SERVERS TO RUNTIME in order to accept any
	// new pods that have joined the cluster
	if string(old) == digest {
		command := "LOAD PROXYSQL SERVERS TO RUNTIME"
		_, err = p.conn.Exec(command)
		if err != nil {
			slog.Error("Command failed to execute", slog.String("command", command), slog.Any("error", err))
		}
		return
	}

	commands := createCommands(pods)
	for _, command := range commands {
		_, err = p.conn.Exec(command)
		if err != nil {
			slog.Error("Commands failed", slog.String("commands", command), slog.Any("error", err))
		}
	}

	// Write the new checksum to the file for the next run
	err = os.WriteFile(checksumFile, []byte(digest), 0644)
	if err != nil {
		slog.Error("Failed to write to checksum file", slog.String("file", checksumFile), slog.Any("error", err))
	}

	slog.Info("Commands ran", slog.String("commands", strings.Join(commands, "; ")))
}

func GetCorePods(settings *config) ([]PodInfo, error) {
	app := settings.Core.PodSelector.App
	component := settings.Core.PodSelector.Component

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
		corePods = append(corePods, PodInfo{PodIP: pod.Status.PodIP, Hostname: pod.Name, UID: string(pod.GetUID())})
	}

	return corePods, err
}

func calculateChecksum(pods []PodInfo) string {
	data := []string{}

	for _, pod := range pods {
		data = append(data, fmt.Sprintf("%s:%s:%s", pod.PodIP, pod.Hostname, pod.UID))
	}

	sort.Strings(data)

	return fmt.Sprintf("%x", data)
}

func createCommands(pods []PodInfo) []string {
	sort.Sort(ByPodIP(pods))

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
