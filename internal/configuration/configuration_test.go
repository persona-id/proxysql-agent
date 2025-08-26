package configuration

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

//nolint:gochecknoglobals
var testConfigFile = []byte(`
start_delay: 30
log:
  level: "TRACE"
  format: "text"
  source: true
  probes: false
run_mode: core
proxysql:
  address: "proxysql.vip:6032"
  username: "agent-user"
  password: "agent-password"
core:
  interval: 30
  podselector:
    namespace: test-namespace
    app: test-application
    component: test-component
satellite:
  interval: 60
`)

func TestValidations(t *testing.T) {
	tests := []struct {
		name    string
		wantErr error
		args    []string
	}{
		{
			name:    "valid default config",
			wantErr: nil,
			args:    []string{"cmd"},
		},
		{
			name:    "invalid run_mode",
			wantErr: ErrInvalidRunMode,
			args:    []string{"cmd", "--run_mode=failure"},
		},
		{
			name:    "negative start_delay",
			wantErr: ErrNegativeStartDelay,
			args:    []string{"cmd", "--start_delay=-1"},
		},
		{
			name:    "negative core.interval",
			wantErr: ErrNegativeCoreInterval,
			args:    []string{"cmd", "--core.interval=-1"},
		},
		{
			name:    "negative satellite.interval",
			wantErr: ErrNegativeSatelliteInterval,
			args:    []string{"cmd", "--satellite.interval=-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset viper and set command line args for each test
			viper.Reset()

			os.Args = tt.args
			pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

			// Run the function being tested
			_, err := Configure()

			// Check error
			if tt.wantErr == nil && err != nil {
				t.Errorf("Configure() unexpected error = %v", err)
			} else if tt.wantErr != nil {
				if err == nil {
					t.Errorf("Configure() expected error = %v, got nil", tt.wantErr)
				} else if !errors.Is(err, tt.wantErr) {
					t.Errorf("Configure() error = %v, wantErr %v", err, tt.wantErr)
				}
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	// Setup test
	os.Args = []string{"cmd"}
	pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

	viper.Reset()

	// Execute function being tested
	config, err := Configure()
	if err != nil {
		t.Fatalf("Configure() returned unexpected error: %v", err)
	}

	// Verify defaults using a table of expected values
	tests := []struct {
		name     string
		expected any
		got      any
	}{
		{"StartDelay", 0, config.StartDelay},
		{"Log.Level", "INFO", config.Log.Level},
		{"Log.Format", "text", config.Log.Format},
		{"Log.Source", false, config.Log.Source},
		{"Log.Probes", false, config.Log.Probes},
		{"ProxySQL.Address", "127.0.0.1:6032", config.ProxySQL.Address},
		{"ProxySQL.Username", "radmin", config.ProxySQL.Username},
		{"Core.Interval", 10, config.Core.Interval},
		{"Core.PodSelector.Namespace", "proxysql", config.Core.PodSelector.Namespace},
		{"Core.PodSelector.App", "proxysql", config.Core.PodSelector.App},
		{"Core.PodSelector.Component", "core", config.Core.PodSelector.Component},
		{"Satellite.Interval", 10, config.Satellite.Interval},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.expected) {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}
}

func TestConfigFile(t *testing.T) {
	// Create a temporary config file
	tmpfile, err := os.CreateTemp(t.TempDir(), "config_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())

	// Write the test config to the file
	_, fileErr := tmpfile.Write(testConfigFile)
	if fileErr != nil {
		t.Fatalf("Failed to write to temp file: %v", fileErr)
	}

	err = tmpfile.Close()
	if err != nil {
		t.Fatalf("Failed to close temp file: %v", err)
	}

	// Set up test
	viper.Reset()

	t.Setenv("AGENT_CONFIG_FILE", tmpfile.Name())

	os.Args = []string{"cmd"}

	pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

	// Execute function being tested
	config, err := Configure()
	if err != nil {
		t.Fatalf("Configure() returned unexpected error: %v", err)
	}

	// Check all expected values from config file
	tests := []struct {
		name     string
		expected any
		got      any
	}{
		{"StartDelay", 30, config.StartDelay},
		{"Log.Level", "TRACE", config.Log.Level},
		{"Log.Format", "text", config.Log.Format},
		{"Log.Source", true, config.Log.Source},
		{"Log.Probes", false, config.Log.Probes},
		{"RunMode", "core", config.RunMode},
		{"ProxySQL.Address", "proxysql.vip:6032", config.ProxySQL.Address},
		{"ProxySQL.Username", "agent-user", config.ProxySQL.Username},
		{"ProxySQL.Password", "agent-password", config.ProxySQL.Password},
		{"Core.Interval", 30, config.Core.Interval},
		{"Core.PodSelector.Namespace", "test-namespace", config.Core.PodSelector.Namespace},
		{"Core.PodSelector.App", "test-application", config.Core.PodSelector.App},
		{"Core.PodSelector.Component", "test-component", config.Core.PodSelector.Component},
		{"Satellite.Interval", 60, config.Satellite.Interval},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.expected) {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}
}

func TestEnvironment(t *testing.T) {
	// Set up environment variables
	envVars := map[string]string{
		"AGENT_START_DELAY":                "500",
		"AGENT_LOG_LEVEL":                  "env-WARN",
		"AGENT_LOG_FORMAT":                 "env-text",
		"AGENT_LOG_SOURCE":                 "true",
		"AGENT_LOG_PROBES":                 "true",
		"AGENT_RUN_MODE":                   "satellite",
		"AGENT_PROXYSQL_ADDRESS":           "env-proxysql:6666",
		"AGENT_PROXYSQL_USERNAME":          "env-proxysql-user",
		"AGENT_PROXYSQL_PASSWORD":          "env-proxysql-password",
		"AGENT_CORE_PODSELECTOR_NAMESPACE": "env-proxysql-blue",
		"AGENT_CORE_PODSELECTOR_APP":       "env-proxysql-blue",
		"AGENT_CORE_PODSELECTOR_COMPONENT": "env-proxysql-core",
		"AGENT_SATELLITE_INTERVAL":         "60",
	}

	// Set each environment variable
	for k, v := range envVars {
		t.Setenv(k, v)
	}

	// Set up test
	os.Args = []string{"cmd"}
	pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

	viper.Reset()

	// Execute function being tested
	config, err := Configure()
	if err != nil {
		t.Fatalf("Configure() returned unexpected error: %v", err)
	}

	// Check all expected values from environment variables
	tests := []struct {
		name     string
		expected any
		got      any
	}{
		{"StartDelay", 500, config.StartDelay},
		{"Log.Level", "env-WARN", config.Log.Level},
		{"Log.Format", "env-text", config.Log.Format},
		{"Log.Source", true, config.Log.Source},
		{"Log.Probes", true, config.Log.Probes},
		{"RunMode", "satellite", config.RunMode},
		{"ProxySQL.Address", "env-proxysql:6666", config.ProxySQL.Address},
		{"ProxySQL.Username", "env-proxysql-user", config.ProxySQL.Username},
		{"ProxySQL.Password", "env-proxysql-password", config.ProxySQL.Password},
		{"Core.PodSelector.Namespace", "env-proxysql-blue", config.Core.PodSelector.Namespace},
		{"Core.PodSelector.App", "env-proxysql-blue", config.Core.PodSelector.App},
		{"Core.PodSelector.Component", "env-proxysql-core", config.Core.PodSelector.Component},
		{"Satellite.Interval", 60, config.Satellite.Interval},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.expected) {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}
}

func TestFlags(t *testing.T) {
	// Define the command-line flags to test
	flags := []string{
		"cmd",
		"--start_delay=415",
		"--log.level=ERROR",
		"--log.format=text",
		"--log.source=true",
		"--log.probes=false",
		"--run_mode=core",
		"--proxysql.address=86.75.30.9:9999",
		"--proxysql.username=nick",
		"--proxysql.password=NOWAY",
		"--core.interval=1000",
		"--core.podselector.app=proxysql-green",
		"--core.podselector.component=notcore",
		"--satellite.interval=5533",
	}

	// Set up test
	os.Args = flags
	pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

	viper.Reset()

	// Execute function being tested
	config, err := Configure()
	if err != nil {
		t.Fatalf("Configure() returned unexpected error: %v", err)
	}

	// Check all expected values from command-line flags
	tests := []struct {
		name     string
		expected any
		got      any
	}{
		{"StartDelay", 415, config.StartDelay},
		{"Log.Level", "ERROR", config.Log.Level},
		{"Log.Format", "text", config.Log.Format},
		{"Log.Source", true, config.Log.Source},
		{"Log.Probes", false, config.Log.Probes},
		{"RunMode", "core", config.RunMode},
		{"ProxySQL.Address", "86.75.30.9:9999", config.ProxySQL.Address},
		{"ProxySQL.Username", "nick", config.ProxySQL.Username},
		{"ProxySQL.Password", "NOWAY", config.ProxySQL.Password},
		{"Core.Interval", 1000, config.Core.Interval},
		{"Core.PodSelector.App", "proxysql-green", config.Core.PodSelector.App},
		{"Core.PodSelector.Component", "notcore", config.Core.PodSelector.Component},
		{"Satellite.Interval", 5533, config.Satellite.Interval},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.expected) {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}
}

func TestPrecedence(t *testing.T) {
	// Create a temporary config file
	tmpfile, err := os.CreateTemp(t.TempDir(), "config_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())

	// Write test config to the file
	_, err = tmpfile.Write(testConfigFile)
	if err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}

	err = tmpfile.Close()
	if err != nil {
		t.Fatalf("Failed to close temp file: %v", err)
	}

	// Set up environment variable for config file
	t.Setenv("AGENT_CONFIG_FILE", tmpfile.Name())

	tests := []struct {
		name           string
		envVars        map[string]string
		cmdArgs        []string
		checkField     string
		expectedValue  any
		fieldExtractor func(*Config) any
	}{
		{
			name:           "env overwrites config file",
			envVars:        map[string]string{"AGENT_CORE_PODSELECTOR_COMPONENT": "env-test"},
			cmdArgs:        []string{"cmd"},
			checkField:     "Core.PodSelector.Component",
			expectedValue:  "env-test",
			fieldExtractor: func(c *Config) any { return c.Core.PodSelector.Component },
		},
		{
			name:           "flag overwrites config file and env",
			envVars:        map[string]string{"AGENT_CORE_PODSELECTOR_COMPONENT": "env-test"},
			cmdArgs:        []string{"cmd", "--core.podselector.component=flagtest"},
			checkField:     "Core.PodSelector.Component",
			expectedValue:  "flagtest",
			fieldExtractor: func(c *Config) any { return c.Core.PodSelector.Component },
		},
		{
			name:           "config file value when no override",
			envVars:        map[string]string{},
			cmdArgs:        []string{"cmd"},
			checkField:     "StartDelay",
			expectedValue:  30,
			fieldExtractor: func(c *Config) any { return c.StartDelay },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset viper for each test
			viper.Reset()

			// Set environment variables
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			// Set command line arguments
			os.Args = tt.cmdArgs
			pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

			// Execute function being tested
			config, err := Configure()
			if err != nil {
				t.Fatalf("Configure() returned unexpected error: %v", err)
			}

			// Check the expected value
			got := tt.fieldExtractor(config)
			if !reflect.DeepEqual(got, tt.expectedValue) {
				t.Errorf("%s = %v, want %v", tt.checkField, got, tt.expectedValue)
			}
		})
	}
}

func TestClusterPort(t *testing.T) {
	tests := []struct {
		name         string
		address      string
		expectedPort int
		expectError  bool
		expectedErr  error
	}{
		{
			name:         "valid port",
			address:      "127.0.0.1:6032",
			expectedPort: 6032,
			expectError:  false,
		},
		{
			name:         "different valid port",
			address:      "proxysql.example.com:3306",
			expectedPort: 3306,
			expectError:  false,
		},
		{
			name:        "no colon",
			address:     "127.0.0.1",
			expectError: true,
			expectedErr: ErrMissingPort,
		},
		{
			name:        "multiple colons",
			address:     "127.0.0.1:6032:extra",
			expectError: true,
			expectedErr: ErrMissingPort,
		},
		{
			name:        "non-numeric port",
			address:     "127.0.0.1:abc",
			expectError: true,
			expectedErr: ErrMissingPort,
		},
		{
			name:        "empty port",
			address:     "127.0.0.1:",
			expectError: true,
			expectedErr: ErrMissingPort,
		},
		{
			name:        "empty address",
			address:     "",
			expectError: true,
			expectedErr: ErrMissingPort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{}
			config.ProxySQL.Address = tt.address

			port, err := config.ClusterPort()

			if tt.expectError { //nolint:nestif
				if err == nil {
					t.Errorf("ClusterPort() expected error, got nil")

					return
				}

				if tt.expectedErr != nil && !errors.Is(err, tt.expectedErr) {
					t.Errorf("ClusterPort() error = %v, want %v", err, tt.expectedErr)
				}
			} else {
				if err != nil {
					t.Errorf("ClusterPort() unexpected error = %v", err)

					return
				}

				if port != tt.expectedPort {
					t.Errorf("ClusterPort() = %v, want %v", port, tt.expectedPort)
				}
			}
		})
	}
}

func TestConfigureErrorScenarios(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func(t *testing.T) string
		expectError   bool
		errorContains string
	}{
		{
			name: "malformed yaml config file",
			setupFunc: func(t *testing.T) string {
				t.Helper()

				tmpfile, err := os.CreateTemp(t.TempDir(), "bad_config_*.yaml")
				if err != nil {
					t.Fatalf("Failed to create temp file: %v", err)
				}

				// Write malformed YAML
				_, err = tmpfile.WriteString("invalid: yaml: content: [\n")
				if err != nil {
					t.Fatalf("Failed to write to temp file: %v", err)
				}

				tmpfile.Close()

				return tmpfile.Name()
			},
			expectError:   true,
			errorContains: "error reading config file",
		},
		{
			name: "nonexistent config directory permissions",
			setupFunc: func(_ *testing.T) string {
				t.Helper()

				// Try to read from a file that will cause permission error
				return "/proc/1/mem"
			},
			expectError:   true,
			errorContains: "error reading config file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Reset()

			os.Args = []string{"cmd"}
			pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

			configFile := tt.setupFunc(t)
			t.Setenv("AGENT_CONFIG_FILE", configFile)

			_, err := Configure()

			if tt.expectError {
				if err == nil {
					t.Errorf("Configure() expected error, got nil")

					return
				}

				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("Configure() error = %v, want error containing %v", err, tt.errorContains)
				}
			} else if err != nil {
				t.Errorf("Configure() unexpected error = %v", err)
			}
		})
	}
}

