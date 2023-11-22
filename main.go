package main

import (
	"log/slog"
	"os"
	"time"
)

var (
	// compile time info.
	Build     = ""
	BuildTime = ""
	Version   = ""
)

func main() {
	settings, err := Configure()
	if err != nil {
		panic(err)
	}

	setupLogger(settings)

	slog.Info("build info", slog.Any("version", Version), slog.Any("time", BuildTime), slog.Any("build", Build))

	// if defined, pause before booting; this allows the proxysql pods to fully come up before the agent tries
	// connecting. Sometimes the proxysql container takes a a few seconds to fully start. This is mainly only
	// an issue when booting into core or satellite mode; any other commands that might be run ad hoc should be
	// fine
	if settings.StartDelay > 0 {
		slog.Info("Pausing before boot", slog.Int("seconds", settings.StartDelay))
		time.Sleep(time.Duration(settings.StartDelay) * time.Second)
	}

	var psql *ProxySQL

	psql, err = psql.New(settings)
	if err != nil {
		slog.Error("Unable to connect to ProxySQL", slog.Any("error", err))
		panic(err)
	}

	// run the process in either core or satellite mode; each of these is a for {} loop,
	// so it will block the process from exiting
	switch settings.RunMode {
	case "core":
		psql.Core()
	case "satellite":
		psql.Satellite()
	case "dump":
		psql.DumpData()
	default:
		slog.Info("No run mode specified, exiting")
	}
}

func setupLogger(settings *config) {
	var level slog.Level

	switch settings.LogLevel {
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
	// if appEnv == "production" {
	//     handler = slog.NewJSONHandler(os.Stdout, opts)
	// }

	logger := slog.New(handler)

	slog.SetDefault(logger)
}
