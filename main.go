package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/persona-id/proxysql-agent/proxysql"
	"github.com/spf13/viper"
)

var (
	logger *slog.Logger
	psql   *proxysql.ProxySQL
)

func main() {
	Configure()

	setupLogger()

	logger.Info("dump", slog.Any("dump", Config))

	// if defined, pause before booting; this allows the proxysql pods to fully come up before connecting
	if Config.StartDelay > 0 {
		logger.Info("Pausing before boot", slog.Int("seconds", Config.StartDelay))
		time.Sleep(time.Duration(Config.StartDelay) * time.Second)
	}

	// open a connection to proxysql
	var err error
	psql, err = proxysql.New()
	if err != nil {
		logger.Error("Unable to connect to ProxySQL", slog.Any("error", err))
		panic(err)
	}

	// run the process in either core or satellite mode; each of these is a for {} loop,
	// so it will block the process from exiting
	mode := viper.GetViper().GetString("run_mode")
	if mode == "core" {
		psql.Core()
	} else if mode == "satellite" {
		psql.Satellite()
	}
}

func setupLogger() {
	var level slog.Level

	switch viper.GetViper().GetString("log_level") {
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

	logger = slog.New(handler)

	slog.SetDefault(logger)
}
