# Declarative `image:` Block for Kit Config — Design

**Goal:**
Give kits a single declarative `image:` section in `config.yml`
that expresses build-time setup, extra `PATH`/env, and extra egress domains —
with cove owning the translation to the correct underlying mechanism,
so kit authors never reason about `/etc/environment` vs bashrc, the Dockerfile, or `squid.conf`.

**Status:**
design approved (brainstorming complete);
ready for implementation planning.

## Motivation

Today a kit customises the image by dropping raw files into `image-files/`
and relying on conventions baked into the sealed hardening layer:

- A setup script must be named `.install-files/install.sh`
  because the Dockerfile hardcodes `RUN /.install-files/install.sh`.
- Environment must be hand-written,
  and the obvious choice (`~/.bashrc` / `/etc/bash.bashrc`) is **wrong**:
  those are sourced only for *interactive* shells,
  while cove launches programs over SSH *non-interactively*.
  The session-wide mechanism is `/etc/environment` (pam_env),
  which the kit author has no reason to know.
  This footgun is what surfaced the whole effort (Go's `GOROOT`/`PATH` invisible to the agent).
- Adding an egress domain means knowing `squid.conf` uses an ACL file (`allowed_domains.txt`).

Each concern needs knowledge of a different sealed mechanism.
The `image:` block collapses that knowledge into cove, declared once, correctly.

## Design Principles

1. **Strictly additive.**
   Kit declarations can only *extend* the hardened baseline, never remove or override it.
   `allowed-domains` only widens egress on top of the mandatory base (`.anthropic.com`, `.github.com`, …);
   `paths` only appends to the base `PATH`;
   setup scripts run *after* base hardening.
   A kit can never shrink the allow-list or drop a base path.
2. **Sealed hardening stays sealed.**
   The embedded hardening Dockerfile and base image-files remain a single, static, auditable artifact.
   The kit supplies *data*;
   it never edits the Dockerfile.
   cove renders the `image:` block into build-time data files that fixed Dockerfile steps consume.
3. **One declarative surface.**
   `config.yml` is the interface;
   the raw `image-files/` overlay remains available as an escape hatch for anything the block does not cover.

## Config Schema

`kit.Config` gains an optional `Image` field:

```yaml
image:
  setup-script:                    # ordered list; paths relative to the kit dir
    - .install-files/install.sh
  paths:                           # appended to PATH in /etc/environment
    - /usr/local/go/bin
  env:                             # arbitrary KEY=VALUE written to /etc/environment
    GOROOT: /usr/local/go
  allowed-domains:                 # added to the squid allow-list
    - .example.com
```

Go struct (names indicative; finalise in the plan):

```go
type ImageConfig struct {
    SetupScript    []string          `yaml:"setup-script"`
    Paths          []string          `yaml:"paths"`
    Env            map[string]string `yaml:"env"`
    AllowedDomains []string          `yaml:"allowed-domains"`
}

type Config struct {
    // … existing fields …
    Image ImageConfig `yaml:"image"`
}
```

An empty or absent `image:` block is a no-op for all four keys.

## Architecture (sealed Dockerfile, kit supplies data)

The build is assembled by `internal/assemble`.
Layering today:
kit `image-files/` (layer 2) then embedded `hardening/` (layer 4, copied last, **wins**).
The `image:` block is rendered during assembly into build-time data the sealed Dockerfile consumes through fixed extension points.

cove writes its generated consumables into a reserved namespace in the build context (e.g. `image-files/.cove/…`)
that the kit may not use (a kit file under `.cove/` is an error).
The base allow-list addition is the one generated file written to a real runtime path (`etc/squid/allowed_domains.kit.txt`)
because `squid.conf` references it directly.

### setup-script

- Each entry is a path **relative to the kit dir**,
  resolved against the kit's `image-files/` overlay
  (the file is already copied into the image by `COPY image-files/. /.`).
  Example: `.install-files/install.sh` → in-image `/.install-files/install.sh`.
- cove writes an **ordered manifest** of the in-image absolute paths into the reserved namespace.
- A static Dockerfile step runs each script **in place, as root, after base hardening**:
  `cd "$(dirname "$s")" && bash "$s"`.
  Running in place lets a script reference sibling files (other things under its directory).
  Build-time network is open (matches today), so scripts may `curl`/`apt-get`.
- This **replaces** the hardcoded `RUN /.install-files/install.sh`.
  The `.install-files/install.sh` magic filename is no longer special;
  a kit must list whatever it wants run.
- A script with a non-zero exit fails the docker build (surfaced normally).

### paths + env

- cove writes the `paths` list and `env` map into the reserved namespace.
- A static Dockerfile step folds them into `/etc/environment`,
  **after** the base step that writes the base `PATH`/`CLAUDE_CONFIG_DIR`/proxy lines:
  - each `env` entry is appended as its own `KEY=VALUE` line;
  - each `paths` entry is **merged into the single existing `PATH=` line**
    (appended, so a kit cannot shadow system binaries).
    It must remain one `PATH=` line —
    pam_env is last-wins, so a second `PATH=` line would clobber the base.
    The merge reads the current line and rewrites it;
    cove supplies only the delta, so the base `PATH` stays owned by the Dockerfile.
- Because the values land in `/etc/environment`,
  pam_env exposes them to **every** SSH session —
  interactive or not, login or not,
  inherited by `claude` and all its non-interactive child shells.
  This is the fix for the original bug.

### allowed-domains

- cove always writes `etc/squid/allowed_domains.kit.txt` (empty when no domains),
  one host per line, leading-dot = match subdomains (same convention as the base file).
- The sealed `squid.conf` ACL references **both** files so the base list is never touched:
  ```
  acl allowed_domains dstdomain "/etc/squid/allowed_domains.txt" "/etc/squid/allowed_domains.kit.txt"
  ```
- This is the multi-file `dstdomain` form
  (chosen over a `conf.d/*.conf` include for simplicity: bare host lists, no per-snippet squid syntax).
  The file is always present so squid never errors on a missing ACL file.

## Collision Check (separable; the "no surprises" guard)

Independent of the `image:` block and shippable on its own:

During assembly, before the winning hardening copy,
compare every path in the kit's `image-files/` against the hardening tree.
Any path present in **both** is a **build-time error** that enumerates the colliding paths,
instead of silently overwriting the kit's file.
This removes the "my file vanished" surprise inherent in the layer-4-wins model.

## Additive Guarantee (how it holds)

Nothing cove generates overwrites a sealed file.
Kit content lives in *separate* files that the base *references* (`allowed_domains.kit.txt`)
or *appends to* (`/etc/environment`),
and scripts run *after* base hardening.
The base allow-list, base `PATH`, and base hardening are always present regardless of kit config.

## Error Handling

- `setup-script` entry missing or not a regular file → assembly error naming the kit-relative path.
- `setup-script` non-zero exit at build → docker build fails.
- Kit file colliding with the hardening tree → enumerated assembly error.
- Kit file under the reserved `.cove/` namespace → assembly error.

## Testing

- **Config parse:**
  the new `image:` schema (each key present/absent, ordering of `setup-script`).
- **Assemble (Fake FS):**
  assert the generated setup manifest (order + in-image paths),
  the env consumables,
  `allowed_domains.kit.txt` contents,
  and — via the Dockerfile step's logic —
  that `PATH` remains a single merged line with base segments preserved and kit segments appended.
- **Collision detection:**
  a kit file shadowing a hardening path → error;
  no false positive when paths are disjoint.
- **Lint:**
  `hadolint` on the (still static) Dockerfile.

## Migration (one breaking change)

Removing the hardcoded `RUN /.install-files/install.sh`:

- The current kit must add `image.setup-script: [.install-files/install.sh]` (or it stops running).
- `install.sh` should drop its `>> /etc/bash.bashrc` writes for Go env;
  replace with `image.paths: [/usr/local/go/bin]`
  and, if desired, `image.env: { GOROOT: /usr/local/go, GOPATH: /home/agent/go }`.
  (Go auto-detects `GOROOT` from the binary and defaults `GOPATH`,
  so `paths` alone already makes `go` work;
  `env` is for explicitness or non-path vars.)
- Good moment to split the current double-shebang `install.sh`
  (Go install + test-tooling concatenated) into clean per-tool scripts,
  each listed under `setup-script`.

## Out of Scope / Deferred

- Per-script `run-as: agent`
  (root only for now; a script can `su - agent -c` for agent-context steps).
- Auto-cleanup of setup-script artifacts from the final image
  (kit may self-clean; not auto-removed).
- `conf.d`-style includes for squid (the multi-file ACL form suffices).
- Layer 3 (`.local/image-files`) interaction — unchanged by this design.