func TestValidateConfigDumpMode(t *testing.T) {
	// Test that dump mode is valid in validation
	viper.Reset()

	os.Args = []string{"cmd", "--run_mode=dump"}
	pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

	config, err := Configure()
	if err != nil {
		t.Fatalf("Configure() with dump mode returned unexpected error: %v", err)
	}

	if config.RunMode != "dump" {
		t.Errorf("Configure() RunMode = %v, want dump", config.RunMode)
	}
}

func TestSetupLoggerLevels(t *testing.T) {
	// Test different log levels are handled correctly
	// Since setupLogger modifies global state, we test indirectly by checking no panics
	tests := []struct {
		name     string
		logLevel string
		format   string
	}{
		{"debug level json", "DEBUG", "JSON"},
		{"info level json", "INFO", "JSON"},
		{"warn level json", "WARN", "JSON"},
		{"error level json", "ERROR", "JSON"},
		{"debug level text", "DEBUG", "text"},
		{"info level text", "INFO", "text"},
		{"invalid level defaults to info", "INVALID", "JSON"},
		{"empty level defaults to info", "", "JSON"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Log: struct {
					Level  string `mapstructure:"level"`
					Format string `mapstructure:"format"`
					Source bool   `mapstructure:"source"`
					Probes bool   `mapstructure:"probes"`
				}{
					Level:  tt.logLevel,
					Format: tt.format,
					Source: false,
					Probes: false,
				},
			}

			// Test that setupLogger doesn't panic
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("setupLogger() panicked: %v", r)
				}
			}()

			setupLogger(config)
		})
	}
}

