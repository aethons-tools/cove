# cove-image — FROM cove-base-image + the full build/test/run toolchain.
#
# The base image for BOTH cove's CI job containers AND the agent sandboxes: an
# agent must be able to build/test/run anything CI does. Adds go on top of the lean base.
# See docs/superpowers/specs/2026-07-16-shared-base-image-design.md.
ARG BASE_TAG=latest
FROM cove-base-image:${BASE_TAG}
ARG TARGETARCH
# Toolchain versions — pinned for reproducibility; Renovate keeps them current.
ARG GO_VERSION=1.26.5
ARG HADOLINT_VERSION=v2.14.0

# hadolint (Dockerfile linter) — no apt package; fetch the release binary.
RUN case "${TARGETARCH}" in \
      arm64) HADO_ARCH=arm64 ;; \
      amd64) HADO_ARCH=x86_64 ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL "https://github.com/hadolint/hadolint/releases/download/${HADOLINT_VERSION}/hadolint-Linux-${HADO_ARCH}" \
      -o /usr/local/bin/hadolint \
 && chmod 0755 /usr/local/bin/hadolint

# Language toolchain: golang
COPY image-files/. /.
WORKDIR /install
RUN TARGETARCH="${TARGETARCH}" GO_VERSION="${GO_VERSION}" ./install-go.sh
WORKDIR /
RUN rm -rf /install

# Put the go toolchain on PATH via ENV, so it works for BOTH usages with no
# duplication: a bare `docker run` / CI container reads ENV directly, and at-cove's
# hardening layer copies the live PATH (intrinsic in COVE_SSHENV) into
# /etc/environment for SSH agent sessions. One statement, both contexts.
ENV PATH="/usr/local/go/bin:${PATH}"

# Go dev env. COVE_SSHENV names the vars the hardening layer transfers from this
# image's ENV into the SSH-session environment (PATH is intrinsic, so the go bin
# above already flows through — these are the rest of the Go toolchain env).
ENV GOROOT=/usr/local/go \
    GOPATH=/home/agent/go \
    GOPROXY=direct \
    GOSUMDB=off \
    GOFLAGS=-mod=mod
ENV COVE_SSHENV="GOROOT:GOPATH:GOPROXY:GOSUMDB:GOFLAGS"
