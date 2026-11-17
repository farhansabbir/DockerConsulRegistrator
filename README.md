# docker-consul-registrator

Registers your containers in Consul so its DNS answers for them: **A, AAAA,
SRV and PTR**. Label a container `consul=yes` and publish a port; the
registrator does the rest, and removes the registration when the container
stops.

It works with **Docker, Podman and containerd** alike, and finds out which
one it is talking to by itself.

## What it does

What is labelled `consul=yes` is registered: a container (`docker run -l`),
or a Swarm service - on the service (`docker service create --label`) or on
its containers (`--container-label`). Every node registers the containers
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

The image carries a Consul agent. With `--agent` (the image's default), the
registrator starts it in the same container, registers with it only, and
the agent's anti-entropy keeps the Consul servers in step. The registrator
works out what the agent needs from the host:

- **node name:** the host's name;
- **address:** the host's Swarm address, else the default route's
  (`--agent-bind`);
- **IPv6:** the stable global address beside it, for AAAA answers;
- **DNS domain:** `--domain`, made the agent's own domain - PTR answers
  name things in it - with `consul` kept as its alternative.

Everything else - servers to join, the gossip key, ACL tokens, TLS - is
ordinary Consul configuration in `/consul/config` (`--agent-config-dir`).
Keep `/consul/data` on a volume.

Without `--agent`, it registers with whichever agent `--consul` names. It
cannot change that agent's DNS domain, so with `--domain` it checks the
agent serves it, and refuses to start, saying what to add, when it does not.

## Running it

It needs the host's network (`--network host`) and the runtime's socket at
the usual path; `--socket auto` (the default) finds it there:

| Runtime | Mount |
|---|---|
| Docker | `-v /var/run/docker.sock:/var/run/docker.sock` |
| Podman (rootful) | `-v /run/podman/podman.sock:/run/podman/podman.sock` |
| Podman (rootless) | `-v $XDG_RUNTIME_DIR/podman/podman.sock:/run/podman/podman.sock` |
| containerd | `-v /run/containerd/containerd.sock:/run/containerd/containerd.sock --uts host` |

The socket is identified by how it answers: the Engine API's `/_ping` for
Docker and Podman (Podman says Libpod), containerd's gRPC version call for
containerd. `--runtime` insists on one.

**containerd** knows nothing of published ports: the registrator reads them
from the label nerdctl writes (`nerdctl/ports`), so containers made with
nerdctl are registered and others are reported. It reads one namespace
(`--containerd-namespace`, default `default` - nerdctl's). It takes the
host's name from its own, hence `--uts host`.

**Swarm**, on every node - [deploy/stack.yml](deploy/stack.yml):

```
cd deploy
cp agent.hcl.example agent.hcl        # your Consul servers, datacenter
cp secrets.hcl.example secrets.hcl    # gossip key, agent and DNS tokens
printf '%s\n' "<the registrator's token>" > registrator.token
IMAGE=<registry>/docker-consul-registrator:<version> DOMAIN=dmarts.app \
  docker stack deploy -c stack.yml consul
```

The three files become a Swarm config and Swarm secrets; `.gitignore` keeps
the filled-in copies out of the repository. Run it on a manager, or from
anywhere with `docker -H ssh://<manager>`. Swarm cannot change a config or
secret in place: after editing one, `docker stack rm consul` and deploy
again.

Only a Swarm manager can read the Swarm's services - their labels and
routing-mesh ports - so the registrators on managers write the services to
register into Consul's KV store (`docker-consul-registrator/swarm/`), and
those on workers read them from there. A routing-mesh task is registered
at its node's IPv4 address and published port, one instance per task, its
check passing while it runs (or following its health check); a task
published with `mode=host` is registered like any container.

**A single host:**

```
docker run -d --name consul-registrator --network host --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock -v consul-agent-data:/consul/data \
  -v /etc/consul-agent:/consul/config:ro \
  docker-consul-registrator:VERSION --agent --token-file /consul/config/registrator.token --domain dmarts.app
```

## Consul ACLs

The registrator's token (`--token-file`, read again when the file changes):

```hcl
service_prefix "" { policy = "write" }
agent_prefix "" { policy = "read" }
key_prefix "docker-consul-registrator/" { policy = "write" } # a Swarm's services, shared by its managers
```

The bundled agents' token (`acl.tokens.agent`):

```hcl
node_prefix "" { policy = "write" }
service_prefix "" { policy = "read" }
```

Anonymous DNS under `default_policy = "deny"` needs a token that may read,
set as the agents' `acl.tokens.default`:

```hcl
node_prefix "" { policy = "read" }
service_prefix "" { policy = "read" }
query_prefix "" { policy = "read" }
```

## Options

`docker-consul-registrator --help` lists them all. The ones you will reach
for: `--socket`, `--runtime`, `--consul`, `--token-file`, `--domain`,
`--tags`, `--address`, `--check`, `--ignore` (dry run: logs what would
change), `--agent`, `--debug`.

## Security

- Access to a runtime's socket is root on that host. Run the registrator
  where you would run root; for Docker, a read-only socket proxy can stand in
  front of it (`--socket tcp://proxy:2375`).
- Whatever comes in is checked first: flags, Docker's and containerd's
  answers (names, labels, ports, addresses), Consul's answers and the token
  file. Text from outside is cleaned before it reaches a log line.
- The ACL token is read from a file only, never from the command line, and
  never logged.

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