func TestConfigureMissingDefaultPaths(t *testing.T) {
	// Test behavior when no config file is found in default paths
	viper.Reset()

	os.Args = []string{"cmd"}
	pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

	// Ensure no AGENT_CONFIG_FILE is set and we're not in a directory with config files
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)

	config, err := Configure()
	if err != nil {
		t.Errorf("Configure() with missing config files returned unexpected error: %v", err)
	}

	// Ensure that it uses defaults when no config file is found
	if config.ProxySQL.Address != "127.0.0.1:6032" {
		t.Errorf("Configure() with no config file, ProxySQL.Address = %v, want 127.0.0.1:6032", config.ProxySQL.Address)
	}
}

func TestConfigureAPIDefaults(t *testing.T) {
	// Test that API configuration defaults are properly set
	viper.Reset()

	os.Args = []string{"cmd"}
	pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

	config, err := Configure()
	if err != nil {
		t.Fatalf("Configure() returned unexpected error: %v", err)
	}

	// Check API defaults that weren't covered in existing tests
	if config.API.Port != 8080 {
		t.Errorf("API.Port = %v, want 8080", config.API.Port)
	}

	if config.Shutdown.DrainingFile != "/var/lib/proxysql/draining" {
		t.Errorf("Shutdown.DrainingFile = %v, want /var/lib/proxysql/draining", config.Shutdown.DrainingFile)
	}
}

