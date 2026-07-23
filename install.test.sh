#!/usr/bin/env bash
# install.test.sh — hermetic tests for the root install.sh installer.
#
# No network, no Docker, no live release. We put stub `gh` and `uname`
# executables on PATH that serve a locally-built fixture "release" (fake
# at-cove/at-mint binaries + a real checksums.txt), then drive install.sh
# and assert on what it does. Pure helpers are unit-tested by sourcing
# install.sh in lib mode (COVE_INSTALL_LIB=1), which suppresses main.
#
# Run: bash install.test.sh   (also wired into `just lint`-adjacent CI gate)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SH="$HERE/install.sh"

pass=0
fail=0
note() { printf '  %s\n' "$*"; }
ok() { pass=$((pass + 1)); printf 'ok   - %s\n' "$1"; }
bad() {
  fail=$((fail + 1))
  printf 'FAIL - %s\n' "$1"
  shift || true
  for line in "$@"; do note "$line"; done
}

# assert_eq <name> <expected> <actual>
assert_eq() {
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "expected: $2" "actual:   $3"; fi
}

# ---- sha helper mirroring the script, for building fixtures -----------------
sha_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# ---- build a throwaway sandbox with a fixture release -----------------------
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
# Run inside the sandbox so any accidental relative write from a stub lands here,
# never in the repo. INSTALL_SH is already absolute (resolved above).
cd "$WORK"

FIXTURE_VERSION="123-0722"
RELEASE_DIR="$WORK/release" # what the stub gh "downloads" from
STUB_BIN="$WORK/stubbin"    # stub gh/uname, first on PATH
mkdir -p "$RELEASE_DIR" "$STUB_BIN"

# build a fixture archive carrying fake at-cove + at-mint for a given os/arch
build_archive() {
  local os="$1" arch="$2"
  local stage="$WORK/stage-$os-$arch"
  mkdir -p "$stage"
  cat >"$stage/at-cove" <<EOF
#!/bin/sh
[ "\$1" = version ] && echo "at-cove $FIXTURE_VERSION"
EOF
  cat >"$stage/at-mint" <<EOF
#!/bin/sh
[ "\$1" = version ] && echo "at-mint $FIXTURE_VERSION"
EOF
  chmod +x "$stage/at-cove" "$stage/at-mint"
  tar -C "$stage" -czf "$RELEASE_DIR/cove_${FIXTURE_VERSION}_${os}_${arch}.tar.gz" at-cove at-mint
}

build_archive linux amd64
build_archive darwin arm64

# real checksums over every archive (goreleaser format: "<sha>  <basename>")
(
  cd "$RELEASE_DIR"
  : >checksums.txt
  for f in cove_*.tar.gz; do printf '%s  %s\n' "$(sha_of "$f")" "$f" >>checksums.txt; done
)

# ---- stub gh: serves the fixture release; records requested patterns --------
PATTERN_LOG="$WORK/patterns.log"
: >"$PATTERN_LOG"
cat >"$STUB_BIN/gh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
RELEASE_DIR="$RELEASE_DIR"
FIXTURE_VERSION="$FIXTURE_VERSION"
PATTERN_LOG="$PATTERN_LOG"
sub="\${1:-}"; act="\${2:-}"
if [ "\$sub" = auth ] && [ "\$act" = status ]; then exit 0; fi
if [ "\$sub" = release ] && [ "\$act" = view ]; then echo "\$FIXTURE_VERSION"; exit 0; fi
if [ "\$sub" = release ] && [ "\$act" = download ]; then
  dir=.; patterns=()
  shift 2
  while [ \$# -gt 0 ]; do
    case "\$1" in
      --dir|-D) dir="\$2"; shift 2;;
      --pattern) patterns+=("\$2"); echo "\$2" >>"\$PATTERN_LOG"; shift 2;;
      --repo|-R) shift 2;;
      --clobber) shift;;
      *) shift;;
    esac
  done
  mkdir -p "\$dir"
  for p in "\${patterns[@]}"; do
    match="\$RELEASE_DIR/\$p"
    [ -e "\$match" ] || { echo "gh: no asset matching \$p" >&2; exit 1; }
    cp "\$match" "\$dir/"
  done
  exit 0
fi
echo "stub gh: unhandled: \$*" >&2; exit 2
EOF
chmod +x "$STUB_BIN/gh"

# stub uname: OS from UNAME_S, machine from UNAME_M
cat >"$STUB_BIN/uname" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  -s) echo "${UNAME_S:-Linux}";;
  -m) echo "${UNAME_M:-x86_64}";;
  *)  echo "${UNAME_S:-Linux}";;
esac
EOF
chmod +x "$STUB_BIN/uname"

# run install.sh with stubs first on PATH and a clean-ish env
run_install() {
  PATH="$STUB_BIN:$PATH" bash "$INSTALL_SH"
}

echo "== unit: pure helpers (sourced in lib mode) =="
# shellcheck disable=SC1090
COVE_INSTALL_LIB=1 source "$INSTALL_SH"

assert_eq "normalize_arch x86_64 -> amd64" "amd64" "$(normalize_arch x86_64)"
assert_eq "normalize_arch aarch64 -> arm64" "arm64" "$(normalize_arch aarch64)"
assert_eq "normalize_arch arm64 -> arm64" "arm64" "$(normalize_arch arm64)"
if normalize_arch i386 >/dev/null 2>&1; then bad "normalize_arch i386 rejected"; else ok "normalize_arch i386 rejected"; fi

