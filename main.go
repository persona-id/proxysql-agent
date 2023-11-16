package main

import (
	"log/slog"
	"os"
	"time"
)

func main() {
	Configure()

	setupLogger()

	slog.Info("configured values", slog.Any("config", Config))

	// if defined, pause before booting; this allows the proxysql pods to fully come up before connecting
	if Config.StartDelay > 0 {
		slog.Info("Pausing before boot", slog.Int("seconds", Config.StartDelay))
		time.Sleep(time.Duration(Config.StartDelay) * time.Second)
	}

	// open a connection to proxysql
	var psql *ProxySQL
	psql, err := psql.New()
	if err != nil {
		slog.Error("Unable to connect to ProxySQL", slog.Any("error", err))
		panic(err)
	}

	// run the process in either core or satellite mode; each of these is a for {} loop,
	// so it will block the process from exiting
	mode := Config.RunMode
	if mode == "core" {
		psql.Core()
	} else if mode == "satellite" {
		psql.Satellite()
	}
}

func setupLogger() {
	var level slog.Level

	switch Config.LogLevel {
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
