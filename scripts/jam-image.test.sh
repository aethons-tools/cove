#!/usr/bin/env bash
# jam-image.test.sh — hermetic tests for scripts/jam-image.sh.
# A fake `docker` on PATH records its argv and the build context's file list.
# Run: bash scripts/jam-image.test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
SCRIPT="$HERE/jam-image.sh"
DOCKERFILE="$REPO/images/jam/Dockerfile"

pass=0
fail=0
ok() { pass=$((pass + 1)); printf 'ok   - %s\n' "$1"; }
bad() {
  fail=$((fail + 1))
  printf 'FAIL - %s\n' "$1"
  shift || true
  for line in "$@"; do printf '  %s\n' "$line"; done
}
assert_eq() {
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "expected: $2" "actual:   $3"; fi
}
assert_contains() { # name haystack needle
  case "$2" in *"$3"*) ok "$1" ;; *) bad "$1" "missing: $3" "in: $2" ;; esac
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/stubbin"
mkdir -p "$STUB"
export DOCKER_LOG="$WORK/docker.log"
export CTX_LOG="$WORK/ctx.log"

# Fake docker: log argv on one line, list the context dir (last arg).
cat >"$STUB/docker" <<'EOS'
#!/bin/sh
echo "$*" >>"$DOCKER_LOG"
for a; do ctx="$a"; done
(cd "$ctx" && find . -type f | sed 's|^\./||' | sort) >"$CTX_LOG"
EOS
chmod +x "$STUB/docker"
export PATH="$STUB:$PATH"

mkbin() { mkdir -p "$(dirname "$1")"; printf '#!/bin/sh\n' >"$1"; chmod +x "$1"; }
reset() { rm -rf "$WORK/dist" "$DOCKER_LOG" "$CTX_LOG"; mkdir -p "$WORK/dist"; }

# run <args...>: sets RC, ERR
run() {
  set +e
  ERR="$(cd "$REPO" && bash "$SCRIPT" "$@" 2>&1 >/dev/null)"
  RC=$?
  set -e
}

# 1. build.sh layout, single platform
reset
mkbin "$WORK/dist/linux-amd64/at-jam"
mkbin "$WORK/dist/linux-amd64/at-mint"
run --dist "$WORK/dist" --platforms linux/amd64
assert_eq "build layout: exit 0" 0 "$RC"
case "$(cat "$DOCKER_LOG")" in
  "buildx build --platform linux/amd64 -t cove-jam:dev --load "*) ok "build layout: argv" ;;
  *) bad "build layout: argv" "actual: $(cat "$DOCKER_LOG")" ;;
esac
ctx="$(cat "$CTX_LOG")"
assert_contains "build layout: at-jam" "$ctx" "bin/linux-amd64/at-jam"
assert_contains "build layout: at-mint" "$ctx" "bin/linux-amd64/at-mint"
assert_contains "build layout: Dockerfile" "$ctx" "Dockerfile"

# 2. goreleaser layout, two platforms, push
reset
mkbin "$WORK/dist/at-jam_linux_amd64_v1/at-jam"
mkbin "$WORK/dist/at-mint_linux_amd64_v1/at-mint"
mkbin "$WORK/dist/at-jam_linux_arm64_v8.0/at-jam"
mkbin "$WORK/dist/at-mint_linux_arm64_v8.0/at-mint"
run --dist "$WORK/dist" --platforms linux/amd64,linux/arm64 \
  --tag ghcr.io/aethons-tools/jam:1-0101 --tag ghcr.io/aethons-tools/jam:latest --push
assert_eq "goreleaser: exit 0" 0 "$RC"
argv="$(cat "$DOCKER_LOG")"
assert_contains "goreleaser: platforms" "$argv" "--platform linux/amd64,linux/arm64"
assert_contains "goreleaser: tag 1" "$argv" "-t ghcr.io/aethons-tools/jam:1-0101"
assert_contains "goreleaser: tag 2" "$argv" "-t ghcr.io/aethons-tools/jam:latest"
assert_contains "goreleaser: push" "$argv" "--push"
ctx="$(cat "$CTX_LOG")"
for f in bin/linux-amd64/at-jam bin/linux-amd64/at-mint bin/linux-arm64/at-jam bin/linux-arm64/at-mint; do
  assert_contains "goreleaser: ctx $f" "$ctx" "$f"
