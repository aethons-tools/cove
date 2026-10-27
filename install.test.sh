#!/usr/bin/env bash
# install.test.sh — hermetic tests for the root install.sh installer.
#
# No network, no Docker, no live release. Stub `gh` and `uname` executables on
# PATH serve a locally-built fixture "release": a jam_<ver>_<os>_<arch>.tar.gz
# holding a fake `jam`, plus, as a control, an old-format cove_<ver>_* archive
# like the releases before the jam rename. Pure helpers are unit-tested by
# sourcing install.sh in lib mode (JAM_INSTALL_LIB=1), which suppresses main.
#
# Run: bash install.test.sh   (`just test-install`; a CI gate)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SH="$HERE/install.sh"
# install.sh as embedded in the pre-rename at-jam builds in the field: after
# jam-only slice 2 (main before the rename), and the older pre-slice-2 one.
LEGACY_SHS=("$HERE/testdata/install-legacy.sh" "$HERE/testdata/install-legacy-preslice2.sh")

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
assert_eq() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "expected: $2" "actual:   $3"; fi; }

sha_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

FIXTURE_VERSION="200-1026"   # a jam release
OLD_VERSION="150-1001"       # a pre-rename release (old archive name and contents)
RELEASE_DIR="$WORK/release"
STUB_BIN="$WORK/stubbin"
mkdir -p "$RELEASE_DIR" "$STUB_BIN"

# fake_bin <dir> <name> <version>
fake_bin() {
  cat >"$1/$2" <<EOF
#!/bin/sh
[ "\$1" = version ] && echo "$2 $3"
EOF
  chmod +x "$1/$2"
}

# build_jam_archive <os> <arch> — the new shape: exactly `jam`.
build_jam_archive() {
  local stage="$WORK/stage-jam-$1-$2"
  mkdir -p "$stage"
  fake_bin "$stage" jam "$FIXTURE_VERSION"
  tar -C "$stage" -czf "$RELEASE_DIR/jam_${FIXTURE_VERSION}_$1_$2.tar.gz" jam
}
# build_old_archive <os> <arch> — the pre-rename shape the legacy installer expects.
build_old_archive() {
  local stage="$WORK/stage-old-$1-$2" b
  mkdir -p "$stage"
  for b in at-cove at-mint at-jam; do fake_bin "$stage" "$b" "$OLD_VERSION"; done
  tar -C "$stage" -czf "$RELEASE_DIR/cove_${OLD_VERSION}_$1_$2.tar.gz" at-cove at-mint at-jam
}
build_jam_archive linux amd64
build_jam_archive darwin arm64
build_old_archive linux amd64
(
  cd "$RELEASE_DIR"
  : >checksums.txt
  for f in *.tar.gz; do printf '%s  %s\n' "$(sha_of "$f")" "$f" >>checksums.txt; done
)

PATTERN_LOG="$WORK/patterns.log"
: >"$PATTERN_LOG"
cat >"$STUB_BIN/gh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
sub="\${1:-}"; act="\${2:-}"
if [ "\$sub" = auth ] && [ "\$act" = status ]; then exit 0; fi
if [ "\$sub" = release ] && [ "\$act" = view ]; then echo "$FIXTURE_VERSION"; exit 0; fi
if [ "\$sub" = release ] && [ "\$act" = download ]; then
  dir=.; patterns=(); shift 2
  while [ \$# -gt 0 ]; do
    case "\$1" in
      --dir|-D) dir="\$2"; shift 2;;
      --pattern) patterns+=("\$2"); echo "\$2" >>"$PATTERN_LOG"; shift 2;;
      --repo|-R) shift 2;;
      *) shift;;
    esac
  done
  mkdir -p "\$dir"
  for p in "\${patterns[@]}"; do
    [ -e "$RELEASE_DIR/\$p" ] || { echo "gh: no assets match the file pattern \$p" >&2; exit 1; }
    cp "$RELEASE_DIR/\$p" "\$dir/"
  done
  exit 0
fi
echo "stub gh: unhandled: \$*" >&2; exit 2
EOF
chmod +x "$STUB_BIN/gh"
cat >"$STUB_BIN/uname" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in -s) echo "${UNAME_S:-Linux}";; -m) echo "${UNAME_M:-x86_64}";; *) echo "${UNAME_S:-Linux}";; esac
EOF
chmod +x "$STUB_BIN/uname"
run_install() { PATH="$STUB_BIN:$PATH" bash "$INSTALL_SH"; }

