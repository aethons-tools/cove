# cove-image — FROM cove-base-image + the full build/test/run toolchain.
#
# The base image for BOTH cove's CI job containers AND the agent sandboxes: an
# agent must be able to build/test/run anything CI does. Adds go, just,
# shellcheck, hadolint, node, chrome, and java on top of the lean base.
# See docs/superpowers/specs/2026-07-16-shared-base-image-design.md.
ARG BASE_TAG=latest
FROM cove-base-image:${BASE_TAG}
ARG TARGETARCH
# Toolchain versions — pinned for reproducibility; Renovate keeps them current.
ARG GO_VERSION=1.26.4
ARG JDK_RELEASE=jdk-25.0.3+9
ARG NODE_MAJOR_VERSION=22
ARG NODE_VERSION=22.23.1-1nodesource1
ARG HADOLINT_VERSION=v2.14.0

# CI/lint tooling from apt (pinned; see the note in cove-base-image's Dockerfile).
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      shellcheck=0.9.0-1 \
      just=1.21.0-1 \
 && rm -rf /var/lib/apt/lists/*

# hadolint (Dockerfile linter) — no apt package; fetch the release binary.
RUN case "${TARGETARCH}" in \
      arm64) HADO_ARCH=arm64 ;; \
      amd64) HADO_ARCH=x86_64 ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL "https://github.com/hadolint/hadolint/releases/download/${HADOLINT_VERSION}/hadolint-Linux-${HADO_ARCH}" \
      -o /usr/local/bin/hadolint \
 && chmod 0755 /usr/local/bin/hadolint

# Language toolchains + headless chromium, via the install scripts (run in place,
# then removed). Order matters: chromium is installed through npx, so node first.
# chromium is the one input not version-pinned here — it rides whatever
# `playwright` npx resolves; pinning it needs the playwright npm version (a
# follow-up).
COPY install-files/ /base-install-files/
WORKDIR /base-install-files
RUN TARGETARCH="${TARGETARCH}" GO_VERSION="${GO_VERSION}" ./install-go.sh
RUN TARGETARCH="${TARGETARCH}" JDK_RELEASE="${JDK_RELEASE}" ./install-java.sh
RUN NODE_MAJOR_VERSION="${NODE_MAJOR_VERSION}" NODE_VERSION="${NODE_VERSION}" ./install-node.sh
RUN ./install-chrome.sh
WORKDIR /
RUN rm -rf /base-install-files

# Put the go toolchain on PATH and pin JAVA_HOME for non-login shells — a
# `docker run` / CI container does not read /etc/environment (that path, used by
# SSH agent sessions, is set later by at-cove's hardening layer).
ENV PATH="/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
ENV JAVA_HOME="/opt/jdk"
