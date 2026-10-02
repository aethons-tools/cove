# jam-studio-image — FROM jam-studio-base-image + the full build/test/run toolchain.
#
# The base image for BOTH cove's CI job containers AND the agent sandboxes: an
# agent must be able to build/test/run anything CI does. Adds go on top of the lean base.
# See docs/superpowers/specs/2026-07-16-shared-base-image-design.md.
ARG AT_JAM_STUDIO_BASE_IMAGE=scratch
FROM ${AT_JAM_STUDIO_BASE_IMAGE}
ARG AT_JAM_STUDIO_TARGET_ARCH
# Toolchain versions — pinned for reproducibility; Renovate keeps them current.
ARG GO_VERSION=1.27.1
ARG HADOLINT_VERSION=v2.15.1

# Fonts for headless-chrome UI testing. The base ships font files but no
# fontconfig, so /etc/fonts/fonts.conf is missing (chrome logs "Fontconfig
# error: Cannot load default config file"); fonts-noto-cjk covers CJK glyphs,
# which otherwise render as tofu.
# hadolint ignore=DL3008
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      fontconfig \
      fonts-noto-cjk \
 && rm -rf /var/lib/apt/lists/*

# hadolint (Dockerfile linter) — no apt package; fetch the release binary.
RUN case "${AT_JAM_STUDIO_TARGET_ARCH}" in \
      arm64) HADO_ARCH=arm64 ;; \
      amd64) HADO_ARCH=x86_64 ;; \
      *) echo "unsupported AT_JAM_STUDIO_TARGET_ARCH: ${AT_JAM_STUDIO_TARGET_ARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL "https://github.com/hadolint/hadolint/releases/download/${HADOLINT_VERSION}/hadolint-Linux-${HADO_ARCH}" \
      -o /usr/local/bin/hadolint \
 && chmod 0755 /usr/local/bin/hadolint

# Language toolchain: golang
COPY image-files/. /.
WORKDIR /install
RUN TARGETARCH="${AT_JAM_STUDIO_TARGET_ARCH}" GO_VERSION="${GO_VERSION}" ./install-go.sh
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
# GOPROXY goes through the module proxy first: a studio's github.com git traffic
# is routed through the at-jam git proxy, which serves only the studio's own
# repo, so `direct` alone 403s on every dependency.
ENV GOROOT=/usr/local/go \
    GOPATH=/home/agent/go \
    GOPROXY=https://proxy.golang.org,direct \
    GOSUMDB=off \
    GOFLAGS=-mod=mod
# Git commit identity for the agent working in this studio. Set via git's env
# overrides rather than /etc/gitconfig, which the hardening layer owns.
ENV GIT_AUTHOR_NAME=claude-aethon \
    GIT_AUTHOR_EMAIL=claude+brent@lcbm.life \
    GIT_COMMITTER_NAME=claude-aethon \
    GIT_COMMITTER_EMAIL=claude+brent@lcbm.life
ENV COVE_SSHENV="GOROOT:GOPATH:GOPROXY:GOSUMDB:GOFLAGS:GIT_AUTHOR_NAME:GIT_AUTHOR_EMAIL:GIT_COMMITTER_NAME:GIT_COMMITTER_EMAIL"

# Headless Chrome for the browser tests of the /me UI (`just test-browser`) and
# for agents to screenshot UI changes. The official Chrome for Testing
# headless-shell build (amd64 and arm64), pinned by version; Ubuntu's own
# `chromium` is a snap stub that cannot run in a container. The apt list is the
# shared libraries the binary links against (from the zip's deb.deps, as
# Ubuntu 24.04 t64 names) plus fonts to render text (DejaVu covers symbols such
# as the ⇧⌘ in the /me composer placeholder); the ldd check fails the build if
# the list falls behind a new Chrome.
ARG CHROME_VERSION=153.0.8010.12
# hadolint ignore=DL3008
RUN case "${AT_JAM_STUDIO_TARGET_ARCH}" in \
      arm64) CFT_PLATFORM=linux-arm64 ;; \
      amd64) CFT_PLATFORM=linux64 ;; \
      *) echo "unsupported AT_JAM_STUDIO_TARGET_ARCH: ${AT_JAM_STUDIO_TARGET_ARCH}" >&2; exit 1 ;; \
    esac \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      unzip libasound2t64 libatk1.0-0t64 libatspi2.0-0t64 libdbus-1-3 libgbm1 \
      libglib2.0-0t64 libnspr4 libnss3 libx11-6 libxcb1 libxcomposite1 \
      libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2 \
      fonts-dejavu-core fonts-liberation fonts-noto-color-emoji \
 && rm -rf /var/lib/apt/lists/* \
 && tmp="$(mktemp -d)" \
 && curl -fsSL "https://storage.googleapis.com/chrome-for-testing-public/${CHROME_VERSION}/${CFT_PLATFORM}/chrome-headless-shell-${CFT_PLATFORM}.zip" \
      -o "$tmp/chrome.zip" \
 && unzip -q "$tmp/chrome.zip" -d "$tmp" \
 && mv "$tmp/chrome-headless-shell-${CFT_PLATFORM}" /opt/chrome-headless-shell \
 && rm -rf "$tmp" \
 && ln -s /opt/chrome-headless-shell/chrome-headless-shell /usr/local/bin/chrome-headless-shell \
 && ldd /opt/chrome-headless-shell/chrome-headless-shell > /tmp/chrome.ldd \
 && if grep 'not found' /tmp/chrome.ldd; then exit 1; fi \
 && rm /tmp/chrome.ldd

# Go agent skills (samber/cc-skills-golang) — clone the pinned release and seed
# its skill files into the first-boot skills set. Scoped to cove-image (this
# repo's toolchain image + agent sandboxes), NOT the shared hardening layer, so
# other kits are unaffected. entrypoint.sh re-mirrors $SEED/skills ->
# /agent-data/skills on every boot, so a rebuilt image reaches existing sandboxes
# on restart. MIT-licensed; the notice is retained under /usr/share/doc.
# Version pinned for reproducibility; Renovate (github-releases) keeps it current.
ARG CC_SKILLS_GOLANG_VERSION=v2.0.0
RUN skills_seed=/home/agent/.init-agent-data/skills \
 && tmp="$(mktemp -d)" \
 && git clone --depth 1 --branch "${CC_SKILLS_GOLANG_VERSION}" \
      https://github.com/samber/cc-skills-golang.git "$tmp" \
 && mkdir -p "$skills_seed" /usr/share/doc/cc-skills-golang \
 && cp -a "$tmp/skills/." "$skills_seed/" \
 && cp -a "$tmp/LICENSE" /usr/share/doc/cc-skills-golang/LICENSE \
 && chown -R agent:agent "$skills_seed" \
 && rm -rf "$tmp"
