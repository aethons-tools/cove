# TEMPLATE — the base at-cove hardens for this worker, plus any build-time
# customization. Adjust for your target project.
#
# Start from a blessed base: cove-image (the full go/node/java/chrome toolchain,
# for building/testing most projects) or the leaner cove-base-image. Both descend
# from a blessed cove-base-image, so they pass at-cove's provenance gate. Pin to a
# digest for reproducibility; :latest tracks the newest publish.
#
# at-task (the git/PR worker) is INJECTED by at-cove's sealed layer — do NOT
# install it here. The image just needs the project's build/test toolchain and
# `claude` on PATH.
FROM ghcr.io/aethons-tools/cove-image:latest

# Example — install your target project's toolchain (build-time egress is open):
# RUN apt-get update \
#  && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends <pkgs> \
#  && rm -rf /var/lib/apt/lists/*

# Example — session env for every SSH session (folded into /etc/environment).
# A later fragment shadows 00-base.env, so restate PATH's base entries if you
# extend PATH:
# COPY 50-kit.env /etc/cove/env.d/50-kit.env