func TestLogDebugInfo(t *testing.T) {
	// Test logDebugInfo doesn't panic with valid config
	config := &Config{
		Log: struct {
			Level  string `mapstructure:"level"`
			Format string `mapstructure:"format"`
			Source bool   `mapstructure:"source"`
			Probes bool   `mapstructure:"probes"`
		}{
			Level:  "DEBUG",
			Format: "text",
			Source: false,
			Probes: false,
		},
		RunMode:    "core",
		StartDelay: 5,
		ProxySQL: struct {
			Address  string `mapstructure:"address"`
			Username string `mapstructure:"username"`
			Password string `mapstructure:"password"`
		}{
			Address:  "127.0.0.1:6032",
			Username: "admin",
			Password: "secret",
		},
		Core: struct {
			PodSelector struct {
				Namespace string `mapstructure:"namespace"`
				App       string `mapstructure:"app"`
				Component string `mapstructure:"component"`
			} `mapstructure:"podselector"`
			Interval int `mapstructure:"interval"`
		}{
			Interval: 10,
		},
		Satellite: struct {
			Interval int `mapstructure:"interval"`
		}{
			Interval: 15,
		},
		API: struct {
			Port int `mapstructure:"port"`
		}{
			Port: 8080,
		},
		Shutdown: struct {
			DrainingFile    string `mapstructure:"draining_file"`
			DrainTimeout    int    `mapstructure:"drain_timeout"`
			ShutdownTimeout int    `mapstructure:"shutdown_timeout"`
		}{
			DrainingFile:    "/tmp/draining",
			DrainTimeout:    30,
			ShutdownTimeout: 60,
		},
	}

	config.Core.PodSelector.Namespace = "default"
	config.Core.PodSelector.App = "proxysql"
	config.Core.PodSelector.Component = "core"

	// Test that logDebugInfo doesn't panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("logDebugInfo() panicked: %v", r)
		}
	}()

	logDebugInfo(config)
}
