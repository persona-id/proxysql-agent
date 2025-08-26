# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

ProxySQL Agent is a statically compiled Go binary designed to maintain state in a ProxySQL cluster, primarily for use as a Kubernetes sidecar container. The agent handles cluster self-healing operations, health checks, and graceful shutdown procedures.

## Development Commands

### Build and Test
- `make build` - Build the binary (outputs to `proxysql-agent`)
- `make test` - Run tests with race detection and randomized order
- `make bench` - Run benchmarks
- `make check` - Full validation pipeline (format, lint, test)

### Code Quality
- `make fmt` - Format code using golangci-lint-v2
- `make lint` - Run golangci-lint-v2 with comprehensive rules
- `make coverage` - Generate test coverage report
- `make coverage-html` - Generate HTML coverage report and open in browser

### Cleanup
- `make clean` - Remove build artifacts and coverage files

### Docker
- `make docker` - Build Docker image (runs clean and lint first)

### Running Locally
- `make run` - Build and run with sample satellite configuration (requires ProxySQL instance)

### Linting Tool
Uses `golangci-lint-v2` - note the `-v2` suffix is required for this project's tooling.

## Architecture

### Core Components

1. **Main Entry Point** (`cmd/proxysql-agent/main.go`)
   - Handles configuration parsing and signal management
   - Supports three run modes: `core`, `satellite`, and `dump`
   - Implements graceful shutdown with SIGTERM/SIGINT handling
   - Supports SIGUSR1 (status dump) and SIGUSR2 (config reload) signals

2. **Configuration System** (`internal/configuration/`)
   - Uses Viper for hierarchical configuration (precedence: defaults → config file → ENV → flags)
   - Environment variables use `AGENT_` prefix with dot-to-underscore replacement
   - Supports both YAML config files and command-line flags
   - Configuration validation with custom error types

3. **ProxySQL Management** (`internal/proxysql/`)
   - **Core Module** (`core.go`): Manages ProxySQL core pods using Kubernetes informers
   - **Satellite Module** (`satellite.go`): Handles satellite pod clustering logic
   - **Main Module** (`proxysql.go`): Database connection management and health probes
   - Uses MySQL driver to connect to ProxySQL admin interface (default: `127.0.0.1:6032`)

4. **REST API** (`internal/restapi/`)
   - HTTP server for Kubernetes health checks and lifecycle management
   - Endpoints: `/healthz/started`, `/healthz/ready`, `/healthz/live`, `/shutdown`
   - Implements graceful shutdown via prestop hooks

### Operational Modes

- **Core Mode**: Manages the ProxySQL cluster state using Kubernetes pod informers
- **Satellite Mode**: Maintains connection to core pods and handles cluster membership
- **Dump Mode**: Exports ProxySQL query digest data to CSV files

### Key Design Patterns

- Uses structured logging with `log/slog`
- Kubernetes client-go for cluster operations
- Graceful shutdown with drain file mechanism
- Health probe system with backend status monitoring
- Thread-safe operations with sync primitives
- Always use idiomatic Golang patterns, and rely on the standard library as much as possible
- Always write table driven tests, and NEVER use testify or other testing frameworks
- When adding or changing code, ensure there are tests for it

## Configuration

Configuration follows precedence order:
1. Code defaults
2. YAML config file (`configs/example_config.yaml` for reference)
3. Environment variables (prefix: `AGENT_`)
4. Command-line flags

Example: `AGENT_PROXYSQL_PASSWORD` sets the ProxySQL admin password.

## Testing

- All tests use race detection (`--race`)
- Tests are randomized (`--shuffle=on`)
- Configuration tests cannot be parallelized due to shared global state
- Uses `go-sqlmock` for database testing

## Dependencies

- Go 1.25.0+
- Key dependencies: MySQL driver, Viper, Kubernetes client-go, slog, tint (colored logging)
- Uses golangci-lint v2 with comprehensive rule set (see `.golangci.yml`)

## Deployment Context

Designed for Kubernetes deployment as sidecar containers alongside ProxySQL pods. Handles horizontal pod autoscaler scenarios and maintains cluster consistency during pod lifecycle events.
