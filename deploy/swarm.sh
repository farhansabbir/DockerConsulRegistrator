#!/bin/sh
# Runs the registrator - with its bundled Consul agent - as a global Swarm
# service: one task on every node, on the host's network, with the node's
# Docker socket and a volume for the agent's state.
#
# Run it where the Consul secrets are (the files below) and the Swarm
# manager and nodes are reachable over ssh. It replaces an earlier
# deployment of itself. The secrets go from their files into Swarm secrets
# and are never printed.
#
#   IMAGE    the image, loaded on every node      (docker-consul-registrator:dev)
#   DOMAIN   the DNS domain to serve              (dmarts.app)
#   DC       Consul datacenter                    (dc1)
#   JOIN     Consul servers to join, spaces       (required)
#   MANAGER  a Swarm manager, for ssh             (required)
#   NODES    nodes to load the image on, for ssh  (the manager)
#   SECRETS  directory with gossip.key, agent.token, dns.token,
#            registrator.token                    ($HOME/consul-lab/secrets)
set -eu
IMAGE=${IMAGE:-docker-consul-registrator:dev}
DOMAIN=${DOMAIN:-dmarts.app}
DC=${DC:-dc1}
JOIN=${JOIN:?JOIN=<Consul server addresses> is needed}
MANAGER=${MANAGER:?MANAGER=<ssh host of a Swarm manager> is needed}
NODES=${NODES:-$MANAGER}
S=${SECRETS:-$HOME/consul-lab/secrets}
NAME=consul-registrator

for f in gossip.key agent.token dns.token registrator.token; do
  [ -s "$S/$f" ] || { echo "$S/$f is missing or empty"; exit 1; }
done

for n in $NODES; do
  echo "loading $IMAGE on $n"
  docker save "$IMAGE" | ssh "$n" docker load -q
done

# Swarm configs and secrets cannot change: the earlier deployment goes first.
ssh "$MANAGER" "docker service rm $NAME >/dev/null 2>&1 || true
  for i in \$(seq 1 30); do
    docker secret rm $NAME-agent-secrets $NAME-token >/dev/null 2>&1
    docker config rm $NAME-agent >/dev/null 2>&1
    [ -z \"\$(docker secret ls -q --filter name=$NAME-)\$(docker config ls -q --filter name=$NAME-)\" ] && break
    sleep 1
  done"

join=$(for a in $JOIN; do printf '"%s", ' "$a"; done | sed 's/, $//')
ssh "$MANAGER" docker config create $NAME-agent - >/dev/null <<EOF
datacenter         = "$DC"
retry_join         = [$join]
client_addr        = "127.0.0.1"
leave_on_terminate = true
dns_config { only_passing = true }
ports {
  grpc     = -1
  grpc_tls = -1
}
acl {
  enabled                  = true
  default_policy           = "deny"
  enable_token_persistence = true
}
EOF
printf 'encrypt = "%s"\nacl {\n  tokens {\n    agent   = "%s"\n    default = "%s"\n  }\n}\n' \
  "$(cat "$S/gossip.key")" "$(cat "$S/agent.token")" "$(cat "$S/dns.token")" |
  ssh "$MANAGER" docker secret create $NAME-agent-secrets - >/dev/null
ssh "$MANAGER" docker secret create $NAME-token - >/dev/null <"$S/registrator.token"

ssh "$MANAGER" docker service create --name $NAME --mode global --network host --no-resolve-image --detach \
  --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
  --mount type=volume,src=consul-agent-data,dst=/consul/data \
  --config src=$NAME-agent,target=/consul/config/agent.hcl,mode=0444 \
  --secret src=$NAME-agent-secrets,target=/consul/config/secrets.hcl,mode=0400 \
  --secret src=$NAME-token,target=registrator.token,mode=0400 \
  --restart-condition any --restart-delay 10s --update-order stop-first \
  "$IMAGE" --agent --token-file /run/secrets/registrator.token --domain "$DOMAIN" >/dev/null
echo "service $NAME created"
sleep 5
ssh "$MANAGER" docker service ps $NAME --format "'{{.Node}} {{.CurrentState}} {{.Error}}'"
