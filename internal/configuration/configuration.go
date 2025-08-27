package configuration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/lmittmann/tint"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/yassinebenaid/godump"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

var (
	ErrInvalidRunMode            = errors.New("run_mode must be either 'core' or 'satellite'")
	ErrNegativeStartDelay        = errors.New("start_delay cannot be < 0")
	ErrNegativeCoreInterval      = errors.New("core.interval cannot be < 0")
	ErrNegativeSatelliteInterval = errors.New("satellite.interval cannot be < 0")
	ErrMissingPort               = errors.New("missing port in address")
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
		Source bool   `mapstructure:"source"`
		Probes bool   `mapstructure:"probes"`
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
	API        struct {
		Port int `mapstructure:"port"`
	} `mapstructure:"api"`
	Shutdown struct {
		DrainingFile    string `mapstructure:"draining_file"`
		DrainTimeout    int    `mapstructure:"drain_timeout"`
		ShutdownTimeout int    `mapstructure:"shutdown_timeout"`
	} `mapstructure:"shutdown"`
}

// Configure() parses the various configuration methods. Levels of precedence, from least to most:
//  1. defaults set in this function
//  2. config file
//  3. ENV variables
//  4. commandline flags
//
// Returns a pointer to a Config struct and an error if the configuration is invalid.
func Configure() (*Config, error) {
	// set up some ENV settings
	// the replacer lets us access nested configs, like PROXYSQL_ADDRESS will equate to proxysql.address
	replacer := strings.NewReplacer(".", "_")
	viper.GetViper().SetEnvKeyReplacer(replacer)
	viper.GetViper().SetEnvPrefix("AGENT")
	viper.GetViper().AutomaticEnv()

	// Set default values
	setupDefaults()

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
			slog.Error("error in Dump()", slog.Any("error", dumpErr))
			os.Exit(1)
		}

		os.Exit(0)
	}

	// Validate configuration
	err = validateConfig()
	if err != nil {
		return nil, err
	}

	settings := &Config{}

	err = viper.Unmarshal(settings)
	if err != nil {
		return nil, fmt.Errorf("error unmarshaling configuration: %w", err)
	}

	setupLogger(settings)

	if settings.Log.Level == "DEBUG" {
		logDebugInfo(settings)
	}

	return settings, nil
}

// ClusterPort returns the port number from the proxysql address.
func (c *Config) ClusterPort() (int, error) {
	address := c.ProxySQL.Address

	parts := strings.Split(address, ":")
	if len(parts) != 2 { //nolint:mnd
		return 0, fmt.Errorf("%w: %s", ErrMissingPort, address)
	}

	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("%w: %s, %w", ErrMissingPort, parts[1], err)
	}

	return port, nil
}

// setupDefaults sets default values for configuration.
func setupDefaults() {
	// set some defaults
	viper.GetViper().SetDefault("start_delay", 0)
	viper.GetViper().SetDefault("log.level", "INFO")
	viper.GetViper().SetDefault("log.format", "text")
	viper.GetViper().SetDefault("log.source", false)
	viper.GetViper().SetDefault("log.probes", false)
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

	viper.GetViper().SetDefault("api.port", 8080) //nolint:mnd
	viper.GetViper().SetDefault("shutdown.draining_file", "/var/lib/proxysql/draining")

	viper.GetViper().SetDefault("shutdown.drain_timeout", 30)    //nolint:mnd
	viper.GetViper().SetDefault("shutdown.shutdown_timeout", 60) //nolint:mnd
}

