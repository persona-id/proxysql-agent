# ProxySQL Agent

## About

A simple, statically compiled go binary for use in maintaining the state of a [ProxySQL](https://github.com/sysown/proxysql) cluster. Also includes a [Dockerfile](Dockerfile) to generate an alpine based image, for use as a kubernetes sidecar.

MVP will include the ability for the agent to maintain the `core` cluster and the `satellite` cluster, each of which has its own set of commands that need to be run.

**NB**: I'd like to open source this ASAP, provided we get signoff from legal. There is so little tooling for ProxySQL out there, this might be useful to someone.

#### Why did you pick golang, if we're a Ruby shop?

I looked into using ruby, and in fact the "agents" we are currently running **are** written in ruby, but there have some issues:

- The scheduler spawns a new ruby process every 10s
  - Each ruby process shells out to the mysql binary one or more times per script run; I wanted to avoid installing mysql gems
  - If the proxysql admin interface gets wedged, the ruby and mysl processes still continue to spawn and block, which will eventually lead to either inode exhaustion or an OOM
- We can statically compile this, and don't need to mess with a bunch of ruby gems. And I mean a _bunch_ of ruby gems
- k8s tooling is generally written in Golang, and it shows. The ruby k8s gems are not as good as the golang libraries, unfortunately

I will say, I _am_ more comfortable with Ruby and am still leaning all of the Go... **pecularities**... so I am more than open to feedback here. However, since this is such a simple application I have no qualms about the choice of language.

### Design

N/A, as yet

### Status - Alpha

This is currently in alpha. Do not use it in production yet.

### TODOS

There are some linear tickets, but here's a high level overview of what I have in mind.

- *P1* - Dump the contents of `stats_mysql_query_digests` to a file on disk; will be used to get the data into snowflake. File format TBD
- *P1* - Health checks; replace the ruby health probe with this
  - since this is a sidecar, I'm not entirely sure how I would be able to do this. Maybe compile it and add it to our proxysql image, and use `--health-check` to only run healthchecks as a standalone binary, and 86 the ruby stuff
- *P2* - Replace the pre-stop ruby script with this
- *P3* - HTTP API for controlling the agent. Much to do here, many ideas
  - health checks
  - get proxysql admin status
  - force a satellite resync (if running in satellite mode)
  - etc
- *P3* - Leader election; elect one core pod and have it be responsible for managing cluster state
- *P3* - "plugin" support; we don't necessarily need to add all the Persona specific cases to the main agent, as they won't likely apply to most people

### See also

Libraries in use:

* [k8s-client-go](https://github.com/kubernetes/client-go) - Golang k8s client
* [slog](https://pkg.go.dev/log/slog) ([examples](https://betterstack.com/community/guides/logging/logging-in-go/)) - log/slog
* [Viper](https://pkg.go.dev/github.com/spf13/viper) - Go configuration library; allows config from a file, ENV, or commandline flags

Misc:

* Look into using [nacelle](https://www.nacelle.dev/docs/topics/overview/) down the road
* Some leader election examples: [golang-k8s-leader-example](https://github.com/mjasion/golang-k8s-leader-example)
