# cove-base-image — the universal lean floor every org repo can build on.
#
# OS + git/gh/sshd + the egress stack + core utilities + at-task (the generic
# dispatched-worker harness, built from this repo and baked in per-arch by the
# build workflow). It deliberately EXCLUDES language toolchains, chrome, and
# java — those live in cove-image, which builds FROM this.
# See docs/superpowers/specs/2026-07-16-shared-base-image-design.md.
FROM ubuntu:24.04
LABEL org.opencontainers.image.source="https://github.com/aethons-tools/cove"
LABEL org.opencontainers.image.description="cove base image: OS + git/gh/sshd + egress stack + at-task"

# Base tooling: sshd, git, the GitHub CLI, curl, the egress stack (nftables,
# squid, podman + rootless plumbing), and core utilities. No language toolchains.
# (apt versions are unpinned in this first increment; pinning + Renovate land in
# a follow-on PR — see COV-33. DL3008 is ignored in .hadolint.yaml until then.)
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      openssh-server ca-certificates curl git gh nftables squid sudo \
      podman podman-docker uidmap fuse-overlayfs slirp4netns \
      jq gnupg xz-utils unzip python3 \
 && rm -rf /var/lib/apt/lists/*

# Non-root user the agent runs as (so --dangerously-skip-permissions is allowed).
RUN useradd -m -s /bin/bash agent

# sshd: key-only auth, no root login, listen on 2222 (the host may use port 22).
RUN mkdir -p /run/sshd \
 && sed -i 's/^#\?Port .*/Port 2222/'                                   /etc/ssh/sshd_config \
 && sed -i 's/^#\?PasswordAuthentication .*/PasswordAuthentication no/' /etc/ssh/sshd_config \
 && sed -i 's/^#\?PermitRootLogin .*/PermitRootLogin no/'               /etc/ssh/sshd_config

# at-task: the generic dispatched-worker harness. The build workflow compiles it
# from this repo's source for the native arch and stages it next to this
# Dockerfile (gitignored) before the build.
COPY at-task /usr/local/bin/at-task
RUN chmod 0755 /usr/local/bin/at-task
