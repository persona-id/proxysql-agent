package restapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/persona-id/proxysql-agent/internal/configuration"
	"github.com/persona-id/proxysql-agent/internal/proxysql"
)

// StartAPI starts the HTTP server for the ProxySQL agent.
// It registers the necessary handlers for health checks and starts listening on the specified port.
// Returns the server instance for graceful shutdown.
func StartAPI(p *proxysql.ProxySQL, settings *configuration.Config) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz/started", startupHandler(p, settings))
	mux.HandleFunc("/healthz/ready", readinessHandler(p, settings))
	mux.HandleFunc("/healthz/live", livenessHandler(p, settings))
	mux.HandleFunc("/shutdown", preStopHandler(p, settings))

	port := fmt.Sprintf(":%d", settings.API.Port)

	// Create a server with reasonable timeouts
	server := &http.Server{
		Addr:              port,
		Handler:           mux,
		ReadTimeout:       10 * time.Second, //nolint:mnd
		WriteTimeout:      10 * time.Second, //nolint:mnd
		IdleTimeout:       30 * time.Second, //nolint:mnd
		ReadHeaderTimeout: 5 * time.Second,  //nolint:mnd
	}

	slog.Info("Starting HTTP server", slog.String("port", port))

	go func() {
		// disabling this semgrep rule here because it's an internal API only accessible inside the pod itself
		// nosemgrep: go.lang.security.audit.net.use-tls.use-tls
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Error starting the HTTP server", slog.Any("err", err))
			panic(err)
		}
	}()

	return server
}

// livenessHandler is an HTTP handler function that handles liveness checks for the ProxySQL agent.
// It returns a http.HandlerFunc that can be used to handle HTTP requests.
// The handler checks the liveness of the ProxySQL instance by running probes and returning the results in JSON format.
// If the probes fail, it returns a 503 Service Unavailable status code.
// If the probes pass, it returns a 200 OK status code.
// The livenessHandler also logs the status check result for debugging purposes, if log.probes is true.
func livenessHandler(psql *proxysql.ProxySQL, settings *configuration.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// During shutdown, avoid running probes that might fail
		if psql.IsShuttingDown() {
			result := map[string]any{
				"status":   "draining",
				"message":  "shutting down",
				"probe":    "liveness",
				"draining": true,
			}

			resultJSON, err := json.Marshal(result)
			if err != nil {
				slog.Error("Error marshalling JSON", slog.Any("err", err))
				w.WriteHeader(http.StatusInternalServerError)

				return
			}

			w.WriteHeader(http.StatusOK) // Keep liveness OK during draining
			fmt.Fprint(w, string(resultJSON))

			return
		}

		results, err := psql.RunProbes(r.Context())
		if err != nil {
			slog.Error("Error in probes()", slog.Any("err", err))

			w.WriteHeader(http.StatusServiceUnavailable)

			// nosemgrep: go.lang.security.audit.xss.no-fprintf-to-responsewriter.no-fprintf-to-responsewriter
			fmt.Fprint(w, err)

			return
		}

		results.Probe = "liveness"

		resultJSON, err := json.Marshal(results)
		if err != nil {
			slog.Error("Error marshalling JSON", slog.Any("err", err))

			return
		}

		// we want to remain live even during draining, so that we can ensure that the pod
		// isn't killed while there are queries in flight
		if results.Status == "ok" || results.Status == "draining" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}

		// nosemgrep: go.lang.security.audit.xss.no-fprintf-to-responsewriter.no-fprintf-to-responsewriter
		fmt.Fprint(w, string(resultJSON))

		if settings.Log.Probes {
			slog.Debug("status check", slog.Any("results", results))
		}
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
func readinessHandler(psql *proxysql.ProxySQL, settings *configuration.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// During shutdown, mark as not ready
		if psql.IsShuttingDown() {
			result := map[string]any{
				"status":   "draining",
				"message":  "shutting down",
				"probe":    "readiness",
				"draining": true,
			}

			resultJSON, err := json.Marshal(result)
			if err != nil {
				slog.Error("Error marshalling JSON", slog.Any("err", err))
				w.WriteHeader(http.StatusInternalServerError)

				return
			}

			w.WriteHeader(http.StatusServiceUnavailable) // Not ready during shutdown
			fmt.Fprint(w, string(resultJSON))

			return
		}

		results, err := psql.RunProbes(r.Context())
		if err != nil {
			slog.Error("Error in probes()", slog.Any("err", err))

			w.WriteHeader(http.StatusServiceUnavailable)

			// nosemgrep: go.lang.security.audit.xss.no-fprintf-to-responsewriter.no-fprintf-to-responsewriter
			fmt.Fprint(w, err)

			return
		}

		results.Probe = "readiness"

		resultJSON, err := json.Marshal(results)
		if err != nil {
			slog.Error("Error marshaling json", slog.Any("err", err))

			return
		}

		// we want to remain live even during draining, so that we can ensure that the proxysql container
		// isn't killed while there are transactions in flight
		if results.Status == "draining" {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}

		// nosemgrep:go.lang.security.audit.xss.no-fprintf-to-responsewriter.no-fprintf-to-responsewriter
		fmt.Fprint(w, string(resultJSON))

		// only log the probe results if the setting is enabled AND debug mode is enabled
		if settings.Log.Probes {
			slog.Debug("status check", slog.Any("results", results))
		}
	}
}

// Run PING() on the proxysql server for core pods; we don't want core pods to go
// unhealthy if there are missing backends. We just want to ensure that proxysql
// is up and listening. This also has the _intended_ side effect of ensuring that
// the mysql connection to the admin port is open.
func startupHandler(psql *proxysql.ProxySQL, _ *configuration.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		err := psql.Ping(context.Background()) //nolint:contextcheck
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)

			// nosemgrep: go.lang.security.audit.xss.no-fprintf-to-responsewriter.no-fprintf-to-responsewriter
			fmt.Fprintf(w, `{"message": %s, "status": "unhealthy"}`, err)

			slog.Error("Error in startupHandler()", slog.Any("err", err))
		} else {
			w.WriteHeader(http.StatusOK)

			// nosemgrep: go.lang.security.audit.xss.no-fprintf-to-responsewriter.no-fprintf-to-responsewriter
			fmt.Fprint(w, `{"message": "ok", "status": "ok"}`)
		}
	}
}

// preStopHandler is an HTTP request handler function that handles the prestop shutdown endpoint.
func preStopHandler(psql *proxysql.ProxySQL, _ *configuration.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		// we don't want to block the pod from shutting down if the shutdown fails
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) //nolint:mnd
		defer cancel()

		err := psql.PreStopShutdown(ctx) //nolint:contextcheck
		if err != nil {
			// Log error but still return success since this is a prestop hook
			slog.Error("prestop shutdown failed", slog.Any("error", err))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)

			// nosemgrep: go.lang.security.audit.xss.no-fprintf-to-responsewriter.no-fprintf-to-responsewriter
			fmt.Fprint(w, `{"message": "shutdown failed", "status": "error"}`)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// nosemgrep: go.lang.security.audit.xss.no-fprintf-to-responsewriter.no-fprintf-to-responsewriter
		fmt.Fprint(w, `{"message": "shutdown initiated", "status": "ok"}`)
	}
}
