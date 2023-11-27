package main

import (
	"fmt"
	"log/slog"
	"net/http"
)

// k8s probes
// call: curl http://localhost:8080/healthz
func statusHandler(psql *ProxySQL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		total, online, err := psql.RunProbes()
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"message": %s, "status": "unhealthy"}`, err)

			slog.Error("Error in RunProbes()", slog.Any("err", err))
		}

		if online == total {
			// all backends are up and running, we're totally healthy
			msg := fmt.Sprintf(`{"status": "ok", "message": "all backends online and healthy", "backends": {"total": %d, "online": %d}}`, total, online)

			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, msg)

			slog.Debug("status check", slog.String("json", msg))
		} else if online > 0 {
			// some backends are offline in some way, so while we're healthy, it's not perfect
			msg := fmt.Sprintf(`{"status": "degraded", "message": "some backends are shunned", "backends": {"total": %d, "online": %d}}`, total, online)

			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, msg)

			slog.Debug("status check", slog.String("json", msg))
		} else {
			// all backends are offline in some way
			msg := fmt.Sprintf(`{"status": "unhealthy", "message": "no healthy backends", "backends": {"total": %d, "online": %d}}`, total, online)

			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, msg)

			slog.Debug("status check", slog.String("json", msg))
		}
	}
}

// Run PING() on the proxysql server for core pods; we don't want core pods to go
// unhealthy if there are missing backends. We just want to ensure that proxysql
// is up and listening.
// call: curl http://localhost:8080/healthz/ping
func pingHandler(psql *ProxySQL) http.HandlerFunc {
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

func StartAPI(p *ProxySQL) {
	http.HandleFunc("/healthz", statusHandler(p))

	http.HandleFunc("/healthz/ping", pingHandler(p))

	// FIXME: make this configurable
	port := ":8080"

	slog.Info("Starting HTTP server", slog.String("port", port))
	if err := http.ListenAndServe(port, nil); err != nil {
		slog.Error("Error starting the HTTP server", slog.Any("err", err))

		// goroutines can't easily return values, so let's just panic here
		panic(err)
	}
}