// setupFlags sets up command line flags.
func setupFlags() error {
	pflag.Int("start_delay", 0, "seconds to pause before starting agent")
	pflag.String("log.level", "INFO", "the log level for the agent; defaults to INFO")
	pflag.String("log.format", "JSON", "Format of the logs; valid values: [JSON OR plain]")
	pflag.Bool("log.source", false, "Include source code location in the logs")
	pflag.Bool("log.probes", false, "Include probe results in the logs")

	pflag.String("run_mode", "", "mode to run the agent in; valid values: [core OR satellite]")

	pflag.String("proxysql.address", "127.0.0.1:6032", "proxysql admin interface address")
	pflag.String("proxysql.username", "radmin", "user for the proxysql admin interface")
	pflag.String("proxysql.password", "radmin", "password for the proxysql admin interface; this is not recommended for use in production")

	pflag.Int("core.interval", 10, "seconds to sleep in the core clustering loop") //nolint:mnd
	pflag.String("core.podselector.namespace", "proxysql", "namespace to use in the k8s pod selector label")
	pflag.String("core.podselector.app", "proxysql", "app to use in the k8s pod selector label")
	pflag.String("core.podselector.component", "core", "component to use in the k8s pod selector label")

	pflag.Int("satellite.interval", 10, "seconds to sleep in the satellite clustering loop") //nolint:mnd

	pflag.Int("api.port", 8080, "port for the HTTP API server") //nolint:mnd
	pflag.String("shutdown.draining_file", "/var/lib/proxysql/draining", "path to the draining status file")

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

// setupLogger sets up the slog logger as the default logger.
// Uses config.log.level and config.log.format to set aspects of the logger.
func setupLogger(settings *Config) {
	levelMap := map[string]slog.Level{
		"DEBUG": slog.LevelDebug,
		"INFO":  slog.LevelInfo,
		"WARN":  slog.LevelWarn,
		"ERROR": slog.LevelError,
	}

	level, exists := levelMap[settings.Log.Level]
	if !exists {
		level = slog.LevelInfo // default fallback
	}

	var handler slog.Handler

	if settings.Log.Format == "JSON" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			AddSource:   settings.Log.Source,
			Level:       level,
			ReplaceAttr: nil,
		})
	} else {
		handler = tint.NewHandler(os.Stdout, &tint.Options{
			AddSource:   settings.Log.Source,
			Level:       level,
			NoColor:     false,
			ReplaceAttr: nil,
			TimeFormat:  time.RFC3339,
		})
	}

	logger := slog.New(handler)

	// append slog to the k8s runtime logging chain, so we get k8s errors logged to both klog and slog
	setupRuntimeLogging()

	slog.SetDefault(logger)
}

// logDebugInfo logs debug information about the service, namely configuration values and build info.
func logDebugInfo(settings *Config) {
	slog.Warn("running service in debug mode")

	// Print out the build info for debugging purposes.
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		slog.Error("failed to read build info")
		os.Exit(1)
	}

	// Parse build info into key-value pairs for structured logging
	buildArgs := []any{}

	// Add Go version, path, and module info
	buildArgs = append(buildArgs, "go", buildInfo.GoVersion)
	buildArgs = append(buildArgs, "path", buildInfo.Path)
	buildArgs = append(buildArgs, "mod", buildInfo.Main.Path+" "+buildInfo.Main.Version)

	// Add build settings (skip deps)
	for _, biSettings := range buildInfo.Settings {
		if strings.HasPrefix(biSettings.Key, "build") ||
			strings.HasPrefix(biSettings.Key, "CGO_") ||
			strings.HasPrefix(biSettings.Key, "GO") {
			if biSettings.Value != "" {
				buildArgs = append(buildArgs, biSettings.Key, biSettings.Value)
			}
		}
	}

	slog.Debug("build info", buildArgs...)

	slog.Debug("configuration",
		slog.Group("config",
			slog.String("run_mode", settings.RunMode),
			slog.String("log.level", settings.Log.Level),
			slog.String("log.format", settings.Log.Format),
			slog.Bool("log.source", settings.Log.Source),
			slog.Bool("log.probes", settings.Log.Probes),
			slog.Int("start_delay", settings.StartDelay),
			slog.String("proxysql.address", settings.ProxySQL.Address),
			slog.String("proxysql.username", settings.ProxySQL.Username),
			slog.String("proxysql.password", "[REDACTED]"),
			slog.Int("satellite.interval", settings.Satellite.Interval),
			slog.Int("core.interval", settings.Core.Interval),
			slog.String("core.podselector.namespace", settings.Core.PodSelector.Namespace),
			slog.String("core.podselector.app", settings.Core.PodSelector.App),
			slog.String("core.podselector.component", settings.Core.PodSelector.Component),
			slog.Int("api.port", settings.API.Port),
			slog.String("shutdown.draining_file", settings.Shutdown.DrainingFile),
		),
	)
}

// setupRuntimeLogging appends a slog-based error handler after the default klog handlers
// so that errors sent via runtime.HandleError are logged to both klog and slog.
func setupRuntimeLogging() {
	slogHandler := func(_ context.Context, err error, msg string, keysAndValues ...any) {
		slog.Error("k8s runtime error",
			slog.String("msg", msg),
			slog.Any("error", err),
			slog.Any("context", keysAndValues),
		)
	}

	utilruntime.ErrorHandlers = append(utilruntime.ErrorHandlers, slogHandler) //nolint:reassign
}
