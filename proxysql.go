package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
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
		var hostgroup, port int
		var hostname string

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

		err = p.Ping()
		if err != nil {
			slog.Error("Error calling Ping()", slog.Any("error", err))
		}

		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func (p *ProxySQL) GetMissingCorePods() (int, error) {
	count := -1

	query := `SELECT COUNT(hostname)
			FROM stats_proxysql_servers_metrics
			WHERE last_check_ms > 30000
			AND hostname != 'proxysql-core'
			AND Uptime_s > 0`
	row := p.conn.QueryRow(query)

	err := row.Scan(&count)
	if err != nil {
		return count, err
	}

	return count, nil
}

func (p *ProxySQL) SatelliteResync() error {
	missing, err := p.GetMissingCorePods()
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

// data we eventually want to load into snowflake
//  1. stats_mysql_query_digests (maybe use _reset to reset the state)
//  2. mysql_query_rules
//  3. stats_mysql_query_rules
//
// FIXME: all these functions dump to ./tmp/X.csv; we want the directory to be configurable at least.
func (p *ProxySQL) DumpData() {
	tmpdir, _ := os.MkdirTemp("/tmp", "")

	digestsFile, err := p.DumpQueryDigests(tmpdir)
	if err != nil {
		slog.Error("Error in DumpQueryDigests()", slog.Any("error", err))
	} else if digestsFile != "" {
		slog.Info("Saved mysql query digests to file", slog.String("filename", digestsFile))
	}

	rulesFile, err := p.DumpQueryRules(tmpdir)
	if err != nil {
		slog.Error("Error in DumpQueryRules()", slog.Any("error", err))
	} else if rulesFile != "" {
		slog.Info("Saved mysql query rules to file", slog.String("filename", rulesFile))
	}

	rulesStatsFile, err := p.DumpQueryRuleStats(tmpdir)
	if err != nil {
		slog.Error("Error in DumpQueryRuleStats()", slog.Any("error", err))
	} else if rulesStatsFile != "" {
		slog.Info("Saved mysql query rules stats to file", slog.String("filename", rulesStatsFile))
	}
}

// ProxySQL docs: https://proxysql.com/documentation/stats-statistics/#stats_mysql_query_digest
func (p *ProxySQL) DumpQueryDigests(tmpdir string) (string, error) {
	var rowCount int
	err := p.conn.QueryRow("SELECT COUNT(*) FROM stats_mysql_query_digest").Scan(&rowCount)

	if err != nil {
		return "", err
	}

	// Don't proceed with this function if there are no entries in the table
	if rowCount <= 0 {
		slog.Debug("No query digests in the log, not proceeding with DumpQueryDigests()")

		return "", nil
	}

	hostname, err := os.Hostname()
	if err != nil {
		// os.Hostname didn't work for some reason, so try to get the hostname from the ENV
		hostname = os.Getenv("HOSTNAME")
		if hostname == "" {
			// that didn't work either, so something is really wrong
			return "", err
		}
	}

	dumpFile := fmt.Sprintf("%s/%s-digests.csv", tmpdir, hostname)
	file, err := os.Create(dumpFile)

	if err != nil {
		return "", err
	}

	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	header := []string{
		"pod_name",
		"hostgroup",
		"username",
		"digest",
		"digest_text",
		"count_star",
		"first_seen",
		"last_seen",
		"sum_time_us",
		"min_time_us",
		"max_time",
		"sum_rows_affected",
		"sum_rows_sent",
	}

	if err := writer.Write(header); err != nil {
		return "", err
	}

	rows, err := p.conn.Query("SELECT * FROM stats_mysql_query_digest")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	for rows.Next() {
		var hostgroup int
		var schemaname, username, clientAddress, digest, digestText string
		var countStar, firstSeen, lastSeen, sumTime, minTime, maxTime, sumRowsAffected, sumRowsSent int

		err := rows.Scan(&hostgroup, &schemaname, &username, &clientAddress, &digest, &digestText, &countStar,
			&firstSeen, &lastSeen, &sumTime, &minTime, &maxTime, &sumRowsAffected, &sumRowsSent)
		if err != nil {
			return "", err
		}

		// Create a slice with the values
		values := []string{
			hostname,
			strconv.Itoa(hostgroup),
			schemaname,
			username,
			digest,
			`"` + digestText + `"`, // Quote the digest_text field to handle commas
			strconv.Itoa(countStar),
			time.Unix(int64(firstSeen), 0).String(),
			time.Unix(int64(lastSeen), 0).String(),
			strconv.Itoa(sumTime),
			strconv.Itoa(minTime),
			strconv.Itoa(maxTime),
			strconv.Itoa(sumRowsAffected),
			strconv.Itoa(sumRowsSent),
		}

		// Write the values to the CSV file
		if err := writer.Write(values); err != nil {
			return "", err
		}
	}

	return dumpFile, nil
}

// ProxySQL docs: https://proxysql.com/documentation/main-runtime/#mysql_query_rules
func (p *ProxySQL) DumpQueryRules(tmpdir string) (string, error) {
	var rowCount int

	err := p.conn.QueryRow("SELECT COUNT(*) FROM mysql_query_rules").Scan(&rowCount)
	if err != nil {

		return "", err
	}

	// Don't proceed with this function if there are no query rules
	if rowCount <= 0 {
		slog.Debug("No query rules defined, not proceeding with DumpQueryRules()")

		return "", nil
	}

	hostname, err := os.Hostname()
	if err != nil {
		// os.Hostname didn't work for some reason, so try to get the hostname from the ENV
		hostname = os.Getenv("HOSTNAME")
		if hostname == "" {
			// that didn't work either, so something is really wrong
			return "", err
		}
	}

	dumpFile := fmt.Sprintf("%s/%s-rules.csv", tmpdir, hostname)

	file, err := os.Create(dumpFile)
	if err != nil {
		return "", err
	}

	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	header := []string{
		"rule_id",
		"active",
		"username",
		"schemaname",
		"flagIN",
		"client_addr",
		"proxy_addr",
		"proxy_port",
		"digest",
		"match_digest",
		"match_pattern",
		"negate_match_pattern",
		"re_modifiers",
		"flagOUT",
		"replace_pattern",
		"destination_hostgroup",
		"cache_ttl",
		"cache_empty_result",
		"cache_timeout",
		"reconnect",
		"timeout",
		"retries",
		"delay",
		"next_query_flagIN",
		"mirror_flagOUT",
		"mirror_hostgroup",
		"error_msg",
		"OK_msg",
		"sticky_conn",
		"multiplex",
		"gtid_from_hostgroup",
		"log",
		"apply",
		"attributes",
		"comment",
	}

	if err := writer.Write(header); err != nil {
		return "", err
	}

	rows, err := p.conn.Query("SELECT * FROM mysql_query_rules")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	for rows.Next() {
		var ruleID, active, flagIN, proxyPort, negateMatchPattern, flagOUT, destinationHostgroup, cacheTTL,
			cacheEmptyResult, cacheTimeout, reconnect, timeout, retries, delay, nextQueryFlagIN, mirrorFlagOUT,
			mirrorHostgroup, stickyConn, multiplex, gtidFromHostgroup, log, apply sql.NullInt64
		var username, schemaname, clientAddr, proxyAddr, digest, matchDigest, matchPattern, reModifiers,
			replacePatternStr, errorMsg, okMsg, attributes, comment sql.NullString

		err := rows.Scan(
			&ruleID, &active, &username, &schemaname, &flagIN, &clientAddr, &proxyAddr, &proxyPort,
			&digest, &matchDigest, &matchPattern, &negateMatchPattern, &reModifiers, &flagOUT,
			&replacePatternStr, &destinationHostgroup, &cacheTTL, &cacheEmptyResult, &cacheTimeout,
			&reconnect, &timeout, &retries, &delay, &nextQueryFlagIN, &mirrorFlagOUT, &mirrorHostgroup,
			&errorMsg, &okMsg, &stickyConn, &multiplex, &gtidFromHostgroup, &log, &apply, &attributes, &comment,
		)
		if err != nil {
			return "", err
		}

		// Create a slice with the values
		values := []string{
			strconv.Itoa(int(ruleID.Int64)),
			strconv.Itoa(int(active.Int64)),
			username.String,
			schemaname.String,
			strconv.Itoa(int(flagIN.Int64)),
			clientAddr.String,
			proxyAddr.String,
			strconv.Itoa(int(proxyPort.Int64)),
			digest.String,
			matchDigest.String,
			matchPattern.String,
			strconv.Itoa(int(negateMatchPattern.Int64)),
			reModifiers.String,
			strconv.Itoa(int(flagOUT.Int64)),
			replacePatternStr.String,
			strconv.Itoa(int(destinationHostgroup.Int64)),
			strconv.Itoa(int(cacheTTL.Int64)),
			strconv.Itoa(int(cacheEmptyResult.Int64)),
			strconv.Itoa(int(cacheTimeout.Int64)),
			strconv.Itoa(int(reconnect.Int64)),
			strconv.Itoa(int(timeout.Int64)),
			strconv.Itoa(int(retries.Int64)),
			strconv.Itoa(int(delay.Int64)),
			strconv.Itoa(int(nextQueryFlagIN.Int64)),
			strconv.Itoa(int(mirrorFlagOUT.Int64)),
			strconv.Itoa(int(mirrorHostgroup.Int64)),
			errorMsg.String,
			okMsg.String,
			strconv.Itoa(int(stickyConn.Int64)),
			strconv.Itoa(int(multiplex.Int64)),
			strconv.Itoa(int(gtidFromHostgroup.Int64)),
			strconv.Itoa(int(log.Int64)),
			strconv.Itoa(int(apply.Int64)),
			attributes.String,
			comment.String,
		}

		if err := writer.Write(values); err != nil {
			return "", err
		}
	}

	return dumpFile, nil
}

// ProxySQL docs: https://proxysql.com/documentation/stats-statistics/#stats_mysql_query_rules
func (p *ProxySQL) DumpQueryRuleStats(tmpdir string) (string, error) {
	var rowCount int

	err := p.conn.QueryRow("SELECT COUNT(*) FROM stats_mysql_query_rules").Scan(&rowCount)
	if err != nil {
		return "", err
	}

	// Don't proceed with this function if there are no query rules
	if rowCount <= 0 {
		slog.Debug("No query rules stats, not proceeding with DumpQueryRuleStats()")

		return "", nil
	}

	hostname, err := os.Hostname()
	if err != nil {
		// os.Hostname didn't work for some reason, so try to get the hostname from the ENV
		hostname = os.Getenv("HOSTNAME")
		if hostname == "" {
			// that didn't work either, so something is really wrong
			return "", err
		}
	}

	dumpFile := fmt.Sprintf("%s/%s-rule-stats.csv", tmpdir, hostname)

	file, err := os.Create(dumpFile)
	if err != nil {
		return "", err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	header := []string{"rule_id", "hits"}

	if err := writer.Write(header); err != nil {
		return "", err
	}

	rows, err := p.conn.Query("SELECT * FROM stats_mysql_query_rules")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	for rows.Next() {
		var ruleID, hits int

		err := rows.Scan(&ruleID, &hits)
		if err != nil {
			return "", err
		}

		// Create a slice with the values
		values := []string{
			strconv.Itoa(ruleID),
			strconv.Itoa(hits),
		}

		if err := writer.Write(values); err != nil {
			return "", err
		}
	}

	return dumpFile, nil
}

//
// Core mode specific settings
//

type PodInfo struct {
	PodIP    string
	Hostname string
	UID      string
}

// Define a custom type to implement the Sort interface.
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
	err = os.WriteFile(checksumFile, []byte(digest), 0o600)
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
		commands = append(commands,
			fmt.Sprintf("INSERT INTO proxysql_servers VALUES ('%s', 6032, 0, '%s')", pod.PodIP, pod.Hostname),
		)
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
