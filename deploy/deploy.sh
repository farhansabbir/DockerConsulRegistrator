#!/bin/sh
# Starts the registrator - with its Consul agent - where it runs: on every
# node of the Swarm when this host is a Swarm manager, otherwise on this host
# alone, with Docker, Podman or nerdctl (containerd).
#
#   cp agent.hcl.example agent.hcl          # your Consul servers, datacenter
#   cp secrets.hcl.example secrets.hcl      # gossip key, agent and DNS tokens
#   printf '%s\n' "<the registrator's token>" > registrator.token
#   IMAGE=<registry>/docker-consul-registrator:<version> DOMAIN=dmarts.app ./deploy.sh
#
# Run it again after changing a file or the image: it replaces what it
# started.
#
#   IMAGE      the registrator's image (required)
#   DOMAIN     the DNS domain to serve the services in (default: consul)
#   DOCKER     the command line to use: docker, podman or nerdctl (default: docker)
#   STACK      the stack's name on a Swarm (default: consul)
#   DRY_RUN=1  print the commands; run nothing
set -eu
cd "$(dirname "$0")"
: "${IMAGE:?set IMAGE to the image of the registrator}"
DOMAIN=${DOMAIN:-consul}
DOCKER=${DOCKER:-docker}
STACK=${STACK:-consul}
NAME=consul-registrator

run() {
  if [ "${DRY_RUN:-}" = 1 ]; then
    printf '%s\n' "$*"
  else
    "$@"
  fi
}

for f in agent.hcl secrets.hcl registrator.token; do
  if [ ! -s "$f" ]; then
    echo "$PWD/$f is missing: write it first (see $f.example where there is one)" >&2
    exit 1
  fi
done
if grep -q REPLACE secrets.hcl; then
  echo "$PWD/secrets.hcl still holds REPLACE placeholders" >&2
  exit 1
fi
chmod 600 secrets.hcl registrator.token

if [ "$DOCKER" = docker ] && [ "$(docker info --format '{{.Swarm.ControlAvailable}}' 2>/dev/null)" = true ]; then
  # A Swarm manager: the stack, one task per node. Swarm configs and secrets
  # cannot change, so they are named after their content - a changed file is
  # a new one, and the service moves to it.
  version() { { sha256sum "$1" 2>/dev/null || shasum -a 256 "$1"; } | cut -c1-12; }
  AGENT_VERSION=$(version agent.hcl)
  SECRETS_VERSION=$(version secrets.hcl)
  TOKEN_VERSION=$(version registrator.token)
  export IMAGE DOMAIN AGENT_VERSION SECRETS_VERSION TOKEN_VERSION
  run docker stack deploy --detach=true -c stack.yml "$STACK"
  # Earlier versions the service no longer uses; one still in use stays.
  for kind in config secret; do
    for old in $(docker "$kind" ls --format '{{.Name}}' | grep "^$NAME-"); do
      case $old in
        *-"$AGENT_VERSION" | *-"$SECRETS_VERSION" | *-"$TOKEN_VERSION") ;;
        *) run docker "$kind" rm "$old" >/dev/null 2>&1 || true ;;
      esac
    done
  done
  echo "deployed: docker stack services $STACK"
  exit 0
fi

# One host. The socket is mounted where the registrator looks for it.
extra=""
case $DOCKER in
  docker) sock=/var/run/docker.sock ;;
  podman)
    sock=/run/podman/podman.sock
    [ -S "$sock" ] || sock=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/podman/podman.sock
    ;;
  nerdctl) sock=/run/containerd/containerd.sock ;;
  *)
    echo "DOCKER=$DOCKER: docker, podman or nerdctl" >&2
    exit 2
    ;;
esac
if [ ! -S "$sock" ] && [ "${DRY_RUN:-}" != 1 ]; then
  echo "no socket at $sock: is $DOCKER's daemon running (podman: systemctl enable --now podman.socket)?" >&2
  exit 1
fi
run "$DOCKER" rm -f "$NAME" >/dev/null 2>&1 || true
# shellcheck disable=SC2086 # $extra is empty or whole words
run "$DOCKER" run -d --name "$NAME" --restart unless-stopped --network host --hostname "$(hostname)" $extra \
  -v "$sock:$sock" \
  -v consul-agent-data:/consul/data \
  -v "$PWD/agent.hcl:/consul/config/agent.hcl:ro" \
  -v "$PWD/secrets.hcl:/consul/config/secrets.hcl:ro" \
  -v "$PWD/registrator.token:/run/secrets/registrator.token:ro" \
  "$IMAGE" --agent --token-file /run/secrets/registrator.token --domain "$DOMAIN"
echo "started: $DOCKER logs -f $NAME"
