package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

// livenessHandler is an HTTP handler function that handles liveness checks for the ProxySQL agent.
// It returns a http.HandlerFunc that can be used to handle HTTP requests.
// The handler checks the liveness of the ProxySQL instance by running probes and returning the results in JSON format.
// If the probes fail, it returns a 503 Service Unavailable status code.
// If the probes pass, it returns a 200 OK status code.
// The livenessHandler also logs the status check result for debugging purposes.
func livenessHandler(psql *ProxySQL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		results, err := psql.RunProbes()
		if err != nil {
			slog.Error("Error in probes()", slog.Any("err", err))

			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, err)
			return
		}

		results.Probe = "liveness"

		resultJSON, err := json.Marshal(results)
		if err != nil {
			fmt.Println("Error marshalling JSON:", err)
			return
		}

		// we want to remain live even during draining, so that we can ensure that the pod
		// isn't killed while there are queries in flight
		if results.Status == "ok" || results.Status == "draining" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}

		fmt.Fprint(w, string(resultJSON))

		slog.Debug("status check", slog.String("json", string(resultJSON)))
	}
}

// readinessHandler is an HTTP request handler function that handles the readiness check endpoint.
// It takes a ProxySQL instance as a parameter and returns an http.HandlerFunc.
// The readiness check endpoint returns the status of the ProxySQL instance and any error encountered during the probe.
// If there is an error, it returns a JSON response with the error message and sets the HTTP status to 503 (Service Unavailable).
// If the status of the ProxySQL instance is "draining", it sets the HTTP status to 503 (Service Unavailable).
// Otherwise, it sets the HTTP status to 200 (OK) and returns a JSON response with the status and probe results.
// Perhaps make use of the proxysql pause command and somehow check to see if it's paused:
//
//	root@proxysql-satellite-9c949fcd7-ldndc:/tmp# mysql -h127.0.0.1 -P6033 -upersona-web-us1 -ppersona-web-us1 -NB -e 'select 1'
//		1
//	root@proxysql-satellite-9c949fcd7-ldndc:/tmp# mysql -e 'proxysql pause' # pause via the admin interface
//	root@proxysql-satellite-9c949fcd7-ldndc:/tmp# mysql -h127.0.0.1 -P6033 -upersona-web-us1 -ppersona-web-us1 -NB -e 'select 1'
//		ERROR 2002 (HY000): Can't connect to MySQL server on '127.0.0.1' (115)
//	root@proxysql-satellite-9c949fcd7-ldndc:/tmp# mysql -e 'proxysql resume' # resume via the admin interface
//	root@proxysql-satellite-9c949fcd7-ldndc:/tmp# mysql -h127.0.0.1 -P6033 -upersona-web-us1 -ppersona-web-us1 -NB -e 'select 1'
//		1
//
// The main caveat here is we'd need the right username, which is apparently hashed in the proxysql db now. I did confirm
// that even if a backend is offline, connections to proxysql are accepted; in other words, unless proxysql is paused
// connections to the serving port with the right creds will succeed.
func readinessHandler(psql *ProxySQL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		results, err := psql.RunProbes()
		if err != nil {
			slog.Error("Error in probes()", slog.Any("err", err))

			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, err)
		}

		results.Probe = "readiness"

		resultJSON, err := json.Marshal(results)
		if err != nil {
			fmt.Println("Error marshalling JSON:", err)
			return
		}

		// we want to remain live even during draining, so that we can ensure that the proxysql container
		// isn't killed while there are transactions in flight
		if results.Status == "draining" {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}

		fmt.Fprint(w, string(resultJSON))

		slog.Debug("status check", slog.String("json", string(resultJSON)))
	}
}

// Run PING() on the proxysql server for core pods; we don't want core pods to go
// unhealthy if there are missing backends. We just want to ensure that proxysql
// is up and listening. This also has the _intended_ side effect of ensuring that
// the mysql connection to the admin port is open.
func startupHandler(psql *ProxySQL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		err := psql.Ping()

		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"message": %s, "status": "unhealthy"}`, err)

			slog.Error("Error in pingHandler()", slog.Any("err", err))
		} else {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"message": "ok", "status": "ok"}`)
		}
	}
}

// StartAPI starts the HTTP server for the ProxySQL agent.
// It registers the necessary handlers for health checks and starts listening on the specified port.
// The function panics if there is an error starting the server.
func StartAPI(p *ProxySQL) {
	http.HandleFunc("/healthz/started", startupHandler(p))
	http.HandleFunc("/healthz/ready", readinessHandler(p))
	http.HandleFunc("/healthz/live", livenessHandler(p))

	// FIXME: make this configurable
	port := ":8080"

	slog.Info("Starting HTTP server", slog.String("port", port))
	if err := http.ListenAndServe(port, nil); err != nil {
		slog.Error("Error starting the HTTP server", slog.Any("err", err))

		panic(err)
	}
}
