## 1.1.7

- Add catching SIGUSR1 and SIGUSR2; the former prints some stats to the log, the latter is NYI
- More devcontainer work; it's still broken until MSFT releases a go 1.25 base image, but in the meantime there is a docker-compose file to bootstrap a local proxysql server for testing
- Refactors to address many of TODOs and FIXMEs
- Improved test coverage
- Update the Makefile to match the standard golang Makefile we use
- Add CLAUDE.md
- More rennovatebot dependency updates

*NB* Some of this release was written using Claude, notably the tests

## 1.1.6

- Upgrade to go 1.25
- More dependency updates for go 1.25
- Changes to the devcontainer setup, but it's currently not working due to a missing go 1.25 image
- Upgrade golangci-lint to 2.4.0 and update the configs to work with the new version

## 1.1.5

- More changes to the Docker build process, switched to debian from alpine
- More fixes to the graceful shutdown logic

## 1.1.4

- Signal catching, so SIGTERM and SIGKILL are caught and initiate the graceful shutdown process
- Fix a race condition in the shutdown logic
- Dependency updates
- Changes to the docker build process
- Reworked some of the tests to make them more idiomatic (ie just using stdlib, no more `testify`)

*NB* Some of this release was written using Claude, notably the tests and the shutdown logic

## 1.1.3

- Basic devcontainers setup, will include more later
- Go updated to 1.22.3 and updated all of the deps

## 1.1.0

- More Go module updates
- Switched to using an `Informer()` to notify the agent when core pods enter/leave the cluster, rather than running get commands every X seconds; it's now push rather than pull, in other words
- Improved some of the tests and expanded test coverage

## 1.0.0

- Go module updates, fixed a breaking change that Viper introduced
- Followed some guides on go project layouts to redo the project layout
- Enabled some extra golangci checks and addressed as many of the findings as possible
- Add a configurable namespace to the core pod selector. We have several proxysql namespaces, so being able to configure it at runtime is important
- Add goreleaser config and workflow
- Added a config flag for logging format, defaults to json structured logs
- Added restapi for proxysql healthcheck endpoints
- Added the /shutdown rest api endpoint to gracefully drain traffic from ProxySQL before killing the container and pod

## 0.9.0

Initial beta release