done

# 3. missing arm64 binary
reset
mkbin "$WORK/dist/linux-amd64/at-jam"
mkbin "$WORK/dist/linux-amd64/at-mint"
run --dist "$WORK/dist" --platforms linux/amd64,linux/arm64 --push
assert_eq "missing: exit 1" 1 "$RC"
assert_contains "missing: names platform" "$ERR" "no at-jam for linux/arm64 (looked in"
assert_contains "missing: names paths" "$ERR" "$WORK/dist/linux-arm64/at-jam"
[ ! -e "$DOCKER_LOG" ] && ok "missing: docker not called" || bad "missing: docker not called"

# 4. --load with two platforms
reset
run --dist "$WORK/dist" --platforms linux/amd64,linux/arm64
assert_eq "load multi: exit 1" 1 "$RC"
assert_contains "load multi: message" "$ERR" "--load supports one platform; use --push"
[ ! -e "$DOCKER_LOG" ] && ok "load multi: docker not called" || bad "load multi: docker not called"

# 4b. unsupported platform, unknown flag
reset
run --dist "$WORK/dist" --platforms linux/s390x
assert_eq "bad platform: exit 1" 1 "$RC"
assert_contains "bad platform: message" "$ERR" "unsupported platform linux/s390x"
[ ! -e "$DOCKER_LOG" ] && ok "bad platform: docker not called" || bad "bad platform: docker not called"
run --bogus
assert_eq "unknown flag: exit 2" 2 "$RC"
[ ! -e "$DOCKER_LOG" ] && ok "unknown flag: docker not called" || bad "unknown flag: docker not called"

# 5. Dockerfile contract
df="$(cat "$DOCKERFILE")"
for pat in 'FROM alpine:3.22' 'ca-certificates' 'USER jam' 'ENTRYPOINT ["at-jam"]' \
  'CMD ["serve","--config","/etc/jam/jam.yml"]' 'ARG TARGETARCH' \
  'COPY bin/linux-${TARGETARCH}/ /usr/local/bin/' 'ENV HOME=/var/lib/jam' \
  'VOLUME /var/lib/jam' 'EXPOSE 8443 8081' 'addgroup -g 10001' '-u 10001' 'chown 10001:10001 /var/lib/jam' '= 10001:10001'; do
  assert_contains "Dockerfile: $pat" "$df" "$pat"
done
if grep -E '^[[:space:]]*(RUN )?.*apk add' "$DOCKERFILE" | grep -Eiq 'docker|openssh|git'; then
  bad "Dockerfile: no docker/openssh/git in apk add"
else
  ok "Dockerfile: no docker/openssh/git in apk add"
fi

# 6. release.yml wiring: the atcove job builds (PR) / publishes (main) the image.
rel="$REPO/.github/workflows/release.yml"
step_block() { # name-prefix: the step's lines, up to the next step
  awk -v p="$1" 'index($0, "- name: " p) {on=1; print; next} on && /^      - / {exit} on {print}' "$rel"
}
pr_step="$(step_block 'Build Jam image (PR')"
main_step="$(step_block 'Publish Jam image (main')"
smoke_step="$(step_block 'Smoke-test Jam image')"
assert_contains "release.yml: PR step guarded" "$pr_step" "github.event_name == 'pull_request'"
assert_contains "release.yml: PR step builds amd64 only" "$pr_step" "scripts/jam-image.sh --platforms linux/amd64 --tag cove-jam:ci"
case "$pr_step" in *--push*) bad "release.yml: PR step has no --push" ;; *) ok "release.yml: PR step has no --push" ;; esac
assert_contains "release.yml: smoke guarded" "$smoke_step" "github.event_name == 'pull_request'"
assert_contains "release.yml: smoke run" "$smoke_step" "docker run --rm cove-jam:ci version"
assert_contains "release.yml: main step guarded" "$main_step" "github.event_name == 'push'"
assert_contains "release.yml: main step pushes" "$main_step" "scripts/jam-image.sh --platforms linux/amd64,linux/arm64"
assert_contains "release.yml: main step --push" "$main_step" "--push"
assert_contains "release.yml: JAM_IMAGE env" "$(cat "$rel")" "JAM_IMAGE: ghcr.io/aethons-tools/jam"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
