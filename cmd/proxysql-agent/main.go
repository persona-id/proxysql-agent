package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"time"

	"github.com/persona-id/proxysql-agent/internal/configuration"
	"github.com/persona-id/proxysql-agent/internal/proxysql"
	"github.com/persona-id/proxysql-agent/internal/restapi"
)

var (
	// Version will be the version tag if the binary is built with "go install url/tool@version".
	// If the binary is built some other way, it will be "(devel)".
	Version = "unknown" //nolint:gochecknoglobals
	// Revision is taken from the vcs.revision tag in Go 1.18+.
	Revision = "unknown" //nolint:gochecknoglobals
	// LastCommit is taken from the vcs.time tag in Go 1.18+.
	LastCommit time.Time //nolint:gochecknoglobals
	// DirtyBuild is taken from the vcs.modified tag in Go 1.18+.
	DirtyBuild = true //nolint:gochecknoglobals
)

func getVersionInfo() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}

	for _, kv := range info.Settings {
		if kv.Value == "" {
			continue
		}

		fmt.Println(kv)

		switch kv.Key {
		case "vcs.revision":
			Revision = kv.Value
		case "vcs.time":
			LastCommit, _ = time.Parse(time.RFC3339, kv.Value)
		case "vcs.modified":
			DirtyBuild = kv.Value == "true"
		}
	}
}

func main() {
	getVersionInfo()

	settings, err := configuration.Configure()
	if err != nil {
		slog.Error("Error in Configure()", slog.Any("err", err))
		os.Exit(1)
	}

	setupLogger(settings)

	slog.Info("build info", slog.Any("version", Version), slog.Any("committed", LastCommit), slog.Any("revision", Revision), slog.Any("dirty", DirtyBuild))

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
