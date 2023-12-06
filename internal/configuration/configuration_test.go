package configuration

import (
	"fmt"
	"os"
	"testing"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
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
	os.Args = []string{"cmd"}
	pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

	viper.Reset()

	_, err := Configure()

	assert.NoError(t, err, "Configuration should not return an error")

	t.Run("validate run_mode", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				// You can log or handle the panic here without printing to console
				t.Logf("Recovered from panic: %v", r)
			}
		}()

		viper.Reset()
		os.Args = []string{"cmd", "--run_mode=failure"}
		pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

		_, err := Configure()
		fmt.Println(err)
		assert.EqualError(t, err, "run_mode must be either 'core' or 'satellite'")
	})

	t.Run("validate start_delay", func(t *testing.T) {
		viper.Reset()
		os.Args = []string{"cmd", "--start_delay=-1"}
		pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

		_, err := Configure()
		fmt.Println(err)
		assert.EqualError(t, err, "start_delay cannot be < 0")
	})

	t.Run("validate core.interval", func(t *testing.T) {
		viper.Reset()
		os.Args = []string{"cmd", "--core.interval=-1"}
		pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

		_, err := Configure()
		fmt.Println(err)
		assert.EqualError(t, err, "core.interval cannot be < 0")
	})

	t.Run("validate satellite.interval", func(t *testing.T) {
		viper.Reset()
		os.Args = []string{"cmd", "--satellite.interval=-1"}
		pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

		_, err := Configure()
		fmt.Println(err)
		assert.EqualError(t, err, "satellite.interval cannot be < 0")
	})
}

func TestDefaults(t *testing.T) {
	os.Args = []string{"cmd"}
	pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

	viper.Reset()

	defaultsConfig, err := Configure()

	assert.NoError(t, err, "Configuration should not return an error")
	assert.Equal(t, 10, defaultsConfig.Satellite.Interval)
}

func TestConfigFile(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "config_test_*.yaml")
	assert.NoError(t, err)

	t.Cleanup(func() {
		os.Remove(tmpfile.Name())
	})

	viper.Reset()

	_, err = tmpfile.Write(testConfigFile)

	assert.NoError(t, err)
	tmpfile.Close()

	// Set environment variables need for testing the file
	t.Setenv("AGENT_CONFIG_FILE", tmpfile.Name())

	os.Args = []string{"cmd"}
	pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

	fileConfig, err := Configure()
	assert.NoError(t, err, "Configuration should not return an error")

	assert.Equal(t, 30, fileConfig.StartDelay)
	assert.Equal(t, "TRACE", fileConfig.Log.Level)
	assert.Equal(t, "core", fileConfig.RunMode)

	assert.Equal(t, "proxysql.vip:6032", fileConfig.ProxySQL.Address)
	assert.Equal(t, "agent-user", fileConfig.ProxySQL.Username)
	assert.Equal(t, "agent-password", fileConfig.ProxySQL.Password)

	assert.Equal(t, "test-application", fileConfig.Core.PodSelector.App)
	assert.Equal(t, "test-component", fileConfig.Core.PodSelector.Component)

	assert.Equal(t, 60, fileConfig.Satellite.Interval)
}

