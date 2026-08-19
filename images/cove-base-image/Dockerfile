# cove-base-image — the universal lean floor every org repo can build on.
#
# OS + git/gh/sshd + the egress stack + the Docker engine (for the opt-in
# docker-in-sandbox capability — present but INERT here) + core utilities. It
# deliberately EXCLUDES language toolchains, chrome, and java — those live in
# cove-image, which builds FROM this. It also carries no at-task: at-cove injects
# the exact version-locked at-task into its sealed hardening layer (COV-42/COV-44),
# so the base is pure tools with no Go build dependency.
# See docs/superpowers/specs/2026-07-16-shared-base-image-design.md and, for the
# Docker engine, docs/superpowers/specs/2026-08-08-sysbox-docker-in-sandbox-design.md.
FROM ubuntu:24.04@sha256:d78ab76437b1afc5f01e223d6bf0172763f404bb166441328845adbef44518cb
LABEL org.opencontainers.image.source="https://github.com/aethons-tools/cove"
LABEL org.opencontainers.image.description="cove base image: OS + git/gh/sshd + egress stack + Docker engine (pure tools)"

# Base tooling from the Ubuntu archive: sshd, git, the GitHub CLI, curl, the
# egress stack (nftables, squid), systemd (PID-1 init for docker-enabled kits;
# inert otherwise), and core utilities. No language toolchains.
# Versions are pinned for reproducibility (Renovate keeps them current — see
# renovate.json). They are identical across amd64/arm64. A pin that ages out of
# the Ubuntu archive fails the build loudly; bump it via the Renovate PR.
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      openssh-server=1:9.6p1-3ubuntu13.18 \
      ca-certificates=20260601~24.04.1 \
      curl=8.5.0-2ubuntu10.12 \
      git=1:2.43.0-1ubuntu7.3 \
      gh=2.45.0-1ubuntu0.3 \
      nftables=1.0.9-1ubuntu0.1 \
      squid=6.14-0ubuntu0.24.04.4 \
      sudo=1.9.15p5-3ubuntu5.24.04.2 \
      systemd=255.4-1ubuntu8.17 \
      systemd-sysv=255.4-1ubuntu8.17 \
      jq=1.7.1-3ubuntu0.24.04.2 \
      gnupg=2.4.4-2ubuntu17.4 \
      xz-utils=5.6.1+really5.4.5-1ubuntu0.3 \
      unzip=6.0-28ubuntu4.1 \
      python3=3.12.3-0ubuntu2.1  \
      shellcheck=0.9.0-1 \
      just=1.21.0-1 \
 && rm -rf /var/lib/apt/lists/*

# Docker engine, from Docker's official apt repo, for the opt-in docker-in-sandbox
# capability. Present but INERT here: no daemon is started and no run-flags or
# systemd units are wired (those live in the config/backend/entrypoint layers — a
# kit opts in with `docker: true`, which runs the sandbox under the Sysbox runtime).
# See docs/superpowers/specs/2026-08-08-sysbox-docker-in-sandbox-design.md §B.
# Pinned for reproducibility (Renovate; the docker.list repo). Docker publishes the
# same version string for amd64/arm64.
RUN install -m 0755 -d /etc/apt/keyrings \
 && curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc \
 && chmod a+r /etc/apt/keyrings/docker.asc \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable" \
      > /etc/apt/sources.list.d/docker.list \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      docker-ce=5:29.7.2-1~ubuntu.24.04~noble \
      docker-ce-cli=5:29.7.2-1~ubuntu.24.04~noble \
      containerd.io=2.3.3-1~ubuntu.24.04~noble \
      docker-buildx-plugin=0.36.1-1~ubuntu.24.04~noble \
      docker-compose-plugin=5.5.0-1~ubuntu.24.04~noble \
 && rm -rf /var/lib/apt/lists/*

# sshd: key-only auth, no root login, listen on 2222 (the host may use port 22).
RUN mkdir -p /run/sshd \
 && sed -i 's/^#\?Port .*/Port 2222/'                                   /etc/ssh/sshd_config \
 && sed -i 's/^#\?PasswordAuthentication .*/PasswordAuthentication no/' /etc/ssh/sshd_config \
 && sed -i 's/^#\?PermitRootLogin .*/PermitRootLogin no/'               /etc/ssh/sshd_config

# Non-root user the agent runs as (so --dangerously-skip-permissions is allowed).
# It joins the `docker` group (created by the docker-ce install above) so it can
# reach the root-owned /var/run/docker.sock when a docker-enabled kit starts the
# inner daemon. Inert for non-docker kits (no daemon, so no socket).
RUN useradd -m -s /bin/bash agent \
 && usermod -aG docker agent

COPY image-files/. /.

RUN chown -R agent:agent /home/agent
