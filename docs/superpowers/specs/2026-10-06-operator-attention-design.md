# Operator attention: Jam detects, Prometheus + Alertmanager deliver

**Status:** design approved in brainstorming (2026-10-06); spec, pre-plan.
**Owner:** Jam service (admin-spider).
**Related:** [`2026-10-06-intercom-identity-and-channels-design.md`](2026-10-06-intercom-identity-and-channels-design.md) — deliberately *not* a dependency (see Decisions).

## Summary

Jam has no way to get an operator's attention when the service itself needs a
human: a brokered credential lapses (e.g. a Google Cloud ADC hitting session
control — #348 only logs a WARN), a pool account stops refreshing, a standing
session crash-loops. Today these are log lines nobody is watching.

This design adds an **attention core** inside Jam that turns such failures into
**conditions** — named, severity-tagged, with a remedy — keeps them in Postgres,
shows them on an admin-UI **health page**, and exports them as Prometheus
metrics. **Delivery is not Jam's job:** a shipped Prometheus + Alertmanager
bundle scrapes Jam and handles routing, severity-based reminders, resolved
notices, silences (ack) and Discord.

## Goals (ranked, from the operator)

1. **Alerts** — never silently miss something that is breaking agents.
2. **A health view** — one place listing everything currently degraded.
   *Success is reaching this.*
3. **Actionable alerts** — the alert carries (later: performs) the fix. *Better still.*

## Decisions (brainstorming record)

1. **Recipients:** every condition goes to Jam's operators (one audience, no
   per-project routing in v1). Operators are **not** intercom users; they may
   become their own registry type later.
2. **Surface undecided ⇒ decouple detection from delivery.** The intercom is not
   assumed to be the right surface.
3. **Lifecycle:** severity-driven reminders until acked or cleared
   (critical/warning/info).
