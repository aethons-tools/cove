---
summary: How to make a persistent change to the at-cove sandbox by editing the kit's config.yml.
read_when: A network request is blocked, or you need a tool / PATH entry / env var to survive a rebuild, and you are about to edit `.at-cove/config.yml`.
owns: kit change workflow, the `image:` block schema (allowed-domains, setup-scripts, paths, env), the at-cove recreate rebuild step
prereqs: SANDBOX.md
tier: leaf
updated: 2026-07-01
---

# Changing the sandbox — editing the kit

You cannot rebuild your own image, and you must not weaken the hardening. The
supported path is **declarative**: edit the kit's `config.yml`, then ask the human
to rebuild. Everything in the `image:` block is **additive** to the hardened
baseline — it can add, never override (for what it can't touch, see
`/agent-data/reference/sandbox-hardening-limits.md`).

## The workflow

1. **Find the kit** — usually `/home/agent/workspace/.at-cove/` (the `.at-cove/`
   directory at the project root). It lives in the persistent workspace, so your
   edits survive. If there is no `.at-cove/` in your workspace, don't guess —
   describe the change you need and let the human apply it on the host.
2. **Edit `.at-cove/config.yml`** under the additive `image:` block (schema below).
3. **Stop and tell the human to run `at-cove recreate` on the host.** That rebuilds
   the VM from the kit while keeping your volumes (the `/agent-data` login and the
   `/home/agent/workspace` contents). The change does not take effect until they do
   this — you cannot trigger it yourself.

Be explicit in your report: name the exact `config.yml` edit you made and why, so
the human can review it before rebuilding.

## The `image:` block — reference

```yaml
image:
  # Hostnames added to the egress allow-list. THIS is the fix for a blocked
  # network request. A leading dot matches the domain and all its subdomains;
  # a bare host matches exactly.
  allowed-domains:
    - .example.com        # example.com + any subdomain
    - downloads.foo.org   # exactly this host

  # Install tools / make system changes at build time. Each entry is a script
  # path relative to .at-cove/image-files/, run as root, in place. Put the
  # script under .at-cove/image-files/ (that tree is copied into the image root,
  # image-files/ -> /). Prefer this over a one-off install so the tool persists.
  setup-scripts:
    - .install-files/install.sh

  # Directories appended to PATH for every session.
  paths:
    - /usr/local/go/bin
    - /home/agent/go/bin

  # Extra environment variables (KEY: VALUE) for every session.
  env:
    GOROOT: /usr/local/go
    GOPATH: /home/agent/go
```

Common tools (`git`, `gh`, `curl`, `jq`) are already installed — check with
`command -v` before adding an install step.
