package configuration

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/yassinebenaid/godump"
)

var (
	ErrInvalidRunMode            = errors.New("run_mode must be either 'core' or 'satellite'")
	ErrNegativeStartDelay        = errors.New("start_delay cannot be < 0")
	ErrNegativeCoreInterval      = errors.New("core.interval cannot be < 0")
	ErrNegativeSatelliteInterval = errors.New("satellite.interval cannot be < 0")
)

type Config struct {
	ProxySQL struct {
		Address  string `mapstructure:"address"`
		Username string `mapstructure:"username"`
		Password string `mapstructure:"password"`
	} `mapstructure:"proxysql"`
	Log struct {
		Level  string `mapstructure:"level"`
		Format string `mapstructure:"format"`
	} `mapstructure:"log"`
	RunMode string `mapstructure:"run_mode"`
	Core    struct {
		PodSelector struct {
			Namespace string `mapstructure:"namespace"`
			App       string `mapstructure:"app"`
			Component string `mapstructure:"component"`
		} `mapstructure:"podselector"`
		Interval int `mapstructure:"interval"`
	} `mapstructure:"core"`
	Interfaces []string `mapstructure:"interfaces"`
	Satellite  struct {
		Interval int `mapstructure:"interval"`
	} `mapstructure:"satellite"`
	StartDelay int `mapstructure:"start_delay"`
}

// Parse the various configuration methods. Levels of precedence, from least to most:
//  1. defaults set in this function
//  2. config file
//  3. ENV variables
//  4. commandline flags
func Configure() (*Config, error) {
	// set up some ENV settings
	// the replacer lets us access nested configs, like PROXYSQL_ADDRESS will equate to proxysql.address
	replacer := strings.NewReplacer(".", "_")
	viper.GetViper().SetEnvKeyReplacer(replacer)
	viper.GetViper().SetEnvPrefix("AGENT")
	viper.GetViper().AutomaticEnv()

	// Set default values
	setDefaults()

	if file := os.Getenv("AGENT_CONFIG_FILE"); file != "" {
		// if the config file path is specified in the env, load that
		viper.SetConfigFile(file)
	} else {
		// otherwise setup some default locations
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		viper.AddConfigPath("/etc/proxysql-agent")
		viper.AddConfigPath(".")
	}

	// read the config file, if it exists. if not, keep on truckin'
	err := viper.ReadInConfig()
	if err != nil {
		errVal := viper.ConfigFileNotFoundError{}
		if ok := errors.As(err, &errVal); !ok {
			return nil, fmt.Errorf("error reading config file: %w", err)
		}
	}

	// Setup command line flags
	err = setupFlags()
	if err != nil {
		return nil, fmt.Errorf("error setting up flags: %w", err)
	}

	// we are only dumping the config if the secret flag show-config is specified, because the config
	// contains the proxysql admin password
	if viper.GetViper().GetBool("show-config") {
		dumpErr := godump.Dump(viper.GetViper().AllSettings())
		if dumpErr != nil {
			slog.Error("Error in Dump()", slog.Any("error", dumpErr))
			os.Exit(1)
		}

		os.Exit(0)
	}

	// Validate configuration
	err = validateConfig()
	if err != nil {
		return nil, err
	}

	settings := &Config{} //nolint:exhaustruct

	err = viper.Unmarshal(settings)
	if err != nil {
		return nil, fmt.Errorf("error unmarshaling configuration: %w", err)
	}

	return settings, nil
}

// setDefaults sets default values for configuration.
func setDefaults() {
	// set some defaults
	viper.GetViper().SetDefault("start_delay", 0)
	viper.GetViper().SetDefault("log.level", "INFO")
	viper.GetViper().SetDefault("log.format", "text")
	viper.GetViper().SetDefault("run_mode", nil)

	// use the dot notation to access nested values
	viper.GetViper().SetDefault("proxysql.address", "127.0.0.1:6032")
	viper.GetViper().SetDefault("proxysql.username", "radmin")
	viper.GetViper().SetDefault("proxysql.password", "")

	viper.GetViper().SetDefault("core.interval", 10) //nolint:mnd
	viper.GetViper().SetDefault("core.podselector.namespace", "proxysql")
	viper.GetViper().SetDefault("core.podselector.app", "proxysql")
	viper.GetViper().SetDefault("core.podselector.component", "core")

	viper.GetViper().SetDefault("satellite.interval", 10) //nolint:mnd
}

// setupFlags sets up command line flags.
func setupFlags() error {
	pflag.Int("start_delay", 0, "seconds to pause before starting agent")
	pflag.String("log.level", "INFO", "the log level for the agent; defaults to INFO")
	pflag.String("log.format", "JSON", "Format of the logs; valid values: [JSON OR plain]")
	pflag.String("run_mode", "", "mode to run the agent in; valid values: [core OR satellite]")

	pflag.String("proxysql.address", "127.0.0.1:6032", "proxysql admin interface address")
	pflag.String("proxysql.username", "radmin", "user for the proxysql admin interface")
	pflag.String("proxysql.password", "radmin", "password for the proxysql admin interface; this is not recommended for use in production")

	pflag.Int("core.interval", 10, "seconds to sleep in the core clustering loop") //nolint:mnd
	pflag.String("core.checksum_file", "/tmp/pods-cs.txt", "path to the pods checksum file")
	pflag.String("core.podselector.namespace", "proxysql", "namespace to use in the k8s pod selector label")
	pflag.String("core.podselector.app", "proxysql", "app to use in the k8s pod selector label")
	pflag.String("core.podselector.component", "core", "component to use in the k8s pod selector label")

	pflag.Int("satellite.interval", 10, "seconds to sleep in the satellite clustering loop") //nolint:mnd

	pflag.Bool("show-config", false, "Dump the configuration for debugging")

	err := pflag.CommandLine.MarkHidden("show-config")
	if err != nil {
		return fmt.Errorf("error marking flag as hidden: %w", err)
	}

	pflag.Parse()

	err = viper.BindPFlags(pflag.CommandLine)
	if err != nil {
		return fmt.Errorf("failed to bind flags: %w", err)
	}

	return nil
}

// validateConfig validates the configuration values.
func validateConfig() error {
	if viper.GetViper().IsSet("run_mode") {
		runMode := viper.GetViper().GetString("run_mode")
		if runMode != "core" && runMode != "satellite" && runMode != "dump" {
			return ErrInvalidRunMode
		}
	}

	if delay := viper.GetViper().GetInt("start_delay"); delay < 0 {
		return ErrNegativeStartDelay
	}

	if cinterval := viper.GetViper().GetInt("core.interval"); cinterval < 0 {
		return ErrNegativeCoreInterval
	}

	if sinterval := viper.GetViper().GetInt("satellite.interval"); sinterval < 0 {
		return ErrNegativeSatelliteInterval
	}

	return nil
}
