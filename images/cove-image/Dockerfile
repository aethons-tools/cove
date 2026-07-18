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

# Put the go toolchain on PATH for non-login shells. A `docker run` / CI container
# does NOT read /etc/environment (that path — used by SSH agent sessions — is set
# later by at-cove's hardening layer, which folds /etc/cove/env.d/*.env into it).
# So the bare image (CI smoke tests, job containers) needs go on PATH here.
ENV PATH="/usr/local/go/bin:${PATH}"
