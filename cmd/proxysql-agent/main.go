package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/kuzmik/proxysql-agent/internal/configuration"
	"github.com/kuzmik/proxysql-agent/internal/proxysql"
	"github.com/kuzmik/proxysql-agent/internal/restapi"
)

var (
	version = "dev"     //nolint:gochecknoglobals
	commit  = "none"    //nolint:gochecknoglobals
	date    = "unknown" //nolint:gochecknoglobals
)

func main() {
	settings, err := configuration.Configure()
	if err != nil {
		slog.Error("Error in Configure()", slog.Any("err", err))
		os.Exit(1)
	}

	setupLogger(settings)

	slog.Info("build info", slog.Any("version", version), slog.Any("date", date), slog.Any("commit", commit))

	// if defined, pause before booting; this allows the proxysql pods to fully come up before the agent tries
	// connecting. Sometimes the proxysql container takes a few seconds to fully start. This is mainly only
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

	// run the process in either core or satellite mode; each of these is a for {} loop,
	// so it will block the process from exiting
	switch settings.RunMode {
	case "core":
		go restapi.StartAPI(psql) // start the http api
		psql.Core()
	case "satellite":
		go restapi.StartAPI(psql) // start the http api
		psql.Satellite()
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

	opts := &slog.HandlerOptions{
		AddSource: false,
		Level:     level,
	}

	var handler slog.Handler = slog.NewTextHandler(os.Stdout, opts)
	if settings.Log.Format == "JSON" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	logger := slog.New(handler)

	slog.SetDefault(logger)
}
