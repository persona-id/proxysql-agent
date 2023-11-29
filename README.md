# ProxySQL Agent

[![Go Report Card](https://goreportcard.com/badge/github.com/kuzmik/proxysql-agent)](https://goreportcard.com/report/github.com/kuzmik/proxysql-agent)

## About

A small, statically compiled go binary for use in maintaining the state of a [ProxySQL](https://github.com/sysown/proxysql) cluster. Also includes a [Dockerfile](Dockerfile) to generate an alpine based image, for use as a kubernetes sidecar.

**NB**: I'd like to open source this ASAP, provided we get signoff from legal. There is so little tooling for ProxySQL out there, this might be useful to someone.

### "Self healing" the ProxySQL cluster

TODO: diagram of core/satellite pods
TODO: link to example deployment

This is mainly useful in a kubernetes deployment if you have a horizontal pod autoscaler defined for satellite and/or core pods; as these pods scale in and out, the state of the ProxySQL cluster needs to be maintained. If you are running a static cluster on VMs and the hosts rarely change, or you don't use an HPA, this probably won't be as useful to you (though there are some features coming that might help with even that).

Some examples of where this is necessary:

- As satellite pods scale in, the core pods need to run `LOAD PROXYSQL SERVERS TO RUNTIME` in order to accept the new pods to the cluster; until that is done, the satellite pod will not receive configuration from the core pods
- As core pods recycle (or all core pods are recycled) and IPs to them change, the satellites need to run some commands to load the new core pods into runtime
- If _all_ core pods recycle, the satellite pods will run `LOAD PROXYSQL SERVERS FROM CONFIG` which points them to the `proxysql-core` service, and once the core pods are up the satellites should receive configuration again

You can see the code for this in `proxysql.go` in the `Core()` and `Satellite()` functions.

Note that if your cluster is running fine, and the core pods all go away, the satellites will continue to function with the settings they already had; in other words, even if the core pods vanish, you will still serve proxied MySQL traffic.

#### Why did you pick golang, if you work at a Ruby shop?

I looked into using ruby, and in fact the "agents" we are currently running **are** written in ruby, but there have been some issues:

- The scheduler spawns a new ruby process every 10s
  - Each ruby process shells out to the mysql binary one or more times per script run; I wanted to avoid installing mysql gems
  - If the proxysql admin interface gets wedged, the ruby and mysl processes still continue to spawn and block, which will eventually lead to either inode exhaustion or an OOM
- We can statically compile this, and don't need to mess with a bunch of ruby gems. And I mean a _bunch_ of ruby gems
- k8s tooling is generally written in Golang, and it shows. The ruby k8s gems are not as good as the golang libraries, unfortunately

I will say, I _am_ more comfortable with Ruby and am still leaning all of the Go differences, so I am more than open to feedback here. However, since this is such a simple application I have no qualms about the choice of language.

### Design

N/A, as yet

### Status - Alpha

This is currently in beta.

### TODOs

There are some linear tickets, but here's a high level overview of what I have in mind.

- *P2* - Better test coverage
- *P3* - Leader election; elect one core pod and have it be responsible for managing cluster state
- *P3* - "plugin" support; we don't necessarily need to add all the Persona specific cases to the main agent, as they won't likely apply to most people
  - "chaosmonkey" feature
    - feature branch currently has it as included in the main agent, but I will extract it later
  - uploading of the CSV dump files to snowflake (likely GCS in this case)
- *P5* - HTTP API for controlling the agent. Much to do here, many ideas
  - get proxysql admin status
  - force a satellite resync (if running in satellite mode)
  - etc
  - Now I'm no sure this is that important; we can just add more commands to the agent, and run said commands from the CLI
- *P5* - If possible, cleanup the errors that are thrown when the `preStop` hook runs. This might not be possible due to how k8s kills containers, but if it is, these errors need to go away:
    ```
    time=2023-11-29T02:32:22.422Z level=INFO msg="Pre-stop called, starting shutdown process" shutdownDelay=120
    time=2023-11-29T02:32:24.341Z level=INFO msg="Pre-stop commands ran" commands="UPDATE global_variables SET variable_value = 120000 WHERE variable_name in ('mysql-connection_max_age_ms', 'mysql-max_transaction_idle_time', 'mysql-max_transaction_time'); UPDATE global_variables SET variable_value = 1 WHERE variable_name = 'mysql-wait_timeout'; LOAD MYSQL VARIABLES TO RUNTIME; PROXYSQL PAUSE;"
    time=2023-11-29T02:32:24.343Z level=INFO msg="No connected clients remaining, proceeding with shutdown"
    [mysql] 2023/11/29 02:32:24 packets.go:37: unexpected EOF
    time=2023-11-29T02:32:24.348Z level=ERROR msg="KILL command failed" commands="PROXYSQL KILL" error="invalid connection"
    rpc error: code = Unknown desc = Error: No such container: e3153c34e0ad525c280dd26695b78d917b1cb377a545744bffb9b31ad1c90670%
    ```

#### MVP Requirements

1. ✅ Cluster management (ie: core and satellite agents)
1. ✅ Health checks via an HTTP endpoint, specifically for the ProxySQL container
1. ✅ Pre-stop hook replacement

#### Done

- *P1* - ~~Dump the contents of `stats_mysql_query_digests` to a file on disk; will be used to get the data into snowflake. File format is CSV~~
- *P1* - ~~Health checks; replace the ruby health probe with this~~
- *P2* - ~~Replace the pre-stop ruby script with this~~

### Releasing a new version

1. Update version in Makefile (and anywhere that calls `go build`, like pipelines)
1. Update the CHANGELOG.md with the changes

### See also

Libraries in use:

* [k8s-client-go](https://github.com/kubernetes/client-go) - Golang k8s client
* [slog](https://pkg.go.dev/log/slog) ([examples](https://betterstack.com/community/guides/logging/logging-in-go/)) - log/slog
* [Viper](https://pkg.go.dev/github.com/spf13/viper) - Go configuration library; allows config from a file, ENV, or commandline flags

Misc:

* Look into possibly using [nacelle](https://www.nacelle.dev/docs/topics/overview/) down the road
* Some leader election examples: [golang-k8s-leader-example](https://github.com/mjasion/golang-k8s-leader-example)
