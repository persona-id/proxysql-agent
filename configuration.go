package main

import (
	"log/slog"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

type config struct {
	StartDelay int `mapstructure:"start_delay"`

	LogLevel string `mapstructure:"log_level"`

	ProxySQL struct {
		Address  string `mapstructure:"address"`
		Username string `mapstructure:"username"`
		Password string `mapstructure:"password"`
	} `mapstructure:"proxysql"`

	Core struct {
		Interval     int    `mapstructure:"interval"`
		ChecksumFile string `mapstructure:"checksum_file"`
		PodSelector  struct {
			App       string `mapstructure:"app"`
			Component string `mapstructure:"component"`
		} `mapstructure:"pod_selector"`
	} `mapstructure:"core"`

	Satellite struct {
		Interval int `mapstructure:"interval"`
	} `mapstructure:"satellite"`

	Interfaces []string `mapstructure:"interfaces"`
}

var Config config

// Parse the various configuration methods. Levels of precedence:
//  1. defaults set in this function
//  2. config file
//  3. ENV variables
//  4. commandline flags
func Configure() {
	setupLogger()

	// set up some ENV settings
	// the replacer lets us access nested configs, like PROXYSQL_ADDRESS will equate to proxysql.address
	replacer := strings.NewReplacer(".", "_")
	viper.GetViper().SetEnvKeyReplacer(replacer)
	viper.GetViper().SetEnvPrefix("AGENT")
	viper.GetViper().AutomaticEnv()

	// set some defaults
	viper.GetViper().SetDefault("start_delay", 1)
	viper.GetViper().SetDefault("log_level", "INFO")
	viper.GetViper().SetDefault("run_mode", nil)

	// use the dot notation to access nested values
	viper.GetViper().SetDefault("proxysql.address", "127.0.0.1:6032")
	viper.GetViper().SetDefault("proxysql.username", "radmin")

	viper.GetViper().SetDefault("core.interval", 10)
	viper.GetViper().SetDefault("core.checksum_file", "/tmp/pods-cs.txt")
	viper.GetViper().SetDefault("core.pod_selector.app", "proxysql")
	viper.GetViper().SetDefault("core.pod_selector.component", "core")

	viper.GetViper().SetDefault("satellite.interval", 10)

	// read the config file, if it exists. if not, keep on truckin'
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("/etc/proxysql-agent")
	viper.AddConfigPath("$HOME/.config/proxysql-agent")
	viper.AddConfigPath(".")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// Config file not found; ignore error and move on
		} else {
			slog.Error(
				"Error loading config file",
				slog.String("config_file", viper.GetViper().ConfigFileUsed()),
				slog.Any("err", err),
			)
		}
	}

	file := viper.GetViper().ConfigFileUsed()
	if file != "" {
		slog.Debug(
			"Loaded configuration from file",
			slog.String("config_file", file),
		)
	}

	// commandline flags
	pflag.Int("start_delay", 1, "seconds to pause before starting agent")
	pflag.String("log_level", "INFO", "the log level for the agent; defaults to INFO")
	pflag.String("run_mode", "", "mode to run the agent in; valid values: [core OR satellite]")

	pflag.String("proxysql.address", "127.0.0.1:6032", "proxysql admin interface address")
	pflag.String("proxysql.username", "radmin", "user for the proxysql admin interface")

	pflag.Int("core.interval", 10, "seconds to sleep in the core clustering loop")
	pflag.String("core.checksum_file", "/tmp/pods-cs.txt", "path to the pods checksum file")
	pflag.String("core.pod_selector.app", "proxysql", "app to use in the k8s pod selector label")
	pflag.String("core.pod_selector.component", "core", "component to use in the k8s pod selector label")

	pflag.Int("satellite.interval", 10, "seconds to sleep in the satellite clustering loop")

	pflag.Parse()
	viper.BindPFlags(pflag.CommandLine)

	logger.Debug("Settings", slog.Any("settings map", viper.GetViper().AllSettings()))

	// run some validations before proceeding
	if viper.GetViper().IsSet("run_mode") {
		run_mode := viper.GetViper().GetString("run_mode")
		if run_mode != "core" && run_mode != "satellite" {
			msg := "run_mode must be either 'core' or 'satellite'"
			logger.Error(msg, slog.String("run_mode", run_mode))
			panic(msg)
		}
	}

	if delay := viper.GetViper().GetInt("start_delay"); delay < 0 {
		msg := "start_delay cannot be less < 0"
		logger.Error(msg)
		panic(msg)
	}

	if cinterval := viper.GetViper().GetInt("core.interval"); cinterval < 0 {
		msg := "core.interval cannot be less < 0"
		logger.Error(msg, slog.Int("core.interval", cinterval))
		panic(msg)
	}

	if sinterval := viper.GetViper().GetInt("satellite.interval"); sinterval < 0 {
		msg := "satellite.interval cannot be less < 0"
		logger.Error(msg, slog.Int("start_delay", sinterval))
		panic(msg)
	}

	err := viper.Unmarshal(&Config)
	if err != nil {
		logger.Error("Unable to unmarshal onto config struct", slog.Any("error", err))
	}
}
