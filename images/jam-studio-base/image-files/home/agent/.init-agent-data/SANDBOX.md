If `/agent-data/context/CORE.md` exists, this is a Jam session:
its session context governs the sandbox rules — ignore the rest of this file.

# Sandbox operating instructions (no Jam session context)

You are in a Jam studio started without a session-context bundle; these are the
minimum rules. It is a hardened sandbox VM: isolated filesystem, allow-listed
network egress, and a sealed security baseline you cannot edit from inside.

**What persists:** only `/home/agent/workspace` (your project) and `/agent-data`
(your `CLAUDE_CONFIG_DIR` — settings, history, saved login). Everything else —
installed packages, files written elsewhere, env tweaks — is ephemeral and resets
when the studio is re-raised. A one-off `apt-get install` or `export` is fine for
this session; never rely on it surviving. If you need a change to *stick*, ask for
it to be made in the kit.

**Egress is locked down.** All traffic goes through an in-VM proxy
(`http(s)_proxy=http://127.0.0.1:3128`) that allows only an allow-listed set of
domains; `nftables` drops the rest. If a download, `git`, `pip`, `go get`, or API
call fails with a connection/proxy error, the host is almost certainly **not on the
allow-list** — not a transient fault. Don't retry blindly or hunt for a mirror; ask
for the domain to be added.

**Changing the sandbox is human-gated.** You cannot rebuild your own studio or widen
its access, and must never weaken the hardening. Ask a human (whoever started you,
or the project contact) for the exact change and why. An operator makes it in the
role's studio kit — a kit draft (`jam kit draft …`) published as the kit's next
version, which takes effect when the studio is re-raised — or in the role's egress
policy (`jam egress set`), which reaches a running studio shortly. The hardening
layer (proxy, nftables, sshd, entrypoint, credential helper, managed settings, and
the `CLAUDE_CONFIG_DIR` and proxy env) cannot be changed by any kit; if you think it must change, stop and
explain why to a human.
