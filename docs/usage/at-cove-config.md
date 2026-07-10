---
summary: The at-cove kit config.yml schema — every field an operator sets to define a sandbox (name, secrets, worker classes, image customization), with validation rules and a full annotated example.
read_when: You are authoring or editing a kit's .at-cove/config.yml — adding a secret, a worker class, an allowed domain, or a PATH entry.
owns: the config.yml schema (all top-level fields + their validation)
prereqs: ../OVERVIEW.md — what at-cove is and the kit/build model; at-cove-secrets.md — secret declaration + resolution
tier: leaf
updated: 2026-07-09
---

# at-cove `config.yml`

`config.yml` is a kit's **spec** — identity and wiring only. It carries **no secret
values**, no hardening knobs, and no workspace mode (those are chosen at `create` time so
a committed spec stays portable). It lives at the kit root — by convention
`<repo>/.at-cove/config.yml` (see the [kit layout](../OVERVIEW.md#the-kit-at-cove)).

Parsing is **strict**: an unknown or misspelled field is a hard error (`config.yml: field
… not found`), so typos surface immediately rather than being silently ignored.

## Fields

A `*` marks a required field.

### name*
*string*

The base sandbox/VM name. Also keys the per-sandbox `known_hosts` and the state/workspace
volumes. Keep it stable — changing it points commands at a different instance.


### secrets
*map of secret env name -> config*
Environment variables the sandbox needs, **declared by name**; values are resolved at
`connect`/`dispatch` time and never stored in the kit. Full schema — declaration,
host-resolver `command`, the user-supplied `~/.config/at-cove/secrets.yml`, precedence,
and the fail-closed / host-execution rules — is in **[at-cove-secrets.md](at-cove-secrets.md)**.

#### secrets.*name*.description
*string*

Describes the content and use of the secret.

#### secrets.*name*.command
*list of command line tokens*

The command to execute to resolve the secret's value (its stdout).
If omitted, the value is supplied from the user's `~/.config/at-cove/secrets.yml`
(see [at-cove-secrets.md](at-cove-secrets.md)).

```yaml
secrets:
  GITHUB_TOKEN:
    description: private-repo git over HTTPS
    command: ["op", "read", "op://Personal/github-pat/token"]
```

### workers
*map of classname → config*

Defines the worker classes that `at-cove dispatch` can launch. 

#### workers.*class*.prompt
*string*

The prompt to send to the worker.

```yaml
workers:
  triage:
    prompt: Determine what needs to be done and write TODOs.
```

### image
**Additive** build-time customizations of the sandbox image. Every field layers **onto**
the hardened baseline and can never override it — cove translates each to the correct
sealed mechanism.

#### image.setup-scripts
*list of strings*
Kit-relative scripts run **as root at build**, in place (e.g. install a toolchain). Each must be non-empty.

#### image.paths
*list of strings*

Appended to `PATH` in `/etc/environment`. Each must be non-empty and single-line.

#### image.env
*map string → string*

`KEY=VALUE` written to `/etc/environment`. Keys must be non-empty and free of `=`/newline; values single-line.
**Cannot set base-owned keys** — `PATH`, `CLAUDE_CONFIG_DIR`, `http_proxy`/`https_proxy`
(and their upper-case / `no_proxy` variants) are owned by the sealed hardening layer;
setting them is a hard error (it would breach the additive guarantee or weaken the egress
gate). Use `paths:` to extend `PATH`.

#### image.allowed-domains
*list of strings*

Added to the squid egress allow-list. Each must be non-empty.

```yaml
image:
  setup-scripts:
    - .install-files/install-go.sh
  paths:
    - /usr/local/go/bin
  env:
    GOFLAGS: "-mod=mod"
  allowed-domains:
    - proxy.golang.org
    - sum.golang.org
```

## Full example

```yaml
name: claude-on-myrepo

secrets:
  GITHUB_TOKEN:
    description: private-repo git over HTTPS
    command: ["gh", "auth", "token"]

image:
  setup-scripts: [ .install-files/install-toolchain.sh ]
  paths:
    - /usr/local/go/bin
  allowed-domains:
    - proxy.golang.org

workers:
  triage:
    prompt: Determine what needs to be done and write TODOs.
```

## Validation summary

`config.yml` is rejected (with a `config.yml: …` error) if any of:
- an unknown field is present, or `name` is missing;
- an `image.setup-scripts[i]` / `image.paths[i]` / `image.allowed-domains[i]` is empty (or a
  path contains a newline);
- an `image.env` key is empty, contains `=`/newline, or is a **base-owned** key; or a value
  contains a newline.

Other fields (e.g. `secrets.*.command`, `workers.*.prompt`) are structurally validated only —
the decoder rejects wrong shapes/types.
