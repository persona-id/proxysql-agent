# ProxySQL Agent

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

#### Why did you pick golang, if we're a Ruby shop?

I looked into using ruby, and in fact the "agents" we are currently running **are** written in ruby, but there have been some issues:

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

### TODOs

There are some linear tickets, but here's a high level overview of what I have in mind.

- *P1* - Health checks; replace the ruby health probe with this
  - make use of a filesystem check for this; use the agent to drop a file into the proxysql container FS (for either healthy or not, haven't decided yet), and make the proxysql container healthcheck monitor that file
- *P2* - Replace the pre-stop ruby script with this
  - same deal as the health check, use the shared FS for this
- *P3* - Leader election; elect one core pod and have it be responsible for managing cluster state
- *P3* - "plugin" support; we don't necessarily need to add all the Persona specific cases to the main agent, as they won't likely apply to most people
  - "chaosmonkey" feature
    - feature branch currently has it as included in the main agent, but I will extract it later
  - uploading of the CSV dump files to snowflake (likely GCS in this case)
- *P5* - HTTP API for controlling the agent. Much to do here, many ideas
  - health checks
  - get proxysql admin status
  - force a satellite resync (if running in satellite mode)
  - etc
  - Now I'm no sure this is that important; we can just add more commands to the agent, and run said commands from the CLI

#### Done

- *P1* - ~~Dump the contents of `stats_mysql_query_digests` to a file on disk; will be used to get the data into snowflake. File format is CSV~~

### See also

Libraries in use:

* [k8s-client-go](https://github.com/kubernetes/client-go) - Golang k8s client
* [slog](https://pkg.go.dev/log/slog) ([examples](https://betterstack.com/community/guides/logging/logging-in-go/)) - log/slog
* [Viper](https://pkg.go.dev/github.com/spf13/viper) - Go configuration library; allows config from a file, ENV, or commandline flags

Misc:

* Look into using [nacelle](https://www.nacelle.dev/docs/topics/overview/) down the road
* Some leader election examples: [golang-k8s-leader-example](https://github.com/mjasion/golang-k8s-leader-example)
