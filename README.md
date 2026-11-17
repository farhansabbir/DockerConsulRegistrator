# docker-consul-registrator

Registers your containers in Consul so its DNS answers for them: **A, AAAA,
SRV and PTR**. Label a container `consul=yes` and publish a port; the
registrator does the rest, and removes the registration when the container
stops.

It works with **Docker, Podman and containerd** alike, and finds out which
one it is talking to by itself.

## What it does

What carries the label - `consul=yes` unless `REGISTRATOR_LABEL` says
otherwise - is registered: a container (`docker run -l`), or a Swarm
service, on the service (`docker service create --label`) or on its
containers (`--container-label`). Every node registers the containers
running on it, at its own address, one Consul service instance **per
published port and address family**:

```
docker run -d -l consul=yes -p 8080:80 nginx      # on docker1, 192.168.1.93
```

gives, in the domain `dmarts.app` (`--domain`):

| Query | Answer |
|---|---|
| `nginx.service.dmarts.app A` | `192.168.1.93` |
| `nginx.service.dmarts.app AAAA` | the host's IPv6 address, when the port is published on IPv6 too |
| `nginx.service.dmarts.app SRV`, `_nginx._tcp.service.dmarts.app SRV` | port `8080`, one target per address family |
| PTR for the host's IPv4 address | the host's Consul node name, `docker1.node.dmarts.app` |
| PTR for the host's IPv6 address | a service on it, e.g. `nginx.service.dc1.dmarts.app` (Consul answers node names for a node's main address only) |

One instance per family is deliberate: Consul 2.1 gives an instance with
both an IPv4 and an IPv6 address only an IPv6 SRV target, which IPv4-only
clients cannot use, and no IPv6 PTR answer.

**Service names** - always a valid DNS label:
- a Swarm task is an instance of its service, named after it
  (`mystack_web` becomes `mystack-web`): every node registers its own tasks
  under that one name;