func TestEnvironment(t *testing.T) {
	t.Setenv("AGENT_START_DELAY", "500")
	t.Setenv("AGENT_LOG_LEVEL", "env-WARN")
	t.Setenv("AGENT_LOG_FORMAT", "env-text")
	t.Setenv("AGENT_RUN_MODE", "satellite")
	t.Setenv("AGENT_PROXYSQL_ADDRESS", "env-proxysql:6666")
	t.Setenv("AGENT_PROXYSQL_USERNAME", "env-proxysql-user")
	t.Setenv("AGENT_PROXYSQL_PASSWORD", "env-proxysql-password")
	t.Setenv("AGENT_CORE_PODSELECTOR_NAMESPACE", "env-proxysql-blue")
	t.Setenv("AGENT_CORE_PODSELECTOR_APP", "env-proxysql-blue")
	t.Setenv("AGENT_CORE_PODSELECTOR_COMPONENT", "env-proxysql-core")
	t.Setenv("AGENT_SATELLITE_INTERVAL", "60")

	os.Args = []string{"cmd"}
	pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

	viper.Reset()

	envConfig, err := Configure()

	assert.NoError(t, err, "Configuration should not return an error")

	assert.Equal(t, 500, envConfig.StartDelay)
	assert.Equal(t, "env-WARN", envConfig.Log.Level)
	assert.Equal(t, "env-text", envConfig.Log.Format)
	assert.Equal(t, "satellite", envConfig.RunMode)

	assert.Equal(t, "env-proxysql:6666", envConfig.ProxySQL.Address)
	assert.Equal(t, "env-proxysql-user", envConfig.ProxySQL.Username)
	assert.Equal(t, "env-proxysql-password", envConfig.ProxySQL.Password)

	assert.Equal(t, "env-proxysql-blue", envConfig.Core.PodSelector.Namespace)
	assert.Equal(t, "env-proxysql-blue", envConfig.Core.PodSelector.App)
	assert.Equal(t, "env-proxysql-core", envConfig.Core.PodSelector.Component)

	assert.Equal(t, 60, envConfig.Satellite.Interval)
}

func TestFlags(t *testing.T) {
	os.Args = []string{
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
	pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

	viper.Reset()

	envConfig, err := Configure()

	assert.NoError(t, err, "Configuration should not return an error")

	assert.Equal(t, 415, envConfig.StartDelay)
	assert.Equal(t, "ERROR", envConfig.Log.Level)
	assert.Equal(t, "text", envConfig.Log.Format)
	assert.Equal(t, "core", envConfig.RunMode)

	assert.Equal(t, "86.75.30.9:9999", envConfig.ProxySQL.Address)
	assert.Equal(t, "nick", envConfig.ProxySQL.Username)
	assert.Equal(t, "NOWAY", envConfig.ProxySQL.Password)

	assert.Equal(t, 1000, envConfig.Core.Interval)
	assert.Equal(t, "proxysql-green", envConfig.Core.PodSelector.App)
	assert.Equal(t, "notcore", envConfig.Core.PodSelector.Component)

	assert.Equal(t, 5533, envConfig.Satellite.Interval)
}

func TestPrecedence(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "config_test_*.yaml")
	assert.NoError(t, err)

	t.Cleanup(func() {
		os.Remove(tmpfile.Name())
	})

	_, err = tmpfile.Write(testConfigFile)

	assert.NoError(t, err)
	tmpfile.Close()

	// set environment variables need for testing the file
	t.Setenv("AGENT_CONFIG_FILE", tmpfile.Name())

	// not necessary to test config file taking precedence over defaults, the TestConfigfile
	// already demonstrates that

	t.Run("env overwrites config file", func(t *testing.T) {
		viper.Reset()

		t.Setenv("AGENT_CORE_PODSELECTOR_COMPONENT", "env-test")

		os.Args = []string{"cmd"}
		pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

		var configs *Config
		configs, err = Configure()
		assert.NoError(t, err, "Configuration should not return an error")

		// set in the config
		assert.Equal(t, 30, configs.StartDelay)

		// set via the ENV variable
		assert.Equal(t, "env-test", configs.Core.PodSelector.Component)
	})

	t.Run("flag overwrites config file and env", func(t *testing.T) {
		viper.Reset()

		t.Setenv("AGENT_CORE_PODSELECTOR_COMPONENT", "env-test")

		os.Args = []string{"cmd", "--core.podselector.component=flagtest"}
		pflag.CommandLine = pflag.NewFlagSet("cmd", pflag.ContinueOnError)

		var configs *Config
		configs, err = Configure()
		assert.NoError(t, err, "Configuration should not return an error")

		// set via the commandline flag
		assert.Equal(t, "flagtest", configs.Core.PodSelector.Component)
	})
}
