# Sandbox operating instructions

You are running inside an **at-cove** hardened sandbox VM: isolated filesystem,
allow-listed network egress, and a sealed security baseline you cannot edit from
inside. Work within it; when you need something it doesn't give you, request it by
editing the kit rather than improvising a workaround.

**What persists:** only `/home/agent/workspace` (your project) and `/agent-data`
(your `CLAUDE_CONFIG_DIR` — settings, history, saved login). Everything else —
installed packages, files written elsewhere, env tweaks — is ephemeral and resets
on rebuild. A one-off `apt-get install` or `export` is fine for this session;
never rely on it surviving. If you need a change to *stick*, change the kit.

**Egress is locked down.** All traffic goes through an in-VM proxy
(`http(s)_proxy=http://127.0.0.1:3128`) that allows only an allow-listed set of
domains; `nftables` drops the rest. If a download, `git`, `pip`, `go get`, or API
call fails with a connection/proxy error, the host is almost certainly **not on the
allow-list** — not a transient fault. Don't retry blindly or hunt for a mirror; add
the domain to the kit.

**Changing the sandbox is declarative and human-gated.** You cannot rebuild your own
image and must never weaken the hardening. The path is always: edit
`.at-cove/config.yml`, then ask the human to run `at-cove recreate` on the host —
the change does not take effect until they do. Name the exact edit and why so they
can review it.

For the detail, load only the leaf your task needs:

- **Editing the kit** — adding an allowed domain, installing a tool, extending
  PATH/env, the `image:` block schema, the rebuild steps →
  `/agent-data/reference/sandbox-kit-changes.md`
- **Hitting a wall you can't get past from inside** — what the hardening layer owns
  and refuses to let the kit override →
  `/agent-data/reference/sandbox-hardening-limits.md`
