# cove-base-image — the universal lean floor every org repo can build on.
#
# OS + git/gh/sshd + the egress stack + core utilities. It deliberately EXCLUDES
# language toolchains, chrome, and java — those live in cove-image, which builds
# FROM this. It also carries no at-task: at-cove injects the exact version-locked
# at-task into its sealed hardening layer (COV-42/COV-44), so the base is pure
# tools with no Go build dependency.
# See docs/superpowers/specs/2026-07-16-shared-base-image-design.md.
FROM ubuntu:24.04@sha256:4fbb8e6a8395de5a7550b33509421a2bafbc0aab6c06ba2cef9ebffbc7092d90
LABEL org.opencontainers.image.source="https://github.com/aethons-tools/cove"
LABEL org.opencontainers.image.description="cove base image: OS + git/gh/sshd + egress stack (pure tools)"

# Base tooling: sshd, git, the GitHub CLI, curl, the egress stack (nftables,
# squid, podman + rootless plumbing), and core utilities. No language toolchains.
# Versions are pinned for reproducibility (Renovate keeps them current — see
# renovate.json). They are identical across amd64/arm64. A pin that ages out of
# the Ubuntu archive fails the build loudly; bump it via the Renovate PR.
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      openssh-server=1:9.6p1-3ubuntu13.18 \
      ca-certificates=20260601~24.04.1 \
      curl=8.5.0-2ubuntu10.11 \
      git=1:2.43.0-1ubuntu7.3 \
      gh=2.45.0-1ubuntu0.3 \
      nftables=1.0.9-1ubuntu0.1 \
      squid=6.14-0ubuntu0.24.04.4 \
      sudo=1.9.15p5-3ubuntu5.24.04.2 \
      podman=4.9.3+ds1-1ubuntu0.2 \
      podman-docker=4.9.3+ds1-1ubuntu0.2 \
      uidmap=1:4.13+dfsg1-4ubuntu3.2 \
      fuse-overlayfs=1.13-1 \
      slirp4netns=1.2.1-1build2 \
      jq=1.7.1-3ubuntu0.24.04.2 \
      gnupg=2.4.4-2ubuntu17.4 \
      xz-utils=5.6.1+really5.4.5-1ubuntu0.3 \
      unzip=6.0-28ubuntu4.1 \
      python3=3.12.3-0ubuntu2.1 \
 && rm -rf /var/lib/apt/lists/*

# Non-root user the agent runs as (so --dangerously-skip-permissions is allowed).
RUN useradd -m -s /bin/bash agent

# Overridable startup defaults (COV-34): the agent's first-boot settings and the
# base .claude.json, seeded into /agent-data on first boot. at-cove's sealed
# hardening layer no longer ships these (it COPYs only sealed files); a consumer
# kit's Dockerfile can replace either by writing its own.
COPY --chown=agent:agent init-agent-data/ /home/agent/.init-agent-data/

# Session env drop-in (COV-34): the sealed layer concatenates /etc/cove/env.d/*.env
# into /etc/environment. 00-base.env owns PATH, CLAUDE_CONFIG_DIR, and the egress
# proxy vars; a kit Dockerfile may drop higher-numbered fragments to add its own.
COPY cove-env.d/ /etc/cove/env.d/

# sshd: key-only auth, no root login, listen on 2222 (the host may use port 22).
RUN mkdir -p /run/sshd \
 && sed -i 's/^#\?Port .*/Port 2222/'                                   /etc/ssh/sshd_config \
 && sed -i 's/^#\?PasswordAuthentication .*/PasswordAuthentication no/' /etc/ssh/sshd_config \
 && sed -i 's/^#\?PermitRootLogin .*/PermitRootLogin no/'               /etc/ssh/sshd_config
