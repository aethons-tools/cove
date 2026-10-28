# jam-studio-base — the universal lean floor every org repo can build on.
#
# OS + git/gh/sshd + the egress stack + the Docker engine (for the opt-in
# docker-in-sandbox capability — present but INERT here) + core utilities. It
# deliberately EXCLUDES language toolchains, chrome, and java — those live in
# jam-studio, which builds FROM this. It also carries no in-studio binary: jam injects
# the exact version-locked jam-studio-agent into its sealed hardening layer
# (COV-42/COV-44), so the base is pure tools with no Go build dependency.
# See docs/superpowers/specs/2026-07-16-shared-base-image-design.md and, for the
# Docker engine, docs/superpowers/specs/2026-08-08-sysbox-docker-in-sandbox-design.md.
FROM ubuntu:24.04@sha256:68ee22004a9109b44973313b02ff7b1de250f1de0f5ecb628d73f579aaff33ac
LABEL org.opencontainers.image.source="https://github.com/aethons-tools/cove"
LABEL org.opencontainers.image.description="jam studio base image: OS + git/gh/sshd + egress stack + Docker engine (pure tools)"

# Base tooling from the Ubuntu archive: sshd, git, the GitHub CLI, curl, the
# egress stack (nftables, squid), systemd (PID-1 init for docker-enabled kits;
# inert otherwise), and core utilities. No language toolchains.
# Versions are pinned for reproducibility (Renovate keeps them current — see
# renovate.json). They are identical across amd64/arm64. A pin that ages out of
# the Ubuntu archive fails the build loudly; bump it via the Renovate PR.
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      openssh-server=1:9.6p1-3ubuntu13.20 \
      ca-certificates=20260816ubuntu0.24.04.1 \
      curl=8.5.0-2ubuntu10.16 \
      git=1:2.43.0-1ubuntu7.4 \
      gh=2.45.0-1ubuntu0.3 \
      nftables=1.0.9-1ubuntu0.1 \
      squid=6.14-0ubuntu0.24.04.5 \
      sudo=1.9.15p5-3ubuntu5.24.04.4 \
      systemd=255.4-1ubuntu8.18 \
      systemd-sysv=255.4-1ubuntu8.18 \
      jq=1.7.1-3ubuntu0.24.04.2 \
      gnupg=2.4.4-2ubuntu17.6 \
      xz-utils=5.6.1+really5.4.5-1ubuntu0.4 \
      unzip=6.0-28ubuntu4.1 \
      python3=3.12.3-0ubuntu2.1  \
      shellcheck=0.9.0-1 \
      just=1.45.0-1 \
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
      docker-compose-plugin=5.5.1-1~ubuntu.24.04~noble \
 && rm -rf /var/lib/apt/lists/*

# code-server: the built-in Studio IDE (docs/superpowers/specs/2026-10-25-session-studio-view-design.md).
# Renovate bumps CODE_SERVER_VERSION; update both SHA-256s with it (the build fails on a mismatch).
ARG CODE_SERVER_VERSION=4.143.1
ARG CODE_SERVER_SHA256_AMD64=e4b771c98fafbe2af3990ecd71d93ba22515588db550007076768a014ae34c02
ARG CODE_SERVER_SHA256_ARM64=79ca8df2db219b24de75e2a830c5302808124d8c2bf132f4bc6105cea834bb1c
RUN set -eux; arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64) sum="$CODE_SERVER_SHA256_AMD64";; arm64) sum="$CODE_SERVER_SHA256_ARM64";; *) echo "unsupported $arch"; exit 1;; esac; \
    curl -fsSL -o /tmp/cs.deb "https://github.com/coder/code-server/releases/download/v${CODE_SERVER_VERSION}/code-server_${CODE_SERVER_VERSION}_${arch}.deb"; \
    printf "%s  /tmp/cs.deb\n" "$sum" > /tmp/cs.sha256; sha256sum -c /tmp/cs.sha256; rm -f /tmp/cs.sha256; \
    apt-get install -y --no-install-recommends /tmp/cs.deb; rm -f /tmp/cs.deb; \
    code-server --version

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

# The agent's default docs and skills: /home/agent/.init-agent-data is the seed
# the sealed entrypoint copies into /agent-data (CLAUDE_CONFIG_DIR) on first
# boot — CLAUDE.md and its includes (SANDBOX.md is the plain sandbox
# guide; a Jam session gets those rules from its session context instead),
# reference/ and skills/, plus the startup defaults (settings.json,
# .claude.json). Its .refresh manifest names the entries re-copied on every boot
# (the image-owned docs; never the runtime-owned settings or COLLABORATOR.md).
# They live here, not in the sealed hardening layer, so a kit overrides them by
# COPY; hardening owns only the seeding mechanism.
COPY image-files/. /.

RUN chown -R agent:agent /home/agent \
 && chmod 0755 /home/agent/.init-agent-data/skills/docs-audit/scripts/docs_audit.py
