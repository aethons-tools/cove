#!/usr/bin/env bash
# local.test.sh — hermetic test for scripts/local.sh (`just local`): a fake
# docker and go, and stub build scripts, in a scratch copy of the repo layout.
# No daemon, no network.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
pass=0
fail=0
check() { # check <name> <condition...>
  local name="$1"
  shift
  if "$@"; then pass=$((pass + 1)); echo "ok   - $name"; else fail=$((fail + 1)); echo "FAIL - $name"; fi
}

# setup: a scratch repo with local.sh and stubs; prints its path.
setup() {
  local d
  d="$(mktemp -d)"
  mkdir -p "$d/scripts" "$d/bin" "$d/internal/basedigest/blessed" "$d/images/jam-studio-base" "$d/images/jam-studio"
  cp "$HERE/local.sh" "$d/scripts/local.sh"
  for s in stage-vscode.sh build.sh jam-image.sh; do
    # shellcheck disable=SC2016 # the stub expands these when it runs
    printf '#!/usr/bin/env bash\necho "%s $* VSCODE_SKIP_CHECK=${VSCODE_SKIP_CHECK:-} DOCKER_CONTEXT=${DOCKER_CONTEXT:-}" >> "%s/calls"\n' "$s" "$d" > "$d/scripts/$s"
    chmod +x "$d/scripts/$s"
  done
  # docker: contexts colima+default, server arch $FAKE_ARCH, deterministic IDs.
  cat > "$d/bin/docker" <<EOF
#!/usr/bin/env bash
echo "docker \$* CTX=\${DOCKER_CONTEXT:-}" >> "$d/calls"
case "\$1 \$2" in
  "context ls") printf '%s\n' \${FAKE_CONTEXTS:-default colima} ;;
  "context show") echo default ;;
  "version -f") echo "\${FAKE_ARCH:-arm64}" ;;
  "image inspect")
    case "\$*" in
      *jam-studio-base*) echo sha256:aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111 ;;
      *) echo sha256:bbbbbbbbbbbb2222222222222222222222222222222222222222222222222222 ;;
    esac ;;
  "image ls") echo jam-studio:dev-bbbbbbbbbbbb ;;
esac
exit 0
EOF
  # shellcheck disable=SC2016 # the stub expands these when it runs
  printf '#!/usr/bin/env bash\ncase "$2" in GOOS) echo darwin ;; GOARCH) echo arm64 ;; esac\n' > "$d/bin/go"
  chmod +x "$d/bin/docker" "$d/bin/go"
  printf '%s\n' "$d"
}

# A full run on a mac with colima: images in colima, local.txt, both builds, jam:dev.
d="$(setup)"
out="$(cd "$d" && PATH="$d/bin:$PATH" DOCKER_CONTEXT='' bash scripts/local.sh 2>&1)"
local_txt="$(grep -v '^#' "$d/internal/basedigest/blessed/local.txt")"
check "local.txt names the base by its image ID" test "$local_txt" = "jam-studio-base:dev-aaaaaaaaaaaa"
check "picks the colima context" grep -q "CTX=colima" "$d/calls"
check "jam-studio is built FROM the local base" grep -q "build --progress plain --build-arg BASE_TAG=dev-aaaaaaaaaaaa --tag jam-studio:dev-build" "$d/calls"
check "jam-studio is tagged by its ID" grep -q "tag jam-studio:dev-build jam-studio:dev-bbbbbbbbbbbb" "$d/calls"
check "the extension is packaged without re-running its checks" grep -q "stage-vscode.sh  VSCODE_SKIP_CHECK=1" "$d/calls"
check "builds the host and the Docker's linux arch" grep -q "build.sh darwin/arm64 linux/arm64" "$d/calls"
check "jam:dev is built in the same Docker" grep -q "jam-image.sh --platforms linux/arm64 --tag jam:dev VSCODE_SKIP_CHECK= DOCKER_CONTEXT=colima" "$d/calls"
check "the summary names the kit base" grep -q "base: {image: jam-studio:dev-bbbbbbbbbbbb}" <<<"$out"
rm -rf "$d"

# DOCKER_CONTEXT is honoured, and an amd64 Docker gets linux/amd64.
d="$(setup)"
(cd "$d" && PATH="$d/bin:$PATH" DOCKER_CONTEXT=other FAKE_ARCH=amd64 bash scripts/local.sh >/dev/null 2>&1)
check "DOCKER_CONTEXT wins" grep -q "CTX=other" "$d/calls"
check "amd64 Docker: linux/amd64 binaries and image" grep -q "build.sh darwin/arm64 linux/amd64" "$d/calls"
rm -rf "$d"

# No colima context: the current one.
d="$(setup)"
(cd "$d" && PATH="$d/bin:$PATH" DOCKER_CONTEXT='' FAKE_CONTEXTS=default bash scripts/local.sh >/dev/null 2>&1)
check "no colima: the current context" grep -q "jam-image.sh .* DOCKER_CONTEXT=default" "$d/calls"
rm -rf "$d"

# LOCAL_SKIP_IMAGES=1 without a local.txt refuses, and builds nothing.
d="$(setup)"
if (cd "$d" && PATH="$d/bin:$PATH" LOCAL_SKIP_IMAGES=1 bash scripts/local.sh >/dev/null 2>&1); then rc=0; else rc=$?; fi
check "LOCAL_SKIP_IMAGES=1 needs a local.txt" test "$rc" -ne 0
check "... and builds nothing" bash -c "! grep -q 'build.sh' '$d/calls'"
rm -rf "$d"

# LOCAL_SKIP_IMAGES=1 with one reuses it: no image builds.
d="$(setup)"
printf '# x\njam-studio-base:dev-cccccccccccc\n' > "$d/internal/basedigest/blessed/local.txt"
(cd "$d" && PATH="$d/bin:$PATH" DOCKER_CONTEXT=colima LOCAL_SKIP_IMAGES=1 bash scripts/local.sh >/dev/null 2>&1)
check "LOCAL_SKIP_IMAGES=1 builds no image" bash -c "! grep -q 'docker build' '$d/calls'"
check "... keeps local.txt" grep -q "jam-studio-base:dev-cccccccccccc" "$d/internal/basedigest/blessed/local.txt"
check "... and still builds the binaries" grep -q "build.sh" "$d/calls"
rm -rf "$d"

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
