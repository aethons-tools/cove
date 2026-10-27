---
summary: Root map of this repo's documentation — start here, route from here.
read_when: You need information from the docs. Always start here; open only the rows that match your task.
owns: routing for all docs
prereqs: none
tier: index
updated: 2026-10-27
---

# Documentation index

This is a **map, not a manual**. Find the row whose "Read when" matches your task,
open only that doc, and stop when you have enough.

| Doc | Read when |
|-----|-----------|
| [OVERVIEW.md](OVERVIEW.md) | You need what this repo builds (the studio image, its hardening and egress tiers), the package map, or how to build/test/run. |
| [DEVELOPMENT.md](DEVELOPMENT.md) | You are building or testing this repo inside the egress-locked dev sandbox and `go` or `just` won't fetch or build. |
| [web-app.md](web-app.md) | You are changing the Jam app (`web/app`, the user area at `/app/user/`), adding an `/app/user/api` route it reads, or building/testing it. |
| [usage/INDEX.md](usage/INDEX.md) | You are running one of the binaries (`at-jam`, `at-mint`, `at-studio-manager`) and need its flags, config, or I/O contract. |
| [superpowers/](superpowers/) | You need the design history — dated specs (`specs/`) and implementation plans (`plans/`) — behind a structural decision. |
| [TODO.md](TODO.md) | You want the loose backlog of known follow-ups not yet filed as tickets. |
