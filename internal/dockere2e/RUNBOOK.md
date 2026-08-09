# docker-in-sandbox e2e (COV-122)

`TestDockerInSandboxE2E` drives the **real** at-cove pipeline to install and boot a
`docker: true` kit under Sysbox on a live colima VM, and asserts the booted sandbox —
the runtime behavior the hermetic unit tests (COV-116/117/118/121/125) can only assert
as text. It is behind the `integration` build tag and gated by `COVE_DOCKER_E2E`, so it
never runs in `just test`.

## Prerequisites

- **colima running with Sysbox** installed and the `sysbox-runc` runtime registered —
  see [`docs/usage/docker-in-sandbox.md`](../../docs/usage/docker-in-sandbox.md) and the
  design's §H. Confirm: `colima ssh -- docker info -f '{{json .Runtimes}}' | jq 'has("sysbox-runc")'` → `true`.
- **`at-cove` on `PATH`** (`just install`), built from this tree so it embeds the
  hardening layer under test.
- The active **docker context points at that colima VM** (the default after `colima start`).

## Run

```console
$ just integration-docker
```

(equivalently: `COVE_DOCKER_E2E=1 go test -tags integration ./internal/dockere2e/ -run TestDockerInSandboxE2E -v -timeout 20m`)

## What it asserts (design §I checklist)

On the booted `atcove-dockere2e` sandbox: systemd is PID 1; the egress lock
(`cove-egress`) is ordered before `docker.service` and active; squid is up with a live
`/run/squid.pid` and `squid -k reconfigure` succeeds (COV-125); the nftables output
skuid lock **and** the COV-121 forward-drop survived docker's own nftables setup; inner
`docker run` / `docker build` / `docker compose up` succeed through squid; an
**un-proxied** nested-container egress is dropped; and the persistent `-docker` cache
volume (`/var/lib/docker`) survives `at-cove recreate`.

The test installs, creates, and — via `t.Cleanup` — destroys the sandbox and its
volumes. The fixture kit is `internal/dockere2e/testdata/dockerkit`; it allow-lists
Docker Hub (incl. its blob CDN) so the inner pulls succeed through squid.
