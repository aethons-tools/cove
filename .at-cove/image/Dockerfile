# The base at-cove hardens for this repo's dev sandbox. cove-image carries the
# full toolchain (go, just, shellcheck, hadolint, node, chrome, java) so the cove
# dev/test loop runs inside the sandbox; the lean base under it provides podman +
# rootless plumbing, git, gh, jq. Pin to a digest for reproducibility if you
# prefer; :latest tracks the newest publish (which descends from the blessed
# cove-base-image, so it passes the provenance gate).
FROM ghcr.io/aethons-tools/cove-image:latest

# Rootless-podman ergonomics for the Colima-backed e2e tests (podman + plumbing
# already ship in the base): silence the podman-docker banner and give the agent
# subuid/subgid ranges if the base image did not (useradd may not add them).
RUN touch /etc/containers/nodocker \
 && if ! grep -q '^agent:' /etc/subuid; then \
      usermod --add-subuids 100000-165535 --add-subgids 100000-165535 agent; \
    fi

# Go env for every SSH session, folded into /etc/environment by the sealed layer.
COPY 50-go.env /etc/cove/env.d/50-go.env