assert_eq "normalize_os Linux -> linux" "linux" "$(normalize_os Linux)"
assert_eq "normalize_os Darwin -> darwin" "darwin" "$(normalize_os Darwin)"
if normalize_os MINGW64_NT >/dev/null 2>&1; then bad "normalize_os windows rejected"; else ok "normalize_os windows rejected"; fi

# resolve_bindir precedence: BINDIR > COVE_SYSTEM > ~/.local/bin default
assert_eq "resolve_bindir default" "$HOME/.local/bin" "$(BINDIR='' COVE_SYSTEM='' resolve_bindir)"
assert_eq "resolve_bindir COVE_SYSTEM=1" "/usr/local/bin" "$(BINDIR='' COVE_SYSTEM=1 resolve_bindir)"
assert_eq "resolve_bindir BINDIR wins" "/opt/bin" "$(BINDIR=/opt/bin COVE_SYSTEM=1 resolve_bindir)"

# verify_checksum: good passes, tampered fails
vc="$WORK/vc"
mkdir -p "$vc"
echo hello >"$vc/cove_x.tar.gz"
printf '%s  %s\n' "$(sha_of "$vc/cove_x.tar.gz")" "cove_x.tar.gz" >"$vc/checksums.txt"
if (cd "$vc" && verify_checksum cove_x.tar.gz checksums.txt); then ok "verify_checksum good"; else bad "verify_checksum good"; fi
echo tampered >>"$vc/cove_x.tar.gz"
if (cd "$vc" && verify_checksum cove_x.tar.gz checksums.txt 2>/dev/null); then bad "verify_checksum tamper -> abort"; else ok "verify_checksum tamper -> abort"; fi

echo "== e2e: gh path installs both binaries =="
BIN1="$WORK/bin1"
out="$(UNAME_S=Linux UNAME_M=x86_64 COVE_VERSION="$FIXTURE_VERSION" BINDIR="$BIN1" run_install 2>&1)" && rc=0 || rc=$?
if [ "$rc" = 0 ]; then ok "install exit 0"; else bad "install exit 0" "rc=$rc" "$out"; fi
if [ -x "$BIN1/at-cove" ]; then ok "at-cove installed + executable"; else bad "at-cove installed + executable" "$out"; fi
if [ -x "$BIN1/at-mint" ]; then ok "at-mint installed + executable"; else bad "at-mint installed + executable" "$out"; fi
if [ -e "$BIN1/at-task" ]; then bad "at-task NOT installed (embedded)"; else ok "at-task NOT installed (embedded)"; fi

echo "== e2e: arch mapping picks darwin_arm64 asset =="
: >"$PATTERN_LOG"
BIN2="$WORK/bin2"
UNAME_S=Darwin UNAME_M=arm64 COVE_VERSION="$FIXTURE_VERSION" BINDIR="$BIN2" run_install >/dev/null 2>&1 || true
if grep -q "cove_${FIXTURE_VERSION}_darwin_arm64.tar.gz" "$PATTERN_LOG"; then ok "requested darwin_arm64 archive"; else bad "requested darwin_arm64 archive" "patterns: $(cat "$PATTERN_LOG")"; fi

echo "== e2e: latest-version resolution via gh =="
BIN3="$WORK/bin3"
UNAME_S=Linux UNAME_M=x86_64 BINDIR="$BIN3" run_install >/dev/null 2>&1 || true
if [ -x "$BIN3/at-cove" ]; then ok "latest resolves + installs"; else bad "latest resolves + installs"; fi

echo "== e2e: tampered checksum aborts, nothing installed =="
BADREL="$WORK/badrelease"
mkdir -p "$BADREL"
cp "$RELEASE_DIR"/cove_"${FIXTURE_VERSION}"_linux_amd64.tar.gz "$BADREL/"
# checksums that will NOT match the archive
printf '%s  %s\n' "$(sha_of /dev/null)" "cove_${FIXTURE_VERSION}_linux_amd64.tar.gz" >"$BADREL/checksums.txt"
# point the stub gh at the tampered release for this run
cat >"$STUB_BIN/gh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
sub="\${1:-}"; act="\${2:-}"
if [ "\$sub" = auth ] && [ "\$act" = status ]; then exit 0; fi
if [ "\$sub" = release ] && [ "\$act" = download ]; then
  # Collect patterns, resolve --dir fully, THEN copy — --dir may follow --pattern.
  dir=.; patterns=(); shift 2
  while [ \$# -gt 0 ]; do
    case "\$1" in
      --dir|-D) dir="\$2"; shift 2;;
      --pattern) patterns+=("\$2"); shift 2;;
      --repo|-R) shift 2;;
      *) shift;;
    esac
  done
  mkdir -p "\$dir"
  for p in "\${patterns[@]}"; do cp "$BADREL/\$p" "\$dir/" 2>/dev/null || true; done
  exit 0
fi
exit 0
EOF
chmod +x "$STUB_BIN/gh"
BIN4="$WORK/bin4"
UNAME_S=Linux UNAME_M=x86_64 COVE_VERSION="$FIXTURE_VERSION" BINDIR="$BIN4" run_install >/dev/null 2>&1 && rc=0 || rc=$?
if [ "$rc" != 0 ]; then ok "tamper aborts (nonzero exit)"; else bad "tamper aborts (nonzero exit)"; fi
if [ -e "$BIN4/at-cove" ]; then bad "no binary installed on tamper"; else ok "no binary installed on tamper"; fi

echo
echo "-------- $pass passed, $fail failed --------"
[ "$fail" -eq 0 ]
