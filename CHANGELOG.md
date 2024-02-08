## 1.1.0

- More Go module updates
- Switched to using an `Informer()` to notify the agent when core pods enter/leave the cluster, rather than running get commands every X seconds; it's now push rather than pull, in other words
- Improved some of the tests and expanded test coverage

## 1.0.0

- Go module updates, fixed a breaking change that Viper introduced
- Followed some guides on go project layouts to redo the project layout
- Enabled some extra golangci checks and addressed as many of the findings as possible
- fix: add a configurable namespace to the core pod selector. We have several proxysql namespaces,
  so being able to configure it at runtime is important
- Add goreleaser config and workflow
- Added a config flag for logging format, defaults to json structured logs
- Added restapi for proxysql healthcheck endpoints
- Added the /shutdown rest api endpoint to gracefully drain traffic from ProxySQL before killing the container and pod

## 0.9.0

Initial beta release