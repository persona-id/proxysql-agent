package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/persona-id/proxysql-agent/proxysql"
	"github.com/rs/zerolog"
)

var (
	logger            zerolog.Logger
	psql              *proxysql.ProxySQL
	coreModeFlag      bool
	satelliteModeFlag bool
	userFlag          string
	passwordFlag      string
	addressFlag       string
	pauseFlag         int
)

// TODO: support either a config file, env vars, or commandline options
func main() {
	setupLogger()

	userEnv := os.Getenv("PROXYSQL_USER")
	passwordEnv := os.Getenv("PROXYSQL_PASSWORD")
	addressEnv := os.Getenv("PROXYSQL_ADDRESS")

	// If environment variables are not set, use command line arguments
	flag.StringVar(&userFlag, "user", userEnv, "ProxySQL username")
	flag.StringVar(&passwordFlag, "password", passwordEnv, "ProxySQL password")
	flag.StringVar(&addressFlag, "address", addressEnv, "ProxySQL address")

	flag.IntVar(&pauseFlag, "pause", 0, "Seconds to pause before attempting to start")

	flag.BoolVar(&coreModeFlag, "core", false, "Run the functions required for core pods")
	flag.BoolVar(&satelliteModeFlag, "satellite", false, "Run the functions required for satellite pods")

	flag.Parse()

	// If command line arguments are not set, use default values
	if userFlag == "" {
		userFlag = "radmin"
	}
	if passwordFlag == "" {
		passwordFlag = "radmin"
	}
	if addressFlag == "" {
		addressFlag = "127.0.0.1:6032"
	}

	logger.Debug().
		Str("address", addressFlag).
		Str("username", userFlag).
		Str("password", "************").
		Msg("ProxySQL configuration")

	if pauseFlag > 0 {
		logger.Info().Int("seconds", pauseFlag).Msg("Pausing before boot")
		time.Sleep(time.Duration(pauseFlag) * time.Second)
	}

	setupProxySQL()

	if coreModeFlag {
		go psql.Core()
	} else if satelliteModeFlag {
		go psql.Satellite()
	}

	for {
		// just loop, i guess. maybe select {} is the right choice here?
	}
}

func setupLogger() {
	logger = zerolog.New(
		zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339},
	).Level(zerolog.TraceLevel).With().Timestamp().Caller().Logger()
}

func setupProxySQL() {
	var err error

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/", userFlag, passwordFlag, addressFlag)

	psql, err = proxysql.New(dsn)
	if err != nil {
		logger.Error().Err(err).Msg("Unable to connect to ProxySQL")
	}
}
