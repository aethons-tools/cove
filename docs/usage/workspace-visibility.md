---
summary: How to browse, diff, and edit an isolated sandbox's workspace from your own machine with VS Code Remote-SSH and git-over-SSH, via `at-cove view` and `at-cove ssh-proxy`.
read_when: You want to see into or edit a collaborator sandbox's workspace from your editor — connecting VS Code Remote-SSH, adding a git remote for it, or wondering why the alias still works after a `recreate`.
owns: the workspace-visibility usage story — `at-cove view`/`ssh-proxy`, the generated Host block and git remote, the client-side `localServerDownload` setting, and why the alias survives `recreate`
prereqs: ../OVERVIEW.md for the sandbox + egress model; the [`chat`](../OVERVIEW.md#the-chat-command-and-collaborator-sessions) collaborator model for what a sandbox workspace is
tier: leaf
updated: 2026-09-01
---

# Workspace visibility (VS Code Remote-SSH + git)

An isolated collaborator sandbox's workspace lives on a private Docker volume —
invisible from the host filesystem. `at-cove view` emits an SSH config that
connects your editor and git to it over the sandbox's *existing* SSH endpoint. No
new service runs in the sandbox, and no egress domain is added — the connection
still terminates at the same in-VM `sshd` that `chat` uses.

## One-time client setup

1. Install the **Remote - SSH** extension in VS Code.
2. Set, in VS Code settings, `"remote.SSH.localServerDownload": "always"`. This
   makes VS Code download its server on *your* machine and push it into the
   sandbox over SFTP, so the sandbox needs **no internet access** for Remote-SSH
   to work — the egress wall is untouched.

## Connect

```
at-cove view <collaborator>            # print the config + git remote to stdout
at-cove view --write <collaborator>    # upsert the Host block into ~/.ssh/config instead
```

Either form resolves the collaborator's instance (same rules as
`chat`/`status`) and prints a `git remote add sandbox …` line for
`/home/agent/workspace`. `--write` additionally upserts a managed
`Host cove-<container>` block into `~/.ssh/config` (creating `~/.ssh` at `0700`
and the file at `0600` if absent) and confirms the path instead of printing the
block.

Then in VS Code: **Remote-SSH: Connect to Host…** → `cove-<container>`. Open
`/home/agent/workspace`. Browse, read, watch the built-in Source Control diff,
and edit in place when you need to (read-only is a discipline here, not
enforced).

## Why the alias keeps working after `recreate`

The sandbox's SSH port is ephemeral and its host key regenerates each boot. The
generated Host block hardcodes neither: its `ProxyCommand` invokes `at-cove
ssh-proxy <collaborator>`, which resolves the instance and dials the backend for
the *current* port at connect time, then relays stdin/stdout to it — so
`HostName` in the block is just the alias itself, not a real host. Host-key trust
uses the same per-sandbox TOFU pinning `chat` does — see [Verify host key
(TOFU)](../OVERVIEW.md#secret-injection-the-chat-data-flow) for that mechanism.
What's specific to this doc: `at-cove destroy`/`recreate` reap the per-sandbox
`known_hosts.d/<container>` pin file when they tear the container down, so the
next boot's regenerated key re-pins cleanly on reconnect instead of tripping a
mismatch. Re-run `at-cove view --write` only if you add a collaborator or move
the kit; a plain `recreate` needs no re-run.

## Break-glass shell

`at-cove chat --raw <collaborator>` opens a plain shell in the same sandbox, over
the same SSH endpoint, when you need more than the editor gives you.
