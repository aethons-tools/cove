---
summary: What the hardening layer owns and refuses to let the kit override, and how to escalate if you truly need a change to it.
read_when: A kit edit was rejected at build, or you are about to try changing the proxy, nftables, sshd, the entrypoint, the credential helper, or a base-owned env var — check here first.
owns: the hardening security boundary, the list of base-owned settings the kit cannot override, the escalation path for hardening changes
prereqs: SANDBOX.md
tier: leaf
updated: 2026-07-01
---

# What you cannot change from inside (or via the kit)

The hardening layer is a security boundary and always wins. The kit's `image:`
block is additive only (see `/agent-data/reference/sandbox-kit-changes.md`); it
cannot alter any of the following:

- **The egress proxy and `nftables` rules.** You can *add* allowed domains via
  `image.allowed-domains`, never disable the gate.
- **`sshd`, the entrypoint, and the git credential helper.**
- **The base-owned environment variables** `PATH`, `CLAUDE_CONFIG_DIR`, and the
  proxy vars (`http_proxy` / `https_proxy` / `no_proxy` and their uppercase forms).
  `image.env` may not set these — it is rejected at build. Use `image.paths` to
  extend `PATH`.

## If you believe the hardening itself must change

Stop and explain why to the human. Changing the hardening is an out-of-band,
host-side decision — it is not something you can request through the kit.
