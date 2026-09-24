# pg-tunnel-collab Kit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a self-contained example kit, `kits/pg-tunnel-collab/`, that lets an `at-cove chat` collaborator reach a customer's external Postgres by tunneling the PG wire protocol over WSS/443 with `wstunnel`, riding the existing squid `CONNECT`-to-:443 egress.

**Architecture:** A kit is a `config.yml` + an `image/` template tree. This kit bakes a pinned `wstunnel` binary and an idempotent `pg-tunnel-up` launcher into the image (build-time egress is open), declares the tunnel's four values as `collaborators.db-analyst.secrets` (injected as session env, never to disk), and widens egress by exactly one host — the tunnel server — under the collaborator class. At session start the human/agent runs `pg-tunnel-up`, which starts the `wstunnel` client through squid and exposes `127.0.0.1:5432`; the agent then uses `psql "$DATABASE_URL"` normally.

**Tech Stack:** Bash (helper + hermetic test), a `FROM`-cove-base Dockerfile, YAML config, Markdown RUNBOOK. No Go, no core, no `internal/assemble/hardening/` change.

**Spec:** `docs/superpowers/specs/2026-09-24-pg-tunnel-collab-kit-design.md`

## Global Constraints

- **No Go/core/hardening change.** Only files under `kits/pg-tunnel-collab/` plus one docs pointer in `docs/OVERVIEW.md`. Nothing under `internal/assemble/hardening/`.
- **Pin `wstunnel` v11.0.0**, asset `wstunnel_11.0.0_linux_amd64.tar.gz`, sha256 `9708a99717b5a951453c2ff7c14c25d3418d02ca7fcb96fdb382a8f2083bab5e`.
- **Secrets never written to disk or logged.** `pg-tunnel-up` must not echo secret values or write them to a file; the `wstunnel` argv (carrying `PG_TUNNEL_SECRET`) stays VM-local and is never printed.
- **Sole egress widening** is the tunnel server host, under `collaborators.db-analyst.allowed-domains` (least-privilege, session-scoped) — not root `image.allowed-domains`.
- **End-to-end PG TLS** is expressed as `host=<db-hostname> hostaddr=127.0.0.1 … sslmode=verify-full` — dial the loopback tunnel entrance, verify the cert against the real DB hostname. Never downgrade to `sslmode=require`.
- **Run path is interactive `chat` only** — a `db-analyst` collaborator class. No `source-control`/`tracker`/`dispatch`/`workers` (not needed for `chat`).

---

### Task 1: Kit `config.yml`

**Files:**
- Create: `kits/pg-tunnel-collab/config.yml`

**Interfaces:**
- Produces: a `db-analyst` collaborator class declaring env-var secrets `PG_TUNNEL_URL`, `PG_TUNNEL_SECRET`, `PG_TARGET`, `DATABASE_URL`, and one session-scoped allow-list entry `tunnel.example.com`. Task 2's helper consumes the first three (plus `PG_LOCAL`) at runtime; Task 4's RUNBOOK documents supplying their values.

- [ ] **Step 1: Write `config.yml`**

```yaml
# pg-tunnel-collab — an EXAMPLE collaborator kit. It lets an `at-cove chat`
# session reach a customer's external Postgres by tunneling the PG wire protocol
# over WSS/443 with wstunnel, through the existing squid CONNECT-to-:443 egress.
# No change to at-cove core or the sealed hardening layer. See RUNBOOK.md.
name: pg-tunnel-collab

# No root `image.allowed-domains`: Anthropic is already in the sealed base, and
# the only widening this kit needs — the wstunnel server host — belongs to the
# db-analyst class below (least-privilege, session-scoped).
collaborators:
  db-analyst:
    # The ONE egress widening: the wstunnel server. squid already permits
    # CONNECT to :443 for an allow-listed host, so the WSS tunnel needs nothing
    # more than this hostname. Replace with your real tunnel host.
    allowed-domains:
      - tunnel.example.com
    # Declared demand-only (name + description); every value is supplied
    # machine-side in ~/.config/at-cove/secrets.yml and injected as session env,
    # never written to the kit or to disk. See RUNBOOK.md.
    secrets:
      PG_TUNNEL_URL:
        description: "wss URL of the wstunnel server, e.g. wss://tunnel.example.com:443"
      PG_TUNNEL_SECRET:
        description: "shared HTTP-upgrade path-prefix authenticating to the wstunnel server"
      PG_TARGET:
        description: "customer Postgres host:port the server forwards to, e.g. db.internal:5432"
      DATABASE_URL:
        description: "libpq conn string; dial loopback but verify the real cert — host=db.internal hostaddr=127.0.0.1 port=5432 sslmode=verify-full"
```

