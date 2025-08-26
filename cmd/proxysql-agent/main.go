package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/persona-id/proxysql-agent/internal/configuration"
	"github.com/persona-id/proxysql-agent/internal/proxysql"
	"github.com/persona-id/proxysql-agent/internal/restapi"
)

func main() {
	settings, err := configuration.Configure()
	if err != nil {
		slog.Error("error in Configure()", slog.Any("error", err))
		os.Exit(1)
	}

	// if defined, pause before booting; this allows the proxysql containers to fully come up before the agent tries
	// connecting; sometimes the proxysql container can take a few seconds to fully start. This is mainly only
	// an issue when booting into core or satellite mode; any other commands that might be run ad hoc should be
	// fine
	if settings.StartDelay > 0 {
		slog.Info("pausing before boot", slog.Int("seconds", settings.StartDelay))
		time.Sleep(time.Duration(settings.StartDelay) * time.Second)
	}

	var psql *proxysql.ProxySQL

	psql, err = psql.New(settings)
	if err != nil {
		slog.Error("unable to connect to ProxySQL", slog.Any("error", err))
		panic(err)
	}

	// Set up signal handling for the graceful shutdown and usr{1,2} signals.
	ctx, cancel := context.WithCancel(context.Background())
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGUSR1, syscall.SIGUSR2)

	go func() {
		for {
			sig := <-sigChan
			switch sig {
			case syscall.SIGTERM, syscall.SIGINT:
				slog.Info("received signal, initiating graceful shutdown", slog.String("signal", sig.String()))
				cancel()

				return
			case syscall.SIGUSR1:
				handleSIGUSR1(psql)

			case syscall.SIGUSR2:
				handleSIGUSR2(psql)
			}
		}
	}()

	// run the process in either core or satellite mode; each of these is a for {} loop,
	// so it will block the process from exiting
	switch settings.RunMode {
	case "core":
		// start the http api
		server := restapi.StartAPI(psql, settings)
		psql.SetHTTPServer(server)

		// fire off the core loop
		err := psql.Core(ctx)
		if err != nil {
			slog.Error("caught error in core loop", slog.Any("error", err))
		}

		// Wait for shutdown to complete
		slog.Info("main: core loop completed, process exiting")

	case "satellite":
		// start the http api
		server := restapi.StartAPI(psql, settings)
		psql.SetHTTPServer(server)

		// fire off the satellite loop
		err := psql.Satellite(ctx)
		if err != nil {
			slog.Error("caught error in satellite loop", slog.Any("error", err))
		}

		// Wait for shutdown to complete
		slog.Info("main: satellite loop completed, process exiting")

	case "dump":
		psql.DumpData(ctx)

	default:
		slog.Info("no run mode specified, exiting")
	}
}

// handleSIGUSR1 handles SIGUSR1 signal - intended for dumping ProxySQL server info to STDOUT.
// Thoughts:
// - select * from runtime_proxysql_servers (and maybe proxysql_servers)
// - select * from runtime_mysql_servers (to see what's shunned, if anything)
// - various stats tables maybe?
func handleSIGUSR1(p *proxysql.ProxySQL) {
	results, err := p.RunProbes(context.Background())
	if err != nil {
		slog.Error("error running probes", slog.Any("error", err))

		return
	}

	// TODO(kuzmik): dump proxysql servers and maybe other relevant data.

	results.Probe = "SIGUSR1"

	slog.Info("signal received",
		slog.String("signal", "SIGUSR1"),
		slog.Group("probe",
			slog.String("probe", results.Probe),
			slog.String("status", results.Status),
			slog.String("message", results.Message),
			slog.Bool("draining", results.Draining),
			slog.Int("clients.connected", results.Clients),
			slog.Int("backends.total", results.Backends.Total),
			slog.Int("backends.online", results.Backends.Online),
			slog.Int("backends.shunned", results.Backends.Shunned),
		),
	)
}

// handleSIGUSR2 handles SIGUSR2 signal - intended for config reload or resync.
func handleSIGUSR2(_ *proxysql.ProxySQL) {
	// TODO(kuzmik): trigger a config reload and cluster resync
	slog.Info("signal received",
		slog.String("signal", "SIGUSR2"),
	)
}
