# Sandbox operating instructions

You are running inside an **at-cove** hardened sandbox VM.
It is deliberately locked down: isolated filesystem, allow-listed network egress,
and a sealed security baseline you cannot edit from inside.
Work within it, and when you need something the sandbox doesn't give you,
request it by editing the kit (see below) rather than improvising a workaround.

## What persists, what doesn't

- `/home/agent/workspace` is your project.
  It is either mapped to a host folder you collaborate on while chatting,
  or a volume you will need to clone the project into.
- `/agent-data` is your `CLAUDE_CONFIG_DIR` (settings, history, saved login).

**These are the ONLY persistent directory trees.**
Everything else — installed packages, files written elsewhere, environment tweaks —
is ephemeral and can reset at any time (notably on a rebuild).
So a one-off `apt-get install` or `export` is fine for the current session,
but never rely on it surviving.
If you need a tool or a system change to *stick*, change the kit (next section).

## Egress is locked down

All network traffic goes through an in-VM proxy (`http(s)_proxy=http://127.0.0.1:3128`)
that allows only an allow-listed set of domains (Anthropic, GitHub, PyPI, …);
`nftables` drops everything else.
If a download, `git`, `pip`, `go get`, or API call fails with a connection/proxy error,
the most likely cause is that its host is **not on the allow-list** — not a transient network fault.
Don't retry blindly or hunt for a mirror: add the domain to the kit and ask for a rebuild.

## Requesting a persistent change to the sandbox

You cannot rebuild your own image, and you must not weaken the hardening.
The supported path is **declarative**: edit the kit's `config.yml`, then ask the human to rebuild.

1. Find the kit — usually `/home/agent/workspace/.at-cove/`
   (the `.at-cove/` directory at the project root).
   It lives in the persistent workspace, so your edits to it survive.
   If there is no `.at-cove/` in your workspace, don't guess — describe the change
   you need and let the human apply it on the host.
2. Edit `.at-cove/config.yml` under the additive `image:` block (reference below).
3. **Stop and tell the human to run `at-cove recreate` on the host.**
   That rebuilds the VM from the kit while keeping your volumes
   (the `/agent-data` login and the `/home/agent/workspace` contents).
   The change does not take effect until they do this — you cannot trigger it yourself.

Be explicit in your report: name the exact `config.yml` edit you made and why,
so the human can review it before rebuilding.

## The `image:` block — reference

Every key here is **additive** to the hardened baseline; none can override it.

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
  setup-script:
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

Common tools (`git`, `gh`, `curl`, `jq`,) are already installed —
check with `command -v` before adding an install step.

## What you cannot change from inside (or via the kit)

The hardening layer is a security boundary and always wins:

- The egress proxy and `nftables` rules — you can *add* allowed domains, never disable the gate.
- `sshd`, the entrypoint, and the git credential helper.
- The base-owned environment variables `PATH`, `CLAUDE_CONFIG_DIR`,
  and the proxy vars (`http_proxy`/`https_proxy`/`no_proxy` and their uppercase forms).
  `image.env` may not set these — it is rejected at build.
  Use `image.paths` to extend `PATH`.

If you believe a change to the hardening itself is genuinely required,
stop and explain why to the human — it is an out-of-band, host-side decision.
