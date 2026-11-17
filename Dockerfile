# syntax=docker/dockerfile:1

# The registrator with a Consul agent in the same container: the registrator
# starts the agent (--agent, the default command), registers with it only,
# and the agent keeps the Consul servers in step.
#
# The binaries are built outside (make image, or the Image workflow) and the
# one for the platform being built is copied in. The Consul agent is copied
# from HashiCorp's image, with its licence (BUSL-1.1).
#
# It runs as root: the sockets of Podman and containerd are root's, and
# Docker's socket is root's in all but name. Run it with the host's network
# and the runtime's socket mounted - see README.md.
FROM hashicorp/consul:2.1.0@sha256:259fd02f2544fe1aa03fa49efd3b105c8dd67106d710a72f5e898110266356f7 AS consul

FROM gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2

ARG TARGETARCH

COPY --from=consul /bin/consul /bin/consul
COPY --from=consul /usr/share/doc/consul/LICENSE.txt /usr/share/doc/consul/LICENSE.txt
COPY --from=consul /consul/config /consul/config
COPY --from=consul /consul/data /consul/data
COPY dist/docker-consul-registrator-linux-${TARGETARCH} /usr/local/bin/docker-consul-registrator

USER 0:0
VOLUME ["/consul/data"]
ENTRYPOINT ["/usr/local/bin/docker-consul-registrator"]
CMD ["--agent"]
