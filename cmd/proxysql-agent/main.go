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

	"github.com/lmittmann/tint"
)

var (
	// Version will be the version tag if the binary is built with "go install url/tool@version".
	// See https://goreleaser.com/cookbooks/using-main.version/
	// Current git tag.
	version = "unknown" //nolint:gochecknoglobals
	// Current git commit sha.
	commit = "unknown" //nolint:gochecknoglobals
	// Built at date.
	date = "unknown" //nolint:gochecknoglobals
)

func main() {
	settings, err := configuration.Configure()
	if err != nil {
		slog.Error("Error in Configure()", slog.Any("err", err))
		os.Exit(1)
	}

	setupLogger(settings)

	slog.Info("build info", slog.Any("version", version), slog.Any("committed", date), slog.Any("revision", commit))

	// if defined, pause before booting; this allows the proxysql containers to fully come up before the agent tries
	// connecting; sometimes the proxysql container can take a few seconds to fully start. This is mainly only
	// an issue when booting into core or satellite mode; any other commands that might be run ad hoc should be
	// fine
	if settings.StartDelay > 0 {
		slog.Info("Pausing before boot", slog.Int("seconds", settings.StartDelay))
		time.Sleep(time.Duration(settings.StartDelay) * time.Second)
	}

	var psql *proxysql.ProxySQL

	psql, err = psql.New(settings)
	if err != nil {
		slog.Error("Unable to connect to ProxySQL", slog.Any("error", err))
		panic(err)
	}

	// Set up signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigChan
		slog.Info("Received signal, initiating graceful shutdown", slog.String("signal", sig.String()))
		cancel()
	}()

	// run the process in either core or satellite mode; each of these is a for {} loop,
	// so it will block the process from exiting
	switch settings.RunMode {
	case "core":
		server := restapi.StartAPI(psql) // start the http api
		psql.SetHTTPServer(server)
		psql.Core(ctx)
	case "satellite":
		server := restapi.StartAPI(psql) // start the http api
		psql.SetHTTPServer(server)
		psql.Satellite(ctx)
	case "dump":
		psql.DumpData()
	default:
		slog.Info("No run mode specified, exiting")
	}
}

func setupLogger(settings *configuration.Config) {
	var level slog.Level

	switch settings.Log.Level {
	case "DEBUG":
		level = slog.LevelDebug
	case "INFO":
		level = slog.LevelInfo
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	handler := tint.NewHandler(os.Stdout, &tint.Options{
		AddSource:   false,
		Level:       level,
		TimeFormat:  time.RFC3339,
		NoColor:     false,
		ReplaceAttr: nil,
	})

	if settings.Log.Format == "JSON" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			AddSource:   false,
			Level:       level,
			ReplaceAttr: nil,
		})
	}

	logger := slog.New(handler)

	slog.SetDefault(logger)
}
