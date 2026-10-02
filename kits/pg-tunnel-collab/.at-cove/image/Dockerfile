# pg-tunnel-collab image — the blessed cove base plus the wstunnel client and the
# pg-tunnel-up launcher. at-cove injects the blessed base via --build-arg when it
# builds this kit; the default below is only for a bare manual `docker build`.
ARG COVE_BASE_IMAGE=ghcr.io/aethons-tools/cove-image:latest
FROM ${COVE_BASE_IMAGE}

# Build-time egress is open. Pin the wstunnel release + verify its checksum so
# the build is reproducible and supply-chain-honest.
ARG WSTUNNEL_VERSION=11.0.0
ARG WSTUNNEL_SHA256=9708a99717b5a951453c2ff7c14c25d3418d02ca7fcb96fdb382a8f2083bab5e
RUN curl -fsSL -o /tmp/wstunnel.tar.gz \
      "https://github.com/erebe/wstunnel/releases/download/v${WSTUNNEL_VERSION}/wstunnel_${WSTUNNEL_VERSION}_linux_amd64.tar.gz" \
 && echo "${WSTUNNEL_SHA256}  /tmp/wstunnel.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/wstunnel.tar.gz -C /usr/local/bin wstunnel \
 && chmod +x /usr/local/bin/wstunnel \
 && rm /tmp/wstunnel.tar.gz \
 && wstunnel --version

# The idempotent launcher the db-analyst session runs before touching the DB.
COPY pg-tunnel-up /usr/local/bin/pg-tunnel-up
RUN chmod +x /usr/local/bin/pg-tunnel-up