4. **Approach A′ (hybrid):** Jam owns detection, state and the health view;
   Alertmanager owns delivery, reminders and ack. Chosen over an all-in-Jam
   notifier (more code, re-implements Alertmanager) and over an intercom-based
   design (blocked on intercom slices, operators aren't users there).
5. **Pull, not push** — including locally. Jam serves `/metrics`; the local dev
   environment is made to work with scraping rather than adding a push path.
6. **Ack = an Alertmanager silence.** Jam stores no ack state. A Jam-side
   "silence" button (bridge) is deferred to slice 3.

## 1. Architecture and the condition model

```
edge producers ──Raise/Clear──▶ attention.Tracker ──▶ Postgres (attention_conditions)
level checkers ──Reconcile───▶        │
                                      ├──▶ /ui/health + banner, GET /admin/attention, `at-jam attention list`
                                      └──▶ GET /metrics (broker listener, scrape token)
                                                 │
                                   Prometheus (rules) ──▶ Alertmanager ──▶ Discord
```

**Condition** (`internal/jam/attention`):

| Field | Meaning |
|---|---|
| `key` | Stable, unique; `<kind>:<subject>`, e.g. `cred.unavailable:vertex-gcp`. Charset `[a-z0-9._:/-]`, ≤ 200 bytes. |
| `kind` | The key's prefix; groups and documents a condition type. |
| `severity` | `critical` (agents failing now) · `warning` (degraded / will fail) · `info` (shown, never pushed). |
| `summary` | One line, ≤ 200 bytes. |
| `detail` | Optional, ≤ 2 KiB. |
| `fix` | The exact remedy (a command or the config entry to check), ≤ 500 bytes. |
| `since`, `last_seen`, `resolved_at` | Timestamps. |

Conditions **name things only** — credential, account, project, role, kit names
and short error codes. Never secret values, tokens or raw upstream error bodies.

**Producers:**

- **Edge producers** — code that already observes failure and recovery calls
  `Raise(c)` / `Clear(key)`. Each debounces: raise after N consecutive failures
  (per-kind default), clear on the first success.
- **Level checkers** — `func(ctx) []Condition` per kind on a ticker; the tracker
  reconciles that kind's set (raise new, clear absent).

**Tracker:** an in-memory map under a mutex; `Raise`/`Clear` are O(1) and never
block — persistence to Postgres is asynchronous and coalesced, retried on the
next change, and a tracker failure never fails the caller (e.g. a broker
request). Resolved conditions are kept 7 days. On startup, open conditions load
from Postgres; level checkers re-evaluate within one tick (clearing what is no
longer true); edge conditions stay open until their producer's next success — a
lapsed credential must not silently clear on restart.

## 2. v1 conditions

| Key | Sev | Detection | Fix hint |
|---|---|---|---|
| `cred.unavailable:<name>` | critical | edge: `GCPTokenResolver` lapse/recover (#348) and the broker's credential-resolve failure for any destination credential; clear on next success | gcp-exchange ADC: "`gcloud auth application-default login` on the Jam host"; else "check `credentials.yml` entry `<name>`" |
| `pool.account.refresh:<account>` | warning; critical when every account fails | edge: the pool refresher's per-account failure/success | "re-seed: `at-jam pool add --name <account> --from-file …`" |
| `standing.crashloop:<project>/<role>/<name>` | critical | level: ≥ 3 consecutive failed starts (in restart backoff) | "`at-jam standing reset …` or check the role's kit / model-spec" |
| `image.stale:<kit>` | info | level: the existing staleness report (#345) | "`at-jam standing upgrade …`" |
| `requisitioner.poll:<project>` | warning | edge: N (3) consecutive tracker-poll failures / success | "check the tracker token cred `<name>`" |
| `relay.discord` | warning | edge: N consecutive Discord egress/gateway failures | "check `runtime.discord.bot-token-cred`" |
| `JamDown` | critical | Prometheus rule `up{job="jam"} == 0` for 2m — not a Jam condition | "Jam unreachable from Prometheus" |

Deferred: broker 5xx/upstream-error rates (metric + rule), store latency, disk,
per-ticket session failures.

## 3. Export: `/metrics` on the broker listener

The admin listener is loopback-only by design (off-loopback needs TLS + OIDC),
so containers can't count on reaching it. `/metrics` is served on the **broker
listener** (`listen:`, TLS), which coves already reach from containers via
`jam-host` → `host-gateway`; no exposure rule changes.

- **Auth:** a dedicated scrape token, `metrics: { token-cred: <name> }` in the
  serve config, supplied by the credentials file. Compared in constant time.
  Missing/wrong token → 401. Cove identity tokens are **not** accepted.
  `metrics:` unset → `/metrics` is 404 (off, never open).
- **Series:**
  - `jam_attention_condition{key,kind,severity,summary,fix} 1` per open condition;
  - `jam_up 1`, plus a few raw gauges (open studios, healthy pool accounts).

## 4. Health view, admin API and CLI

- **`/ui/health` (Jam "Health" tab)** — open conditions sorted critical → warning → info, then
  oldest; each row: severity chip, summary, relative `since`, `fix` in a
  copyable box, expandable `detail`. Below: resolved in the last 7 days with
  duration. htmx-polled like the other live pages.
- **Rail badge instead of a banner** *(amended 2026-10-07)*: open critical
  and warning conditions are Jam-scope items in the admin UI's existing
  attention system (#393) — red for critical, amber for warning — so they count
  in the rail and Health-tab badges and the dashboard's "Needs attention"
  card. Info conditions appear on the Health tab only.
- **`GET /admin/attention?state=open|resolved|all`** (JSON, admin-gated) and
  **`at-jam attention list [--all]`** (`SEV KEY SINCE SUMMARY` + fix line).
- **Optional** `metrics.alertmanager-url`: the health page links to
  Alertmanager's silences. Showing silenced state and a Silence button is slice 3.

## 5. Monitoring bundle: `deploy/monitoring/`

- `compose.yml` — pinned `prom/prometheus` and `prom/alertmanager`;
  `extra_hosts: <jam-host>:host-gateway` (as coves do).
- `prometheus.yml` — https scrape of `<jam-host>/metrics` every 30 s with the
  token file and Jam's CA.
- `jam-rules.yml` — `JamAttention` (`jam_attention_condition == 1`, severity
  and annotations carried from labels) and `JamDown`.
- `alertmanager.yml` — group by `key`; route by `severity`: critical
  `repeat_interval: 1h`, warning `12h`, info → no receiver; `send_resolved`;
  Discord receiver whose webhook URL is read from an uncommitted file.
- `just monitoring-up` / `monitoring-down`. Documented in a new
  `docs/usage/jam/monitoring.md`.

**Dead-man:** Jam being down is caught by `JamDown`. Prometheus or Alertmanager
themselves being down is out of scope (no external watchdog in v1).

## 6. Testing

- Tracker: raise/clear/reconcile, debounce, async persistence, reload semantics
  (edge stays open, level re-evaluates).
- `/metrics`: golden exposition; auth matrix (none / wrong / cove token /
  scrape token / `metrics:` unset → 404); label validation.
- Producers: at each hook — the GCP resolver's existing lapse test, fakes for the
  pool refresher and standing supervisor.
- Health page, admin API, CLI via the existing adminui / cmd test patterns.
- Bundle: `promtool check rules` and `amtool check-config` (CI if the images
  are available, otherwise manual).
- **Slice-1 acceptance:** local e2e — the bundle scrapes a real `serve`, a forced
  condition reaches Alertmanager.

## Slices

Each ships independently, in order, with its own plan and PRs.

1. **Core + health view** — tracker + Postgres table, `/metrics` + scrape token,
   `/ui/health` + banner, admin API + CLI, the monitoring bundle; producers
   `cred.unavailable` and `pool.account.refresh`. *(Meets goal 2.)*
2. **Remaining v1 producers** — standing crash-loop, stale image, requisitioner,
   Discord relay.
3. **Ack bridge** — silenced state and Silence buttons on the health page via
   the Alertmanager API.
4. **Actionable** — one-click fixes, chosen from what slices 1–2 show is common.

## Out of scope

- Per-project routing or per-kind recipients (all go to operators).
- An operator registry; intercom delivery (a later notifier if wanted).
- A push path (Pushgateway, remote write, OTLP) — pull only.
- Jam-side reminder scheduling or ack state.
