# The connector image packages the pre-built binary (see `make dist`) onto a tiny
# multi-arch base that already carries git (the agent shells out to git) + CA certs.
# No in-image compilation and NO RUN step on purpose: a COPY-only build assembles a
# multi-arch manifest without an emulated sandbox, so it works even on hosts whose
# buildkit can't set up /proc masking for emulated RUN.
#
#   make dist                                   # produces dist/connector-linux-*
#   docker buildx build --platform linux/amd64,linux/arm64 --push .   # (make docker)
FROM alpine/git:latest
ARG TARGETARCH
# The right binary per target arch (buildx sets TARGETARCH to amd64 / arm64).
COPY dist/connector-linux-${TARGETARCH} /usr/local/bin/connector
# State (keypair + tokens) lives here; mount a volume to persist across restarts.
ENV CONNECTOR_STATE_DIR=/var/lib/connector
WORKDIR /var/lib/connector
ENTRYPOINT ["connector"]
CMD ["run", "--config", "/etc/connector/connector.yaml"]