- [ ] **Step 2: Validate the config parses**

Run: `just run -- --dry-run install --project-dir kits/pg-tunnel-collab`
Expected: exits 0; prints a dry-run/preview line for the kit and resolves no secrets, builds nothing (a `--dry-run install` is a pure preview that only locates + loads + validates `config.yml`). No parse/validation error.

If it errors that a root `image` or `source-control`/`tracker` block is required (it should not for a chat-only kit), add `image:\n  allowed-domains: []` and re-run — do not add a tracker.

- [ ] **Step 3: Commit**

```bash
git add kits/pg-tunnel-collab/config.yml
git commit -m "feat(kit): pg-tunnel-collab config — db-analyst class + tunnel secrets"
```

---

### Task 2: `pg-tunnel-up` launcher + hermetic smoke test

**Files:**
- Create: `kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up`
- Test: `kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up.test.sh`

**Interfaces:**
- Consumes (env): `PG_TUNNEL_URL`, `PG_TUNNEL_SECRET`, `PG_TARGET` (required); `PG_LOCAL` (default `127.0.0.1:5432`); `HTTPS_PROXY` (set in every session to `http://127.0.0.1:3128`). Test knobs with safe defaults: `PG_TUNNEL_LOG` (default `/tmp/pg-tunnel.log`), `PG_TUNNEL_WAIT_TRIES` (default `30`), `PG_TUNNEL_WAIT_SLEEP` (default `0.5`).
- Produces: `/usr/local/bin/pg-tunnel-up` (Task 3 copies it into the image; Task 4's RUNBOOK invokes it). Exit 0 when `PG_LOCAL` accepts; non-zero + fail-loud message otherwise.

- [ ] **Step 1: Write the failing test**

Create `kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up.test.sh`:

```bash
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up.test.sh`
Expected: FAIL — the harness can't find/execute `pg-tunnel-up` (it doesn't exist yet), so all three cases report `FAIL` and it exits non-zero.

- [ ] **Step 3: Write `pg-tunnel-up`**

Create `kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up`:

```bash
#!/usr/bin/env bash
# Bring up the wstunnel client that fronts the customer's Postgres, exposing it
# on a local port the agent's libpq/psql connects to. Idempotent and fail-loud.
# Runs the WSS handshake through squid ($HTTPS_PROXY), so it rides the existing
# CONNECT-to-:443 egress — no hardening change. Never logs secret values.
set -euo pipefail

PG_LOCAL="${PG_LOCAL:-127.0.0.1:5432}"
LOG="${PG_TUNNEL_LOG:-/tmp/pg-tunnel.log}"
TRIES="${PG_TUNNEL_WAIT_TRIES:-30}"
SLEEP="${PG_TUNNEL_WAIT_SLEEP:-0.5}"

host="${PG_LOCAL%:*}"
port="${PG_LOCAL##*:}"

port_up() { (exec 3<>"/dev/tcp/$host/$port") >/dev/null 2>&1; }

if port_up; then
  echo "pg-tunnel: already up on $PG_LOCAL"
  exit 0
fi

: "${PG_TUNNEL_URL:?PG_TUNNEL_URL is required (wss://host:443)}"
: "${PG_TUNNEL_SECRET:?PG_TUNNEL_SECRET is required}"
: "${PG_TARGET:?PG_TARGET is required (dbhost:5432)}"
: "${HTTPS_PROXY:?HTTPS_PROXY is required (the in-cove squid proxy)}"

# Do not echo PG_TUNNEL_URL/SECRET/TARGET — they are customer secrets.
echo "pg-tunnel: starting wstunnel client -> $PG_LOCAL"
nohup wstunnel client \
  -L "tcp://$PG_LOCAL:$PG_TARGET" \
  --http-upgrade-path-prefix "$PG_TUNNEL_SECRET" \
  --http-proxy "$HTTPS_PROXY" \
  "$PG_TUNNEL_URL" >>"$LOG" 2>&1 &

i=0
while [ "$i" -lt "$TRIES" ]; do
  if port_up; then
    echo "pg-tunnel: ready on $PG_LOCAL"
    exit 0
  fi
  i=$((i + 1))
  sleep "$SLEEP"
done

echo "pg-tunnel: FAILED — $PG_LOCAL did not come up in time" >&2
echo "---- tail $LOG ----" >&2
tail -n 20 "$LOG" >&2 2>/dev/null || true
exit 1
```

