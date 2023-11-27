package main

import (
	"fmt"
	"log/slog"
	"net/http"
)

// k8s probes; we might want to make these more granular
// curl http://localhost:8080/status
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
			msg := fmt.Sprintf(`{"status": "healthy", "message": "all backends online and healthy", "backends": {"total": %d, "online": %d}}`, total, online)

			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, msg)

			slog.Debug("status check", slog.String("json", msg))
		} else if online > 0 {
			// some backends are offline in some way, so we're healthy but it's not great
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

func StartAPI(p *ProxySQL) {
	http.HandleFunc("/status", statusHandler(p))

	// FIXME: make this configurable
	port := ":8080"

	slog.Info("Starting HTTP server", slog.String("port", port))
	if err := http.ListenAndServe(port, nil); err != nil {
		slog.Error("Error starting the HTTP server", slog.Any("err", err))

		// goroutines can't easily return values, so let's just panic here
		panic(err)
	}
}
