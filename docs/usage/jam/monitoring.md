---
summary: Operator attention — the conditions Jam raises about itself (lapsed credentials, failing pool refreshes, …), where they show (admin UI Health tab + rail badge, `at-jam attention list`, `/metrics`), and the shipped Prometheus + Alertmanager bundle that pushes them to Discord.
read_when: You want to be alerted when Jam needs a human, are setting up `metrics:` or the deploy/monitoring bundle, see a condition on the Health tab and want to know what it means, or a Discord alert from Jam arrived.
owns: the condition model (keys, severities, lifecycle), the v1 condition kinds, the `/metrics` exposition and its scrape token, the Health tab, `at-jam attention list`, and the deploy/monitoring bundle
prereqs: serve.md for the serve config and credentials
tier: leaf
updated: 2026-10-07
---

# Operator attention

Jam turns problems it detects in itself into **conditions** — a key, a
severity, a one-line summary and a **fix** — and shows them in three places:
the admin UI (rail badge, "Needs attention", **Health** tab), `at-jam attention
list`, and `/metrics` for Prometheus. Delivery (Discord, reminders, ack) is
Alertmanager's job, via the bundle in `deploy/monitoring/`.

## Severities and lifecycle

| Severity | Meaning | Pushed |
|---|---|---|
| `critical` | agents are failing now | yes, re-sent hourly |
| `warning` | degraded, or will fail | yes, re-sent every 12h |
| `info` | shown on Health only | never |

A condition is **open** until its producer sees success, then **resolved** (kept
7 days on Health). The same problem recurring later opens a new occurrence.
Conditions survive a Jam restart and keep their original start time.
**Ack** = an Alertmanager silence (its UI, or the Health tab's "Silences" link).

## v1 conditions

| Key | Severity | Raised when | Cleared when |
|---|---|---|---|
| `cred.unavailable:<cred>` | critical | 3 consecutive failures resolving a brokered credential | the next successful resolve |
| `pool.account.refresh:<account>` | warning; critical while every account fails | 2 consecutive failed refresh passes | the next successful refresh |

Each carries its fix (e.g. `gcloud auth application-default login` for a
gcp-exchange ADC). Conditions name things only — never secret values.

## `/metrics`

Enable with the serve config `metrics: { token-cred: <name> }` (the name must be
under `credentials:`; its value — the scrape token — comes from the credentials
file). Jam then serves `/metrics` on the **broker listener** (the TLS port
studios use), requiring `Authorization: Bearer <scrape token>`; studio identity
tokens are refused. Series: `jam_up`, `jam_attention_condition{key,kind,severity,summary,fix}`
(one per open condition), `jam_attention_open{severity}`, `jam_studios`.
Unset ⇒ no `/metrics`.

- `token-cred` must be a plain credential, not an `exchange: gcp` one.
- The scrape token is read once at startup; after rotating it, restart
  `at-jam serve` (and update `scrape-token` for the bundle).

## The local bundle

1. Put three files in `deploy/monitoring/.local/` (gitignored): `scrape-token`
   (the token's value), `jam-ca.pem` (the CA that signed Jam's `tls:` cert) and
   `discord-webhook` (a Discord channel webhook URL).
2. `just monitoring-up <jam-host>` — starts Prometheus (`127.0.0.1:9090`) and
   Alertmanager (`127.0.0.1:9093`), reaching Jam the way studios do
   (`<jam-host>` → `host-gateway`). The recipe `chmod 644`s the three files, because
   the containers run as `nobody` and could not read 0600 files; the directory is local and gitignored.
3. Set `metrics.alertmanager-url: http://localhost:9093` to link silences from Health.

The bundle also alerts `JamDown` (critical) when Prometheus cannot scrape Jam
for 2 minutes. `just monitoring-down` stops it. In other environments, point
your own Prometheus at the same endpoint and load `deploy/monitoring/jam-rules.yml`.