Then make it executable:

Run: `chmod +x kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up.test.sh`

- [ ] **Step 4: Run the test to verify it passes**

Run: `bash kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up.test.sh`
Expected: three `ok -` lines then `all passed`, exit 0.

- [ ] **Step 5: Lint the helper**

Run: `shellcheck kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up`
Expected: no output, exit 0. (If shellcheck flags the intentional `/dev/tcp` redirect or `set -e` with background jobs, resolve by the narrowest fix or a scoped `# shellcheck disable=SCxxxx` with a one-line reason — never a blanket disable.)

- [ ] **Step 6: Commit**

```bash
git add kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up.test.sh
git commit -m "feat(kit): pg-tunnel-up launcher + hermetic smoke test"
```

---

### Task 3: `image/Dockerfile`

**Files:**
- Create: `kits/pg-tunnel-collab/.at-cove/image/Dockerfile`

**Interfaces:**
- Consumes: `pg-tunnel-up` from Task 2 (copied to `/usr/local/bin`); the pinned wstunnel release from Global Constraints.
- Produces: an image with `/usr/local/bin/wstunnel` and `/usr/local/bin/pg-tunnel-up` on PATH.

- [ ] **Step 1: Write the Dockerfile**

Create `kits/pg-tunnel-collab/.at-cove/image/Dockerfile`:

```dockerfile
# pg-tunnel-collab image — the blessed cove base plus the wstunnel client and the
# pg-tunnel-up launcher. at-cove injects the blessed base via --build-arg when it
# builds this kit; the default below is only for a bare manual `docker build`.
ARG COVE_BASE_IMAGE=ghcr.io/aethons-tools/cove-image:latest
FROM ${COVE_BASE_IMAGE}

# Build-time egress is open. Pin the wstunnel release + verify its checksum so
# the build is reproducible and supply-chain-honest.
ARG WSTUNNEL_VERSION=11.0.0
ARG WSTUNNEL_SHA256=9708a99717b5a951453c2ff7c14c25d3418d02ca7fcb96fdb382a8f2083bab5e
RUN curl -fsSL -o /tmp/wstunnel.tar.gz \
      "https://github.com/erebe/wstunnel/releases/download/v${WSTUNNEL_VERSION}/wstunnel_${WSTUNNEL_VERSION}_linux_amd64.tar.gz" \
 && echo "${WSTUNNEL_SHA256}  /tmp/wstunnel.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/wstunnel.tar.gz -C /usr/local/bin wstunnel \
 && chmod +x /usr/local/bin/wstunnel \
 && rm /tmp/wstunnel.tar.gz \
 && wstunnel --version

# The idempotent launcher the db-analyst session runs before touching the DB.
COPY pg-tunnel-up /usr/local/bin/pg-tunnel-up
RUN chmod +x /usr/local/bin/pg-tunnel-up
```

- [ ] **Step 2: Verify the pinned checksum matches the release (hermetic)**

