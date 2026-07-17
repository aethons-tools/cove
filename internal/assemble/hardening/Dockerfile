# The base at-cove hardens FROM. at-cove always injects --build-arg BASE (the
# resolved, provenance-gated base: a blessed cove-base-image by default, or the
# kit's image.base / built image/Dockerfile). Declared before FROM so it applies
# to the base line; no default, so a bare `docker build` without BASE fails loud
# rather than silently hardening the wrong floor.
ARG BASE
# BASE is an at-cove-injected, digest-pinned ref, so "always tag" does not apply.
# hadolint ignore=DL3006
FROM ${BASE}

# Pre-install Claude Code (native) at BUILD time, as the agent user.
# Fly's builders have open network, so this fetch happens BEFORE the runtime
# egress lock. The desktop app then finds 'claude' already present instead of
# bootstrapping through the locked-down proxy.
RUN su - agent -c 'curl -fsSL https://claude.ai/install.sh | bash'

# Make the binary findable regardless of how sshd sets PATH for the session.
RUN ln -sf /home/agent/.local/bin/claude /usr/local/bin/claude

# sshd: key-only auth, no root login, listen on 2222
# (Fly already uses port 22 on the machine's private interface)
RUN mkdir -p /run/sshd \
 && sed -i 's/^#\?Port .*/Port 2222/'                       /etc/ssh/sshd_config \
 && sed -i 's/^#\?PasswordAuthentication .*/PasswordAuthentication no/' /etc/ssh/sshd_config \
 && sed -i 's/^#\?PermitRootLogin .*/PermitRootLogin no/'    /etc/ssh/sshd_config

# Don't phone home for background updates from inside the sandbox; rebuild the
# image to update Claude Code instead. (claude.ai is still reached at runtime for
# the subscription OAuth login flow — see the squid allow-list.) Use
# DISABLE_UPDATES=1 to also block manual updates.
ENV DISABLE_AUTOUPDATER=1

# Install the embedded, version-locked at-task the orchestrating at-cove staged
# into the build context (COV-34). Install only when non-empty: a plain
# `go build` without scripts/build.sh stages a 0-byte placeholder, in which case
# we keep the base image's at-task rather than clobbering it with a broken one.
COPY attask/ /tmp/attask/
RUN arch="$(dpkg --print-architecture)" \
 && if [ -s "/tmp/attask/at-task-linux-${arch}" ]; then \
      install -m 0755 "/tmp/attask/at-task-linux-${arch}" /usr/local/bin/at-task; \
    fi \
 && rm -rf /tmp/attask


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

# Run kit-declared setup scripts (config.yml image.setup-scripts) as root, each
# in place. Replaces the old hardcoded /.install-files/install.sh convention.
RUN /usr/local/lib/cove/run-setup.sh

# SSH sessions read /etc/environment via pam_env. The base image ships the
# session env in /etc/cove/env.d/00-base.env (PATH, CLAUDE_CONFIG_DIR, the egress
# proxy vars); fold every /etc/cove/env.d/*.env fragment — the base's plus any a
# kit Dockerfile dropped — into /etc/environment (COV-34).
RUN /usr/local/lib/cove/apply-env-d.sh

# Also fold kit-declared PATH/env (config.yml image.paths/image.env) into
# /etc/environment — the legacy mechanism, retired once kits migrate to env.d
# fragments (COV-34 sub-issue 4).
RUN /usr/local/lib/cove/apply-image-env.sh

# Drop cove's build-time consumables from the final image.
RUN rm -rf /.cove

EXPOSE 2222
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