- any other container is a service of its own, named after the container
  (Compose's too: `shop-api-1`).

Each instance is tagged with its protocol (`tcp`, `udp`), so
`_dns._udp.service.dmarts.app` finds the UDP ones. A container publishing
more than one port has one instance per port under its one name, each also
tagged `port-<container port>`: `_web._port-443.service.dmarts.app`.
`--tags` adds your own.

When a container stops, the node it ran on deregisters its instances - and
only those.

**Health:** a container with a health check (`HEALTHCHECK`, `--health-cmd`)
gets a TTL check that follows it - critical while starting or unhealthy,
passing when healthy. Other TCP ports get a TCP check; UDP ports without a
health check get none. Run the agents with `dns_config { only_passing = true }`
and DNS answers with healthy instances only.

**It only touches its own services:** each carries `registered-by =
docker-consul-registrator` in its metadata; everything else on the agent is
left alone. Only `consul=yes` counts - `consul=true` is reported, not
registered.

It compares containers and services on every change the runtime reports,
and every minute (`--resync`) in case one was missed; when the runtime
restarts, it follows it again.

## The bundled Consul agent

The image carries a Consul agent. The registrator - the container's main
process - starts it in the same container (`--agent`, the image's default),
registers with it only, and the agent's anti-entropy keeps the Consul
servers in step. The registrator works out what the agent needs from the
host:

- **node name:** the host's name;
- **address:** the host's Swarm address, else the default route's;
- **IPv6:** the stable global address beside it, for AAAA answers;
- **DNS domain:** `CONSUL_DOMAIN`, made the agent's own domain - PTR
  answers name things in it - with `consul` kept as its alternative.

The rest comes from the environment (below): it writes the agent's
configuration into a file only it can read, at start. Configuration
mounted in `/consul/config` is read too, and the environment wins over it.

Without `--agent` (`REGISTRATOR_AGENT=false` is the default outside the
image), it registers with whichever agent `CONSUL_HTTP_ADDR` names. It
cannot change that agent's DNS domain, so with `CONSUL_DOMAIN` it checks
the agent serves it, and refuses to start, saying what to add, when it
does not.

## Running it

Everything is configured through environment variables - run the
container however you run containers. Before anything starts the
registrator checks them all; when something is missing or wrong it lists
every problem and does not start:

```
docker-consul-registrator: cannot start:
  - CONSUL_RETRY_JOIN (--join) is required: the Consul servers to join, comma-separated (or mount the agent's configuration in /consul/config)
  - with ACLs on, both CONSUL_AGENT_TOKEN and CONSUL_HTTP_TOKEN are needed (or their _FILE forms): the agent's token and the registrator's; with ACLs off, neither
Run 'docker-consul-registrator --help' for every setting.
```

| Variable | |
|---|---|
| `CONSUL_RETRY_JOIN` | **required** - the Consul servers to join, comma-separated |
| `CONSUL_DATACENTER` | their datacenter (default `dc1`) |
| `CONSUL_DOMAIN` | the DNS domain to serve the services in (default `consul`) |
| `REGISTRATOR_LABEL` | what marks a container or a Swarm service to register (default `consul=yes`) |
| `CONSUL_ENCRYPT` | the cluster's gossip key, when its gossip is encrypted |
| `CONSUL_AGENT_TOKEN`, `CONSUL_HTTP_TOKEN` | with ACLs on, both: the agent's token and the registrator's |
| `CONSUL_DNS_TOKEN` | the token anonymous DNS reads with |
| `CONSUL_LOCAL_CONFIG` | more agent configuration as a JSON object (TLS, for one) |

Each secret can come from a file instead - `CONSUL_AGENT_TOKEN_FILE`,
`CONSUL_HTTP_TOKEN_FILE`, ... - for Docker and Swarm secrets; never from
the command line. Every other option has its variable too
(`REGISTRATOR_TAGS`, `REGISTRATOR_CHECK`, ...): `docker-consul-registrator
--help` lists them, each beside its flag. A flag wins over its variable.

[deploy/registrator.env.example](deploy/registrator.env.example) has them
all; copy it to `registrator.env`, fill it in, and use it any way below.
`.gitignore` keeps the filled-in copy out of the repository.

**Docker:**

```
docker run -d --name consul-registrator --restart unless-stopped --network host \
  -v /var/run/docker.sock:/var/run/docker.sock -v consul-agent-data:/consul/data \
  --env-file registrator.env <registry>/docker-consul-registrator:<version>
```

**Podman** - mount its socket instead
(`-v /run/podman/podman.sock:/run/podman/podman.sock`; rootless:
`-v $XDG_RUNTIME_DIR/podman/podman.sock:/run/podman/podman.sock`, with
`systemctl --user enable --now podman.socket`).

**containerd** (nerdctl) - mount its socket and give the container the
host's name: `-v /run/containerd/containerd.sock:/run/containerd/containerd.sock
--hostname "$(hostname)"`. containerd knows nothing of published ports: the
registrator reads them from the label nerdctl writes (`nerdctl/ports`), so
containers made with nerdctl are registered. It reads one namespace
(`REGISTRATOR_CONTAINERD_NAMESPACE`, default `default` - nerdctl's).

**A Swarm** - every node, from [deploy/stack.yml](deploy/stack.yml), on a
manager (or from anywhere with `docker -H ssh://<manager>`):

```
IMAGE=<registry>/docker-consul-registrator:<version> docker stack deploy -c stack.yml consul
```

**Compose, Kubernetes, Nomad, systemd...** - the same: the host's network,
the runtime's socket, a volume for `/consul/data`, and the environment.

The socket is found at its usual path (`/var/run/docker.sock`,
`/run/podman/podman.sock`, `/run/containerd/containerd.sock`) and the
runtime is known by how it answers; `REGISTRATOR_SOCKET` and
`REGISTRATOR_RUNTIME` say otherwise.

**Label what to register:**

```
docker run -d -l consul=yes -p 8080:80 --name web nginx
docker service create --label consul=yes --publish 8181:80 --replicas 3 --name web-svc nginx
```

**Ask DNS** - on any host running it, its agent answers on 127.0.0.1:8600:

```
dig @127.0.0.1 -p 8600 web.service.dmarts.app
```

For every program on the host to resolve these names, have its resolver
send the domain to the agent - with systemd-resolved
(`/etc/systemd/resolved.conf.d/consul.conf`, then `systemctl restart
systemd-resolved`):

```
[Resolve]
DNS=127.0.0.1:8600
Domains=~dmarts.app
```

or with dnsmasq: `server=/dmarts.app/127.0.0.1#8600`. Add your network's
reverse zone too (`~1.168.192.in-addr.arpa`, or
`server=/1.168.192.in-addr.arpa/127.0.0.1#8600`) for PTR answers.

**Swarm services:** only a manager can read the Swarm's services - their
labels and routing-mesh ports - so the registrators on managers write the
services to register into Consul's KV store (`docker-consul-registrator/swarm/`),
and those on workers read them there. Every node must use the same
`REGISTRATOR_LABEL`. A routing-mesh task is registered at its node's IPv4
address and published port, one instance per task, its check passing
while it runs (or following its health check); a task published with
`mode=host` is registered like any container.

## Consul ACLs

The registrator's token, `CONSUL_HTTP_TOKEN` (from a file, it is read again
when the file changes):

```hcl
service_prefix "" { policy = "write" }
agent_prefix "" { policy = "read" }
key_prefix "docker-consul-registrator/" { policy = "write" } # a Swarm's services, shared by its managers
```

The bundled agents' token, `CONSUL_AGENT_TOKEN` (one for all of them):

```hcl
node_prefix "" { policy = "write" }
service_prefix "" { policy = "read" }
```

Anonymous DNS under `default_policy = "deny"` needs a token that may read,
`CONSUL_DNS_TOKEN` (the agents' default token):

```hcl
node_prefix "" { policy = "read" }
service_prefix "" { policy = "read" }
query_prefix "" { policy = "read" }
```

## Options

`docker-consul-registrator --help` lists them all, each with its
environment variable. Beyond the ones above: `REGISTRATOR_TAGS` (Consul
tags for every service), `REGISTRATOR_ADDRESS` (the address to register),
`REGISTRATOR_CHECK`, `REGISTRATOR_DRY_RUN` (log what would change, change
nothing), `REGISTRATOR_DEBUG`.

## Security

- Access to a runtime's socket is root on that host. Run the registrator
  where you would run root; for Docker, a read-only socket proxy can stand in
  front of it (`--socket tcp://proxy:2375`).
- Whatever comes in is checked first: flags, Docker's and containerd's
  answers (names, labels, ports, addresses), Consul's answers and the token
  file. Text from outside is cleaned before it reaches a log line.
- Secrets - tokens and the gossip key - come from the environment or a file,
  never the command line, and are never logged; the agent's configuration
  holding them is a file only the registrator can read. Prefer the `_FILE`
  forms with Docker or Swarm secrets: a value in the environment shows in
  `docker inspect`.

## Building

```
make check       # format, vet, tests with the race detector, golangci-lint, govulncheck, the docs page
make dist        # static Linux binaries, amd64 and arm64
make image       # the container image
make docs-serve  # the documentation page at http://127.0.0.1:8000
```

The documentation page, [docs/index.html](docs/index.html), is one
self-contained file. The Pages workflow checks it and publishes it on GitHub
Pages whenever it changes on `main`.

The image copies the Consul agent from HashiCorp's image, with its licence
(BUSL-1.1).
