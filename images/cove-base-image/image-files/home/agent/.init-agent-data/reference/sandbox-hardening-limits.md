---
summary: What the hardening layer owns and refuses to let the kit override, and how to escalate if you truly need a change to it.
read_when: A kit edit was rejected at build, or you are about to try changing the proxy, nftables, sshd, the entrypoint, the credential helper, the managed agent settings, or a base-owned env var — check here first.
owns: the hardening security boundary, the list of base-owned settings the kit cannot override, the escalation path for hardening changes
prereqs: SANDBOX.md
tier: leaf
updated: 2026-10-05
---

# What you cannot change from inside (or via the kit)

The image is layered **kit base → harness → hardening**. The hardening layer is
applied last, is a security boundary, and always wins. The kit's `image:` block
and Dockerfile are additive only (see
`/agent-data/reference/sandbox-kit-changes.md`); they cannot alter any of the
following, which the hardening layer owns:

- **The egress proxy and `nftables` rules.** You can *add* allowed domains via
  `image.allowed-domains`, never disable the gate.
- **`sshd`, the entrypoint, and the git credential helper.** The entrypoint
  owns *how* `/agent-data` is seeded from the image, not *what* is seeded.
- **The managed agent settings** (`/etc/claude-code/managed-settings.json`).
- **The base-owned environment variables** `PATH`, `CLAUDE_CONFIG_DIR` (always
  `/agent-data`), and the proxy vars (`http_proxy` / `https_proxy` / `no_proxy`
  and their uppercase forms). `image.env` may not set these — it is rejected at
  build. Use `image.paths` to extend `PATH`.

Not hardening: the docs and skills seeded into `/agent-data` (`CLAUDE.md`,
`SANDBOX.md`, `reference/`, `skills/`). They come from the kit base, so a kit
may replace them.

## If you believe the hardening itself must change

Stop and explain why to the human. Changing the hardening is an out-of-band,
host-side decision — it is not something you can request through the kit.