Run:
```bash
grep -q '9708a99717b5a951453c2ff7c14c25d3418d02ca7fcb96fdb382a8f2083bab5e' kits/pg-tunnel-collab/.at-cove/image/Dockerfile \
  && grep -q 'WSTUNNEL_VERSION=11.0.0' kits/pg-tunnel-collab/.at-cove/image/Dockerfile \
  && grep -q 'COPY pg-tunnel-up /usr/local/bin/pg-tunnel-up' kits/pg-tunnel-collab/.at-cove/image/Dockerfile \
  && echo PIN-OK
```
Expected: `PIN-OK`. (The sha is the published `checksums.txt` value for `wstunnel_11.0.0_linux_amd64.tar.gz`; a real `docker build` is an integration step requiring docker + network and is deferred — the Dockerfile's own `sha256sum -c` is the build-time gate.)

- [ ] **Step 3: Commit**

```bash
git add kits/pg-tunnel-collab/.at-cove/image/Dockerfile
git commit -m "feat(kit): Dockerfile bakes pinned wstunnel v11.0.0 + pg-tunnel-up"
```

---

### Task 4: RUNBOOK + docs pointer

**Files:**
- Create: `kits/pg-tunnel-collab/RUNBOOK.md`
- Modify: `docs/OVERVIEW.md:845` (add a sibling pointer after the reference-worker line)

**Interfaces:**
- Consumes: the `config.yml` secret names (Task 1), the `pg-tunnel-up` contract (Task 2), the pinned wstunnel version (Task 3).
- Produces: the single source of operator detail for the kit, reachable from `docs/OVERVIEW.md`.

- [ ] **Step 1: Write `RUNBOOK.md`**

Create `kits/pg-tunnel-collab/RUNBOOK.md`:

````markdown
# pg-tunnel-collab RUNBOOK

An **example** collaborator kit: an `at-cove chat` session (`db-analyst` class)
reaches a customer's external Postgres by tunneling the PG wire protocol over
**WSS/443** with [`wstunnel`](https://github.com/erebe/wstunnel), through the
cove's existing squid `CONNECT`-to-:443 egress. No change to at-cove core or the
sealed hardening layer — the only egress widening is the tunnel server's hostname.

## How it fits the egress model

A cove's agent has no direct network; all egress goes through squid, which permits
`CONNECT` only to **:443** and only to allow-listed hostnames. `wstunnel` wraps the
Postgres TCP stream in a WSS connection to a server on :443, so it rides that exact
path. Allow-listing the tunnel host (below) is the whole change.

```
psql "$DATABASE_URL"  ->  127.0.0.1:5432 (wstunnel client)
   -> squid CONNECT tunnel.example.com:443  ->  wstunnel server  ->  customer Postgres
```

## The server side (you run this, in front of the customer DB)

On a host that terminates TLS on :443 and can reach the customer Postgres:

```bash
wstunnel server \
  --restrict-to db.internal:5432 \
  --restrict-http-upgrade-path-prefix "$PG_TUNNEL_SECRET" \
  wss://0.0.0.0:443
```

- `--restrict-to` stops the server being an open relay — it may forward only to the DB.
- `--restrict-http-upgrade-path-prefix` requires the shared secret the client sends.
- Terminate TLS on :443 (front it with your own cert / a TLS proxy as needed).

## Supply the secrets (machine-side, out of source control)

In `~/.config/at-cove/secrets.yml`, provide values for the four `db-analyst`
secrets declared in `config.yml` (see the repo's secrets doc for the file format):

| secret | example |
|---|---|
| `PG_TUNNEL_URL` | `wss://tunnel.example.com:443` |
| `PG_TUNNEL_SECRET` | a long random string (also on the server's `--restrict-http-upgrade-path-prefix`) |
| `PG_TARGET` | `db.internal:5432` |
| `DATABASE_URL` | `host=db.internal hostaddr=127.0.0.1 port=5432 user=… dbname=… sslmode=verify-full` |

**End-to-end TLS — read this.** `DATABASE_URL` uses `hostaddr=127.0.0.1` to dial the
local tunnel entrance but `host=db.internal` so libpq verifies the Postgres server
certificate against the *real* DB hostname. This keeps `sslmode=verify-full` working
through the tunnel, so the relay never sees plaintext credentials or data. Plain
`host=127.0.0.1 sslmode=verify-full` fails hostname verification — do **not** "fix"
that by downgrading to `sslmode=require`; use the `host`/`hostaddr` split instead.
Also point the tunnel host in `config.yml` (`collaborators.db-analyst.allowed-domains`)
at your real server, and rotate `PG_TUNNEL_SECRET` periodically.

## Run it

```bash
at-cove install --project-dir kits/pg-tunnel-collab   # build the image
at-cove chat db-analyst --project-dir kits/pg-tunnel-collab
# then, inside the session:
pg-tunnel-up            # brings the tunnel up (idempotent; safe to re-run)
psql "$DATABASE_URL"    # -> 127.0.0.1:5432 -> tunnel -> customer Postgres
```

## Prototype simplifications

- **Auth** is a shared HTTP-upgrade path-prefix secret, not mTLS.
- **Launch** is manual (`pg-tunnel-up`, idempotent), not a supervised service —
  a plain cove has no service manager. Re-run the helper if the tunnel drops.

## Test the launcher

```bash
bash kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up.test.sh   # hermetic, no network
shellcheck kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up
```
````

- [ ] **Step 2: Add the docs pointer**

Edit `docs/OVERVIEW.md` — after the existing line 845:

```
A reference dispatch worker implementation lives at `kits/reference-worker/`; see `RUNBOOK.md` for the end-to-end run with `just e2e`.
```

add:

```
An example collaborator kit that reaches a customer's external Postgres by tunneling it over WSS/443 with `wstunnel` — no hardening change — lives at `kits/pg-tunnel-collab/`; see its `RUNBOOK.md`.
```

- [ ] **Step 3: Verify the pointer resolves**

Run: `grep -q 'kits/pg-tunnel-collab/' docs/OVERVIEW.md && test -f kits/pg-tunnel-collab/RUNBOOK.md && echo DOCS-OK`
Expected: `DOCS-OK`.

- [ ] **Step 4: Commit**

```bash
git add kits/pg-tunnel-collab/RUNBOOK.md docs/OVERVIEW.md
git commit -m "docs(kit): pg-tunnel-collab RUNBOOK + OVERVIEW pointer"
```

---

### Task 5: Final verification + PR

- [ ] **Step 1: Re-run all hermetic checks together**

Run:
```bash
bash kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up.test.sh \
 && shellcheck kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up \
 && just run -- --dry-run install --project-dir kits/pg-tunnel-collab \
 && echo ALL-GREEN
```
Expected: `all passed`, clean shellcheck, a clean config dry-run, then `ALL-GREEN`.

- [ ] **Step 2: Confirm no out-of-scope files changed**

Run: `git diff --stat main -- ':!kits/pg-tunnel-collab' ':!docs/OVERVIEW.md' ':!docs/superpowers'`
Expected: empty output (only the kit, the OVERVIEW pointer, and the spec/plan under `docs/superpowers/` changed; nothing under `internal/`).

- [ ] **Step 3: Open the PR**

```bash
git push -u origin pg-tunnel-collab-kit
gh pr create --base main --title "pg-tunnel-collab: reach an external Postgres over WSS/443 (kit-only)" \
  --body "Adds an example collaborator kit that tunnels the Postgres wire protocol over WSS/443 with wstunnel, riding the existing squid CONNECT-to-:443 egress. No at-cove core or hardening change. Spec + plan under docs/superpowers/.

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

---

## Self-Review

**Spec coverage:** §1 core claim → Tasks 1+3+RUNBOOK; §2 layout → all tasks; §3 helper → Task 2 (env table, idempotent/fail-loud, `--http-proxy` through squid); §4 config → Task 1; §5 Dockerfile → Task 3 (pinned v11.0.0 + real sha256); §6 RUNBOOK+server → Task 4; §"Prototype simplifications" → RUNBOOK; §Testing → Task 2 (shellcheck + stub smoke test) and Task 1 (config validation); §Docs → Task 4. All covered.

**Placeholder scan:** No `TBD`/`TODO`. `tunnel.example.com`, `db.internal`, and the `DATABASE_URL` fields are illustrative operator-supplied values (correct for an example kit), documented as such in the RUNBOOK. The wstunnel version and sha256 are pinned to real published values, not placeholders.

**Type/name consistency:** Env var names (`PG_TUNNEL_URL`, `PG_TUNNEL_SECRET`, `PG_TARGET`, `PG_LOCAL`, `DATABASE_URL`) and the file paths (`/usr/local/bin/pg-tunnel-up`, `/usr/local/bin/wstunnel`) are identical across the config, helper, test, Dockerfile, and RUNBOOK. The wstunnel invocation flags in the helper match those verified for v11.0.0 (`client -L tcp://…`, `--http-upgrade-path-prefix`, `--http-proxy`) — confirm once against `wstunnel client --help` when the image first builds.
