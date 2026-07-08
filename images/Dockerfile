FROM ubuntu:24.04 AS base
ARG TARGETARCH
ARG GO_VERSION=1.26.4
ARG JDK_VERSION="25"
ARG NODE_MAJOR_VERSION="22"
LABEL authors="Aethon's Tools"

# Base tooling: sshd, git, the GitHub CLI, curl (for the installer), and the
# egress stack. Node/npm are intentionally NOT installed: the native Claude Code
# binary ships its own runtime, and the npm install path is deprecated as of
# v2.1.15. gh comes from Ubuntu 24.04's own repos (no extra apt source needed).
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      openssh-server ca-certificates curl git gh nftables squid sudo \
      podman podman-docker uidmap fuse-overlayfs slirp4netns shellcheck \
      jq just python3 gnupg xz-utils unzip \
 && rm -rf /var/lib/apt/lists/*

# Non-root user the agent runs as (so --dangerously-skip-permissions is allowed)
RUN useradd -m -s /bin/bash agent

# sshd: key-only auth, no root login, listen on 2222
# (Fly already uses port 22 on the machine's private interface)
RUN mkdir -p /run/sshd \
    && sed -i 's/^#\?Port .*/Port 2222/'                       /etc/ssh/sshd_config \
    && sed -i 's/^#\?PasswordAuthentication .*/PasswordAuthentication no/' /etc/ssh/sshd_config \
    && sed -i 's/^#\?PermitRootLogin .*/PermitRootLogin no/'    /etc/ssh/sshd_config

COPY /base-install-files/. /base-install-files/.
WORKDIR /base-install-files
RUN echo "$(pwd)"
RUN ./install-go.sh
RUN ./install-java.sh
RUN ./install-node.sh
RUN ./install-chrome.sh
WORKDIR /
RUN rm -rf /base-install-files



FROM base AS agent

# Pre-install Claude Code (native) at BUILD time, as the agent user.
# Fly's builders have open network, so this fetch happens BEFORE the runtime
# egress lock. The desktop app then finds 'claude' already present instead of
# bootstrapping through the locked-down proxy.
RUN su - agent -c 'curl -fsSL https://claude.ai/install.sh | bash'

# Make the binary findable regardless of how sshd sets PATH for the session.
RUN ln -sf /home/agent/.local/bin/claude /usr/local/bin/claude


# Don't phone home for background updates from inside the sandbox; rebuild the
# image to update Claude Code instead. (claude.ai is still reached at runtime for
# the subscription OAuth login flow — see the squid allow-list.) Use
# DISABLE_UPDATES=1 to also block manual updates.
ENV DISABLE_AUTOUPDATER=1


COPY image-files/. /.
# `COPY image-files/. /.` carries home/agent/ directory entries (for .ssh and
# .init-agent-data), and Docker stamps the build context's root ownership onto
# the pre-existing /home/agent — leaving the agent unable to write to its own
# $HOME. Restore it (and .ssh, whose contents COPY also re-owned).
RUN chmod +x /usr/local/bin/entrypoint.sh \
 && chown agent:agent /home/agent \
 && chown -R agent:agent /home/agent/.ssh \
 && chmod 700 /home/agent/.ssh \
 && chmod 600 /home/agent/.ssh/authorized_keys
# && find /etc/claude-code -type d -exec chmod 0755 {} + \
# && find /etc/claude-code -type f -exec chmod 0644 {} + \
# && chmod 0755 /etc/claude-code/skills/docs-audit/scripts/docs_audit.py

# Blend the install-generated ~/.claude.json (machineID, userID, first-start
# time, migration flags, …) UNDER our startup-experience overrides, so that the
# valuable per-install data survives once CLAUDE_CONFIG_DIR redirects claude to
# /agent-data. Our keys win on conflict (jq `*` is a deep merge, right side
# wins). The entrypoint later seeds .init-agent-data/. into /agent-data.
RUN if [ -f /home/agent/.claude.json ]; then \
      jq -s '.[0] * .[1]' /home/agent/.claude.json /home/agent/.init-agent-data/.claude.json \
        > /tmp/claude.json \
      && mv /tmp/claude.json /home/agent/.init-agent-data/.claude.json; \
    fi \
 && chown agent:agent /home/agent/.init-agent-data/.claude.json


# Pre-install the plugin marketplace + the plugins enabled in
# managed-settings.json at BUILD time (open network, before the runtime egress
# lock), then fold them into the first-boot seed. Mirrors the build-time Claude
# Code install above: without it, Claude Code's boot-time auto-installer would
# clone plugins through the locked-down proxy at runtime, where two installer
# invocations racing into one dir leave it half-written and the plugin never
# installs. See docs/OVERVIEW.md ("Workspace and state volumes").
RUN bash /usr/local/lib/cove/seed-plugins.sh

# Run kit-declared setup scripts (config.yml image.setup-script) as root, each
# in place. Replaces the old hardcoded /.install-files/install.sh convention.
RUN /usr/local/lib/cove/run-setup.sh

# SSH sessions read /etc/environment via pam_env. Put ~/.local/bin on PATH,
# point claude at its state on the persistent volume, and force every login
# shell + the SSH session through the local proxy.
RUN printf '%s\n' \
  'PATH=/home/agent/.local/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin' \
  'CLAUDE_CONFIG_DIR=/agent-data' \
  'http_proxy=http://127.0.0.1:3128' \
  'https_proxy=http://127.0.0.1:3128' \
  'HTTP_PROXY=http://127.0.0.1:3128' \
  'HTTPS_PROXY=http://127.0.0.1:3128' \
  'no_proxy=localhost,127.0.0.1,::1,.internal' \
  'NO_PROXY=localhost,127.0.0.1,::1,.internal' \
  >> /etc/environment

# Fold kit-declared PATH/env (config.yml image.paths/image.env) into
# /etc/environment, so pam_env exposes them to every session — the mechanism
# bashrc cannot reach for non-interactive SSH command execution.
RUN /usr/local/lib/cove/apply-image-env.sh

# Drop cove's build-time consumables from the final image.
RUN rm -rf /.cove

COPY /.build-install-files/${TARGETARCH}/at-work /usr/local/bin/.
RUN chmod 0775 /usr/local/bin/at-work

EXPOSE 2222
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
