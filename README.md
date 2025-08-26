# ProxySQL Agent

[![Go Report Card](https://goreportcard.com/badge/github.com/persona-id/proxysql-agent)](https://goreportcard.com/report/github.com/persona-id/proxysql-agent)

## About

The ProxySQL agent is a small, statically compiled Go binary (Go 1.25) for use in maintaining the state of a [ProxySQL](https://github.com/sysown/proxysql) cluster, primarily designed as a Kubernetes sidecar container. The repo includes a [Dockerfile](build/Dockerfile) to generate a debian-based image, or you can use the version in the [GitHub Container Registry](https://github.com/persona-id/proxysql-agent/pkgs/container/proxysql-agent).

There exists relatively little tooling around ProxySQL, so we hope that this is useful to others out there, even if it's just to learn how to maintain a cluster.

### "Self healing" the ProxySQL cluster

This is mainly useful in a kubernetes deployment if you have a horizontal pod autoscaler defined for satellite and/or core pods; as these pods scale in and out, the state of the ProxySQL cluster needs to be maintained. If you are running a static cluster on VMs and the hosts rarely change, or you don't use an HPA, this probably won't be as useful to you (though there are some features coming that might help with even that).

Some examples of where this is necessary:

- As satellite pods scale in, one of the core pods need to run `LOAD PROXYSQL SERVERS TO RUNTIME` in order to accept the new pods to the cluster; until that is done, the satellite pod will not receive configuration from the core pods
- As core pods recycle (or all core pods are recycled) and IPs to them change, the satellites need to run some commands to load the new core pods into runtime
- If _all_ core pods recycle, the satellite pods will run `LOAD PROXYSQL SERVERS FROM CONFIG` which points them to the `proxysql-core` service, and once the core pods are up the satellites should receive configuration again
  - Note that if your cluster is running fine and the core pods all go away, the satellites will continue to function with the settings they already had; in other words, even if the core pods vanish, you will still serve proxied MySQL traffic as long as the satellites have fetched the configuration once

### Why did you pick golang, if you work at a Ruby shop?

We looked into using ruby, and in fact the "agents" we are currently running **are** written in ruby, but there have been some issues:

- If the ProxySQL admin interface gets wedged, the ruby and mysl processes still continue to spawn and spin, which will eventually lead to either inode exhaustion or a container OOM
  - The scheduler spawns a new ruby process every 10s
    - Each ruby process shells out to the mysql binary several times times per script invocation
  - In addition to the scheduler process, the health probes is a separate ruby script that also spawns several mysql processes per run
    - Two script invocations every 10s, one for liveness and one for readiness

We wanted to avoid having to install a bunch of ruby gems in the container, so we decided shelling out to mysql was fine; we got most of the patterns from existing ProxySQL tooling and figured it'd work short term. And it has worked fine, though there have been enough instances of OOM'd containers that it's become worrisome. This usually happens if someone is in a pod doing any kind of work (modifying mysql query rules, etc), but we haven't been able to figure out what causes the admin interface to become wedged.

Because k8s tooling is generally written in Golang, the ruby k8s gems didn't seem to be as maintained or as easy to use as the golang libraries. And because the go process is statically compiled, and we won't need to deal with a bunch of external dependencies at runtime.

## Run Modes

The agent supports three run modes:

- **`core`** - Core pods maintain cluster state and configuration
- **`satellite`** - Satellite pods receive configuration from core pods
- **`dump`** - One-time export of ProxySQL statistics data to CSV files

## Design

In the [example repo](https://github.com/kuzmik/local-proxysql), there are two separate deployments; the `core` and the `satellite` deployments. The agent is responsible for maintaining the state of this cluster.

![image](assets/infra.png)

On boot, the agent will connect to the ProxySQL admin interface on `127.0.0.1:6032` (default address). It will maintain the connection throughout the life of the pod, and will periodically run the commands necessary to maintain the cluster, depending on the run mode specified on boot.

## HTTP API

The agent exposes several HTTP endpoints (default port 8080):

- **`/healthz/started`** - Startup probe (simple ping to ProxySQL admin interface)
- **`/healthz/ready`** - Readiness probe (comprehensive health checks, returns 503 when draining)
- **`/healthz/live`** - Liveness probe (health checks, remains healthy during graceful shutdown)
- **`/shutdown`** - Graceful shutdown endpoint for `container.lifecycle.preStop.httpGet` hooks

All health endpoints return JSON responses with detailed status information including backend server states, connection counts, and draining status.

## Signal Handling

The agent responds to Unix signals for operational control:

- **`SIGTERM/SIGINT`** - Initiates graceful shutdown
- **`SIGUSR1`** - Dumps current ProxySQL status and statistics to logs
- **`SIGUSR2`** - Reserved for future config reload functionality

## Configuration

The agent uses a sophisticated configuration system built on [Viper](https://github.com/spf13/viper) with multiple configuration sources in order of precedence:

1. Defaults set in code
2. Configuration file (YAML format, `config.yaml` or specified via `AGENT_CONFIG_FILE` env var)
3. Environment variables (prefixed with `AGENT_`, e.g., `AGENT_PROXYSQL_ADDRESS`)
4. Command-line flags

Key configuration options include:
- ProxySQL admin interface connection details
- Run mode (`core`, `satellite`, or `dump`)
- Kubernetes pod selector for cluster discovery
- HTTP API port and settings
- Logging configuration (level, format, structured logging)
- Graceful shutdown timeouts

See the included [`config.yaml`](config.yaml) for a complete configuration example.

## Development

### Building

```bash
# Build binary
make build

# Run tests with race detection
make test

# Full validation pipeline
make check

# Generate coverage report
make coverage
```

### Running Locally

```bash
# Run in satellite mode with debug logging
make run
```

Note: Local development requires a running ProxySQL instance. See the `docker-compose.yml` for a local development setup.

## TODOs

- *P3* - Expand HTTP API for operational control
  - Enhanced cluster status endpoints
  - Force satellite resync capability
  - Runtime configuration updates

### Done

- *P1* - ~~Dump the contents of `stats_mysql_query_digests` to a file on disk; will be used to get the data into snowflake. File format is CSV~~
- *P1* - ~~Health checks; replace the ruby health probe with this~~
- *P2* - ~~Replace the pre-stop ruby script with this~~
- *P2* - ~~Better test coverage~~

### MVP Requirements

1. ✅ Cluster management (ie: core and satellite agents)
1. ✅ Health checks via an HTTP endpoint, specifically for the ProxySQL container
1. ✅ Pre-stop hook replacement

## Releases

The project uses automated releases with [goreleaser](https://goreleaser.com/). Current version: **1.1.7**

To create a new release:

1. `git tag vX.X.X`
1. `git push origin vX.X.X`

This triggers goreleaser to build and publish:
- Linux AMD64 binary
- Multi-architecture Docker images
- GitHub release with changelog

See [CHANGELOG.md](CHANGELOG.md) for version history and recent updates.

## See also

### Key Dependencies

- [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql) - MySQL driver for ProxySQL admin interface
- [k8s-client-go](https://github.com/kubernetes/client-go) - Kubernetes API client for cluster discovery
- [slog](https://pkg.go.dev/log/slog) - Structured logging with JSON and text output formats
- [tint](https://github.com/lmittmann/tint) - Pretty console logging for development
- [viper](https://pkg.go.dev/github.com/spf13/viper) - Configuration management with file, ENV, and flag support
