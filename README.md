# ProxySQL Agent

[![Go Report Card](https://goreportcard.com/badge/github.com/kuzmik/proxysql-agent)](https://goreportcard.com/report/github.com/kuzmik/proxysql-agent)
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fkuzmik%2Fproxysql-agent.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2Fkuzmik%2Fproxysql-agent?ref=badge_shield)

## About

A small, statically compiled go binary for use in maintaining the state of a [ProxySQL](https://github.com/sysown/proxysql) cluster, for use as a kubernetes sidecar container. The repo includes a [Dockerfile](build/Dockerfile) to generate an alpine based image.

### "Self healing" the ProxySQL cluster

This is mainly useful in a kubernetes deployment if you have a horizontal pod autoscaler defined for satellite and/or core pods; as these pods scale in and out, the state of the ProxySQL cluster needs to be maintained. If you are running a static cluster on VMs and the hosts rarely change, or you don't use an HPA, this probably won't be as useful to you (though there are some features coming that might help with even that).

Some examples of where this is necessary:

- As satellite pods scale in, one of the core pods need to run `LOAD PROXYSQL SERVERS TO RUNTIME` in order to accept the new pods to the cluster; until that is done, the satellite pod will not receive configuration from the core pods
- As core pods recycle (or all core pods are recycled) and IPs to them change, the satellites need to run some commands to load the new core pods into runtime
- If _all_ core pods recycle, the satellite pods will run `LOAD PROXYSQL SERVERS FROM CONFIG` which points them to the `proxysql-core` service, and once the core pods are up the satellites should receive configuration again
  - Note that if your cluster is running fine and the core pods all go away, the satellites will continue to function with the settings they already had; in other words, even if the core pods vanish, you will still serve proxied MySQL traffic as long as the satellites have fetched the configuration once

### Why did you pick golang, if you work at a Ruby shop?

I looked into using ruby, and in fact the "agents" we are currently running **are** written in ruby, but there have been some issues:

- If the proxysql admin interface gets wedged, the ruby and mysl processes still continue to spawn and spin, which will eventually lead to either inode exhaustion or a container OOM
  - The scheduler spawns a new ruby process every 10s
    - Each ruby process shells out to the mysql binary several times times per script invocation
  - In addition to the scheduler process, the health probes is a separate ruby script that also spawns several mysql processes per run 
    - Two script invocations every 10s, one for liveness and one for readiness


We wanted to avoid having to install a bunch of ruby gems in the container, so we decided shelling out to mysql was fine; we got most of the patterns from existing ProxySQL tooling and figured it'd work short term. The ruby has worked fine, though there have been enough instances of OOM'd containers that it's become worrisome. This usually happens if someone is in a pod doing any kind of work (modifying mysql query rules, etc), but we haven't been able to figure out what causes the admin interface to become wedged.

Because k8s tooling is generally written in Golang, the ruby k8s gems didn't seem to be as maintained or as easy to use as the golang libraries. And because the go process is statically compiled, and we won't need to deal with a bunch of external dependencies at runtime.


## Design

In the [example repo](https://github.com/kuzmik/local-proxysql), there are two separate deployments; the `core` and the `satellite` deployments. The agent is responsible for maintaining this cluster.

![image](docs/infra.png)

On boot, the agent will connect to the ProxySQL admin interface on `127.0.0.1:6032` (default address). It will maintain the connection throughout the life of the pod, and will periodicially run the commands necessary to maintain the cluster, depending on the run mode specified on boot. 

Additionally, the agent also exposes a simple HTTP API used for k8s health checks for the pod, as well as the /shutdown endpoint, which can be used in a `container.lifecycle.preStop.httpGet` hook to gracefully drain traffic from a pod before stopping it.


## Status - Beta

This is currently in beta. We are running this in staging.


## TODOs

There are some internal linear tickets, but here's a high level overview of what we have in mind.

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

### MVP Requirements

1. ✅ Cluster management (ie: core and satellite agents)
1. ✅ Health checks via an HTTP endpoint, specifically for the ProxySQL container
1. ✅ Pre-stop hook replacement

### Done

- *P1* - ~~Dump the contents of `stats_mysql_query_digests` to a file on disk; will be used to get the data into snowflake. File format is CSV~~
- *P1* - ~~Health checks; replace the ruby health probe with this~~
- *P2* - ~~Replace the pre-stop ruby script with this~~


## Releasing a new version

1. Update version in Makefile (and anywhere that calls `go build`, like pipelines)
1. Update the CHANGELOG.md with the changes


## See also

Libraries in use:

* [k8s-client-go](https://github.com/kubernetes/client-go) - Golang k8s client
* [slog](https://pkg.go.dev/log/slog) ([examples](https://betterstack.com/community/guides/logging/logging-in-go/)) - log/slog
* [Viper](https://pkg.go.dev/github.com/spf13/viper) - Go configuration library; allows config from a file, ENV, or commandline flags

Misc:

* Look into possibly using [nacelle](https://www.nacelle.dev/docs/topics/overview/) down the road
* Some leader election examples: [golang-k8s-leader-example](https://github.com/mjasion/golang-k8s-leader-example)


## License
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fkuzmik%2Fproxysql-agent.svg?type=large)](https://app.fossa.com/projects/git%2Bgithub.com%2Fkuzmik%2Fproxysql-agent?ref=badge_large)