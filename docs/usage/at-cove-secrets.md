---
summary: How an at-cove kit declares the secrets a sandbox needs and how their values are resolved — the config.yml `secrets` map, host-side resolver commands, the user-owned ~/.config/at-cove/secrets.yml supply file, precedence, fail-closed behavior, and the host-execution security caveat.
read_when: You are adding a secret to a kit, wiring a resolver command, supplying a value from your machine, or reasoning about the trust boundary of at-cove secrets.
owns: at-cove secret declaration + value resolution (config.yml `secrets` and ~/.config/at-cove/secrets.yml)
prereqs: at-cove-config.md — the config.yml schema this is part of; ../OVERVIEW.md — the connect/injection data flow
tier: leaf
updated: 2026-07-13
---

# at-cove secrets

A **secret** is an environment variable the sandbox needs (e.g. `GITHUB_TOKEN`). Kits
**declare secrets by name** in `config.yml`; **values are resolved on the host** at
`connect`/`dispatch` time, held in memory, and injected over SSH into a tmpfs file — never
written to the kit, to disk, or onto any command line. See the
[injection data flow](../OVERVIEW.md#secret-injection-the-connect-data-flow).

## Declaring — `config.yml` `secrets`

```yaml
secrets:
  GITHUB_TOKEN:                        # the env var name
    description: private-repo git      # optional — human note
    command: ["gh", "auth", "token"]   # optional — host resolver argv
```

Each entry is **keyed by the environment variable name** (exported inside the sandbox); its
value configures how the secret resolves:

| Field | Required | Meaning |
|-------|----------|---------|
| `description` | no | Human-readable note; not used at runtime. |
| `command` | no | Host **argv array** (not a shell string). Its stdout — trailing newline trimmed — is the value. |

A secret with a `command` **resolves itself**. A secret with **no `command`** is a
*demand*: it must be supplied from your machine (below), or it stays unset.

During `at-cove work`, resolver commands additionally see the run's parameters
as `COVE_RUN_{REPO,ISSUE,CLASS,TIMEOUT}` in their environment — see
[at-cove-work-interface.md](../orchestration/at-cove-work-interface.md#three-separated-authorities)
for how this turns a resolver into a per-run credential minter.

## Supplying a value — `~/.config/at-cove/secrets.yml`

The user-owned supply file provides values for **demanded** names (entries for names the
kit doesn't demand are inert). It is consulted by `connect` **and by `work` + `dispatch`** —
any kit-demanded secret without a `command` is supplied from it. A string is a literal
value; an array is a resolver argv:

```yaml
# ~/.config/at-cove/secrets.yml
GITHUB_TOKEN: ghp_xxxxxxxxxxxxxxxxxxxx                     # string -> literal value
ANTHROPIC_API_KEY: ["pass", "show", "anthropic/api-key"]  # array  -> resolver argv
```

**Precedence**, per demanded secret:
1. a `config.yml` `command` — always wins;
2. else `secrets.yml` — a string is injected literally, an array is run as a resolver;
3. else **unresolved** — behavior depends on whether the secret is *required*:
   - a **general / agent** secret warns and is left unset (`connect`, and the agent-injected
     secrets of `work`);
   - a **required well-known** secret — `dispatch`'s tracker token, `work`'s code-host
     token — is a **fail-closed error** naming the secret and the `secrets.yml` path.

**Fail-closed:** any resolver `command` exiting non-zero aborts the run **before any
SSH** happens. A missing `secrets.yml` is fine (treated as empty); a malformed one aborts.

> Literal values sit in plaintext on disk — keep the file `chmod 600`. Resolver *commands*
> (from either source) produce values only in memory.

## Security caveats

- **Host-execution vector (current state).** A resolver `command` lives in the committed
  `config.yml`, so `connect`/`work`/`dispatch` run whatever it declares. **Only run at-cove
  against repos you trust** (your own). The planned `.local/` layer will move `command` out of the
  committed file so an untrusted repo can never trigger a resolver you didn't author.
- **Never inject `ANTHROPIC_API_KEY`.** The VM's managed settings enforce claude.ai
  subscription OAuth (`forceLoginMethod=claudeai`) and **block startup** if an API key is
  present. Authentication is handled separately (see
  [Authentication](../OVERVIEW.md#authentication)); a `GITHUB_TOKEN` secret is the common,
  supported case (it enables private-repo git over HTTPS).