echo "== unit: pure helpers (sourced in lib mode) =="
# shellcheck disable=SC1090
JAM_INSTALL_LIB=1 source "$INSTALL_SH"
assert_eq "normalize_arch x86_64 -> amd64" amd64 "$(normalize_arch x86_64)"
assert_eq "normalize_arch aarch64 -> arm64" arm64 "$(normalize_arch aarch64)"
if normalize_arch i386 >/dev/null 2>&1; then bad "normalize_arch i386 rejected"; else ok "normalize_arch i386 rejected"; fi
assert_eq "normalize_os Darwin -> darwin" darwin "$(normalize_os Darwin)"
if normalize_os MINGW64_NT >/dev/null 2>&1; then bad "normalize_os windows rejected"; else ok "normalize_os windows rejected"; fi
assert_eq "resolve_bindir default" "$HOME/.local/bin" "$(BINDIR='' JAM_SYSTEM='' resolve_bindir)"
assert_eq "resolve_bindir JAM_SYSTEM=1" /usr/local/bin "$(BINDIR='' JAM_SYSTEM=1 resolve_bindir)"
assert_eq "resolve_bindir BINDIR wins" /opt/bin "$(BINDIR=/opt/bin JAM_SYSTEM=1 resolve_bindir)"
assert_eq "resolve_bindir ignores COVE_SYSTEM" "$HOME/.local/bin" "$(BINDIR='' JAM_SYSTEM='' COVE_SYSTEM=1 resolve_bindir)"
vc="$WORK/vc"; mkdir -p "$vc"; echo hello >"$vc/jam_x.tar.gz"
printf '%s  %s\n' "$(sha_of "$vc/jam_x.tar.gz")" jam_x.tar.gz >"$vc/checksums.txt"
if (cd "$vc" && verify_checksum jam_x.tar.gz checksums.txt); then ok "verify_checksum good"; else bad "verify_checksum good"; fi
echo tampered >>"$vc/jam_x.tar.gz"
if (cd "$vc" && verify_checksum jam_x.tar.gz checksums.txt 2>/dev/null); then bad "verify_checksum tamper"; else ok "verify_checksum tamper -> abort"; fi

echo "== e2e: installs exactly jam =="
BIN1="$WORK/bin1"
out="$(JAM_VERSION="$FIXTURE_VERSION" BINDIR="$BIN1" run_install 2>&1)" && rc=0 || rc=$?
assert_eq "install exit 0" 0 "$rc"
if [ -x "$BIN1/jam" ]; then ok "jam installed + executable"; else bad "jam installed + executable" "$out"; fi
for b in at-jam at-mint at-cove; do
  if [ -e "$BIN1/$b" ]; then bad "$b NOT installed"; else ok "$b NOT installed"; fi
done
if grep -q "jam_${FIXTURE_VERSION}_linux_amd64.tar.gz" "$PATTERN_LOG"; then ok "requested the jam_ archive"; else bad "requested the jam_ archive" "$(cat "$PATTERN_LOG")"; fi
case "$out" in *"pre-rename binaries"*) bad "no stale notice on a clean dir" "$out" ;; *) ok "no stale notice on a clean dir" ;; esac

echo "== e2e: stale pre-rename binaries are noticed, never touched =="
BIN2="$WORK/bin2"; mkdir -p "$BIN2"
printf 'old-at-jam\n' >"$BIN2/at-jam"; printf 'old-at-mint\n' >"$BIN2/at-mint"
out="$(JAM_VERSION="$FIXTURE_VERSION" BINDIR="$BIN2" run_install 2>&1)" && rc=0 || rc=$?
assert_eq "install with stale binaries exit 0" 0 "$rc"
case "$out" in *"pre-rename binaries still in $BIN2: at-jam at-mint"*"renamed-from-at.md"*) ok "stale notice names them and the runbook" ;; *) bad "stale notice" "$out" ;; esac
assert_eq "stale at-jam untouched" old-at-jam "$(cat "$BIN2/at-jam")"
assert_eq "stale at-mint untouched" old-at-mint "$(cat "$BIN2/at-mint")"

