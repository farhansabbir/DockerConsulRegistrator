# docker-consul-registrator

Registers your containers in Consul so its DNS answers for them: **A, AAAA,
SRV and PTR**. Label a container `consul=yes` and publish a port; the
registrator does the rest, and removes the registration when the container
stops.

It works with **Docker, Podman and containerd** alike, and finds out which
one it is talking to by itself.

## What it does

For every running container labelled `consul=yes`, it registers one Consul
service instance **per published port and address family**:

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
- a Swarm task: its service's name (`mystack_web` becomes `mystack-web`);
- a Compose container: `<project>-<service>`;
- otherwise the container's name;
- a container publishing more than one port gets one service per port:
  `web-80`, `web-443`.

Each service is tagged with its protocol (`tcp`, `udp`), so
`_dns._udp.service.dmarts.app` finds the UDP ones; `--tags` adds your own.

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

**Swarm**, as a global service - see [deploy/swarm.sh](deploy/swarm.sh):

```
docker service create --name consul-registrator --mode global --network host \
  --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
  --mount type=volume,src=consul-agent-data,dst=/consul/data \
  --config src=consul-agent,target=/consul/config/agent.hcl \
  --secret src=consul-agent-secrets,target=/consul/config/secrets.hcl \
  --secret src=consul-registrator-token,target=registrator.token \
  docker-consul-registrator:VERSION --agent --token-file /run/secrets/registrator.token --domain dmarts.app
```

Swarm's routing-mesh ports can only be read on a manager; on workers,
publish with `mode=host` (each task is then registered at its own node).

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
make check     # format, vet, tests with the race detector, golangci-lint, govulncheck
make dist      # static Linux binaries, amd64 and arm64
make image     # the container image
```

The image copies the Consul agent from HashiCorp's image, with its licence
(BUSL-1.1).
