package configuration

import (
	"errors"
	"os"
	"reflect"
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