echo "== e2e: arch mapping and latest resolution =="
: >"$PATTERN_LOG"
UNAME_S=Darwin UNAME_M=arm64 JAM_VERSION="$FIXTURE_VERSION" BINDIR="$WORK/bin3" run_install >/dev/null 2>&1 || true
if grep -q "jam_${FIXTURE_VERSION}_darwin_arm64.tar.gz" "$PATTERN_LOG"; then ok "requested darwin_arm64"; else bad "requested darwin_arm64"; fi
BINDIR="$WORK/bin4" run_install >/dev/null 2>&1 || true
if [ -x "$WORK/bin4/jam" ]; then ok "latest resolves + installs"; else bad "latest resolves + installs"; fi

echo "== compat: an old at-jam update against the jam release fails, changing nothing =="
for LEGACY_SH in "${LEGACY_SHS[@]}"; do
  L="$(basename "$LEGACY_SH" .sh)"
  BIN5="$WORK/bin5-$L"; mkdir -p "$BIN5"
  printf 'running-at-jam\n' >"$BIN5/at-jam"; printf 'running-at-mint\n' >"$BIN5/at-mint"
  before="$(cd "$BIN5" && find . -mindepth 1 | sort | tr '\n' ' ')"
  out="$(COVE_VERSION="$FIXTURE_VERSION" BINDIR="$BIN5" PATH="$STUB_BIN:$PATH" bash "$LEGACY_SH" 2>&1)" && rc=0 || rc=$?
  if [ "$rc" != 0 ]; then ok "$L exits non-zero on a jam release"; else bad "$L exits non-zero on a jam release" "$out"; fi
  assert_eq "$L leaves at-jam byte-identical" running-at-jam "$(cat "$BIN5/at-jam")"
  assert_eq "$L leaves at-mint byte-identical" running-at-mint "$(cat "$BIN5/at-mint")"
  assert_eq "$L adds no file" "$before" "$(cd "$BIN5" && find . -mindepth 1 | sort | tr '\n' ' ')"
  # Control: the same legacy installer still works against a pre-rename release,
  # so the failure above is the missing asset, not a broken harness.
  BIN6="$WORK/bin6-$L"
  out="$(COVE_VERSION="$OLD_VERSION" BINDIR="$BIN6" PATH="$STUB_BIN:$PATH" bash "$LEGACY_SH" 2>&1)" && rc=0 || rc=$?
  if [ "$rc" = 0 ] && [ -x "$BIN6/at-jam" ]; then ok "control: $L still installs a pre-rename release"; else bad "control: $L" "rc=$rc" "$out"; fi
done

echo "== release: check-archive.sh guards the jam archive shape =="
CHECK_SH="$HERE/scripts/release/check-archive.sh"
ca_case() { # ca_case <name> <want: ok|reject> <jam mode> <bins...>
  local name="$1" want="$2" mode="$3" d="$WORK/ca-$1"
  shift 3
  mkdir -p "$d/stage" "$d/dist"
  for b in jam at-cove README.md; do printf 'x\n' >"$d/stage/$b"; done
  chmod "$mode" "$d/stage/jam"
  tar -C "$d/stage" -czf "$d/dist/jam_1-0101_linux_amd64.tar.gz" "$@"
  if bash "$CHECK_SH" "$d/dist" >/dev/null 2>&1; then got=ok; else got=reject; fi
  assert_eq "check-archive $name -> $want" "$want" "$got"
}
ca_case good ok 0755 jam
ca_case mode reject 0644 jam
ca_case tombstone reject 0755 jam at-cove
ca_case extra reject 0755 jam README.md
if bash "$CHECK_SH" "$WORK/no-such-dist" >/dev/null 2>&1; then bad "check-archive no archives -> reject"; else ok "check-archive no archives -> reject"; fi

echo "== e2e: tampered checksum aborts, nothing installed =="
cp "$RELEASE_DIR/checksums.txt" "$WORK/checksums.good"
printf '%s  %s\n' "$(sha_of /dev/null)" "jam_${FIXTURE_VERSION}_linux_amd64.tar.gz" >"$RELEASE_DIR/checksums.txt"
JAM_VERSION="$FIXTURE_VERSION" BINDIR="$WORK/bin7" run_install >/dev/null 2>&1 && rc=0 || rc=$?
if [ "$rc" != 0 ]; then ok "tamper aborts (nonzero exit)"; else bad "tamper aborts (nonzero exit)"; fi
if [ -e "$WORK/bin7/jam" ]; then bad "no binary installed on tamper"; else ok "no binary installed on tamper"; fi
cp "$WORK/checksums.good" "$RELEASE_DIR/checksums.txt"

echo
echo "-------- $pass passed, $fail failed --------"
[ "$fail" -eq 0 ]
