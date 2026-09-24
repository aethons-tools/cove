#!/usr/bin/env bash
# Hermetic smoke test for pg-tunnel-up. No network, no real wstunnel: a stub
# wstunnel on PATH and a throwaway python listener stand in for the real thing.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SUT="$HERE/pg-tunnel-up"
FAILS=0

pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1"; FAILS=$((FAILS + 1)); }

# --- Case 1: idempotent re-use — port already up, wstunnel must NOT be called.
work="$(mktemp -d)"
python3 -m http.server 55432 --bind 127.0.0.1 >/dev/null 2>&1 &
listener=$!
# wait for the listener to accept
for _ in $(seq 1 30); do (exec 3<>/dev/tcp/127.0.0.1/55432) 2>/dev/null && break; sleep 0.1; done
cat >"$work/wstunnel" <<EOF
#!/usr/bin/env bash
touch "$work/wstunnel-was-called"
sleep 60
EOF
chmod +x "$work/wstunnel"
out="$(PATH="$work:$PATH" PG_LOCAL=127.0.0.1:55432 \
       PG_TUNNEL_URL=wss://x:443 PG_TUNNEL_SECRET=s PG_TARGET=db:5432 \
       HTTPS_PROXY=http://127.0.0.1:3128 "$SUT" 2>&1)"
rc=$?
kill "$listener" 2>/dev/null
if [ "$rc" -eq 0 ] && [ ! -e "$work/wstunnel-was-called" ] && echo "$out" | grep -q "already up"; then
  pass "idempotent re-use skips wstunnel when the port is up"
else
  fail "idempotent re-use (rc=$rc, out=$out)"
fi
rm -rf "$work"

# --- Case 2: missing required env fails loud and names the missing var.
out="$(PG_LOCAL=127.0.0.1:55433 \
       PG_TUNNEL_SECRET=s PG_TARGET=db:5432 HTTPS_PROXY=http://127.0.0.1:3128 \
       "$SUT" 2>&1)"
rc=$?
if [ "$rc" -ne 0 ] && echo "$out" | grep -q "PG_TUNNEL_URL"; then
  pass "missing env fails loud naming PG_TUNNEL_URL"
else
  fail "missing env (rc=$rc, out=$out)"
fi

# --- Case 3: fail-loud timeout — stub wstunnel never opens the port.
work="$(mktemp -d)"
cat >"$work/wstunnel" <<'EOF'
#!/usr/bin/env bash
sleep 60
EOF
chmod +x "$work/wstunnel"
out="$(PATH="$work:$PATH" PG_LOCAL=127.0.0.1:55433 \
       PG_TUNNEL_URL=wss://x:443 PG_TUNNEL_SECRET=s PG_TARGET=db:5432 \
       HTTPS_PROXY=http://127.0.0.1:3128 \
       PG_TUNNEL_LOG="$work/log" PG_TUNNEL_WAIT_TRIES=2 PG_TUNNEL_WAIT_SLEEP=0.1 \
       "$SUT" 2>&1)"
rc=$?
pkill -f "$work/wstunnel" 2>/dev/null
if [ "$rc" -ne 0 ] && echo "$out" | grep -q "did not come up"; then
  pass "fail-loud timeout when the port never opens"
else
  fail "timeout path (rc=$rc, out=$out)"
fi
rm -rf "$work"

echo "----"
if [ "$FAILS" -eq 0 ]; then echo "all passed"; exit 0; else echo "$FAILS failed"; exit 1; fi
