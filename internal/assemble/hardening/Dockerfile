# The sealed hardening layer. It builds FROM the HARNESS LAYER, the stage at-cove
# assemble generates from the model-spec and prepends to this file
# (internal/harnessinstall): `ARG BASE` + `FROM ${BASE} AS harness` installs the
# agent CLI at its exact version + its plugins on the kit-built base. So the
# image is FROM ${BASE} -> harness -> hardening, with hardening always last. A
# bare `docker build` of this file alone fails loud (no `harness` stage).
# `harness` is a stage of the assembled Dockerfile, not a registry image.
# hadolint ignore=DL3006
FROM harness

# sshd: key-only auth, no root login, listen on 2222
# (Fly already uses port 22 on the machine's private interface)
RUN mkdir -p /run/sshd \
 && sed -i 's/^#\?Port .*/Port 2222/'                       /etc/ssh/sshd_config \
 && sed -i 's/^#\?PasswordAuthentication .*/PasswordAuthentication no/' /etc/ssh/sshd_config \
 && sed -i 's/^#\?PermitRootLogin .*/PermitRootLogin no/'    /etc/ssh/sshd_config

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

# Install the embedded, version-locked at-switchboard the orchestrating at-cove
# staged into the build context (mirrors at-task above, COV-135). Install only
# when non-empty: a plain `go build` without scripts/build.sh stages a 0-byte
# placeholder, in which case at-switchboard is simply absent — there is no
# base-image fallback (unlike at-task), so `at-cove teammate` errors clearly at
# launch rather than the build silently installing a broken binary.
COPY switchboard/ /tmp/switchboard/
RUN arch="$(dpkg --print-architecture)" \
 && if [ -s "/tmp/switchboard/at-switchboard-linux-${arch}" ]; then \
      install -m 0755 "/tmp/switchboard/at-switchboard-linux-${arch}" /usr/local/bin/at-switchboard; \
    fi \
 && rm -rf /tmp/switchboard

# Install the embedded, version-locked cove-master the orchestrating at-cove
# staged into the build context (mirrors at-switchboard above, COV-158). Install
# only when the arch binary is non-empty; a plain `go build` stages a 0-byte
# placeholder, in which case cove-master is simply absent and a managed raise
# fails clearly at launch — there is no base-image fallback.
COPY covemaster/ /tmp/covemaster/
RUN arch="$(dpkg --print-architecture)" \
 && if [ -s "/tmp/covemaster/cove-master-linux-${arch}" ]; then \
      install -m 0755 "/tmp/covemaster/cove-master-linux-${arch}" /usr/local/bin/cove-master; \
    fi \
 && rm -rf /tmp/covemaster

COPY image-files/. /.
# `COPY image-files/. /.` carries home/agent/ directory entries (for .ssh and
# .init-agent-data), and Docker stamps the build context's root ownership onto
# the pre-existing /home/agent — leaving the agent unable to write to its own
# $HOME. Restore it (and .ssh, whose contents COPY also re-owned).
# Also create an agent-owned /home/agent/workspace: an isolated workspace is a
# freshly-created named volume mounted there, and Docker initializes an empty
# volume's ownership from this image dir — without it the volume comes up
# root-owned and the first `chat` clone (run as agent) fails Permission denied.
RUN chmod +x /usr/local/bin/entrypoint.sh \
 && chown agent:agent /home/agent \
 && chown -R agent:agent /home/agent/.ssh \
 && chmod 700 /home/agent/.ssh \
 && chmod 600 /home/agent/.ssh/authorized_keys \
 && mkdir -p /home/agent/workspace \
 && chown agent:agent /home/agent/workspace
# && find /etc/claude-code -type d -exec chmod 0755 {} + \
# && find /etc/claude-code -type f -exec chmod 0644 {} + \
# && chmod 0755 /etc/claude-code/skills/docs-audit/scripts/docs_audit.py

# docker:true sandboxes boot systemd as PID 1 (COV-118); enable the sealed nftables
# drop (cove-egress) and the base image's distro squid.service (the egress proxy) at
# boot — before sshd and the inner dockerd — independent of any socket activation. The
# sealed squid.service.d/cove-egress.conf drop-in orders squid After the nft drop.
# `systemctl enable` runs offline during the build (creating the WantedBy symlinks),
# which is why the units are enabled here rather than shipped as symlinks (go:embed
# can't carry symlinks); squid.service is already preset-enabled, enabled again
# idempotently. Inert for non-docker sandboxes: they never boot systemd (the
# entrypoint execs sshd directly), so the enabled units simply never run.
RUN systemctl enable cove-egress.service squid.service

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


# SSH sessions read /etc/environment via pam_env. Bridge the image's Docker ENV
# into it: PATH (intrinsic, ~/.local/bin prepended) + the vars named in
# COVE_SSHENV, then the sealed egress-proxy / CLAUDE_CONFIG_DIR the hardening layer
# owns. One ENV statement feeds both `docker run`/CI and SSH sessions — no
# separate env.d fragment to keep in sync.
RUN /usr/local/lib/cove/apply-sshenv.sh

# Drop cove's build-time consumables from the final image.
RUN rm -rf /.cove

EXPOSE 2222
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
