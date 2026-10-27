# Studio Manager, Plan 5: Jam in a Container Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run `at-jam serve` in a container next to Postgres and Garage, with no launcher. Studio Managers on the Mac dial its published TLS port. Ship the image from the release pipeline.

**Architecture:**
- **The image** is a small Alpine image holding the release's `at-jam` and `at-mint` (static, CGO-free). It also has `ca-certificates` and a non-root user.
  - The image is built from already-built binaries. It does not compile them, so its version stamp and embedded bundles are the release's own.
  - The audit found that a launcher-free `serve` runs no external programs. The exceptions are credential `command:` resolvers and `mint:` entries (`at-mint`), which is why Alpine's busybox (`cat` and so on) and `at-mint` are included.
  - It needs no Docker, SSH or git binary. Kit and config git fetches are in-process go-git.
- **Compose:** `dev/docker-compose.yml` gains a `jam` service, behind a compose profile so `just dev-up` is unchanged.
  - It publishes host `443` to the container's `8443` (the cove-facing TLS listener) and host `127.0.0.1:8081` to the admin port.
  - It reads a container config copied from a committed example. A hermetic Go test validates that example with serve's own parser.
- **Release:** the `atcove` job builds a multi-arch `ghcr.io/aethons-tools/jam:<N>-<MMDD>` image (and `:latest`) from goreleaser's linux binaries.
  - It pushes on main.
  - On pull requests it builds, smoke-runs, and does not push.

**Tech Stack:** Docker / buildx, Alpine 3.22, compose profiles, GitHub Actions (`docker/setup-buildx-action`, `docker/login-action`), bash with a fake-`docker` test harness in the `install.test.sh` style, and Go tests with `gopkg.in/yaml.v3`.

**Spec:** `docs/superpowers/specs/2026-10-26-studio-manager-design.md`, section "Jam in a container", Goal 5 and phasing step 5. That includes the docs it lists: `serve.md`, `coves.md` and the `OVERVIEW.md` architecture section, plus the manual live-test run under "Testing expectations".

Plans 1 to 4 (`docs/superpowers/plans/2026-10-26-studio-manager-{1,2,3}-*.md` and `2026-10-27-studio-manager-4-*.md`) built the router, the protocol, the manager binary and the per-manager kit operations.

**Later (not this plan):** the live test itself, run by the user with the runbook from Task 5. After it passes: delete `local`, `runtime.launcher` and the `at-jam colima` verbs.

## Global Constraints

- **Image name:** `ghcr.io/aethons-tools/jam`.
  - Tags: `<N>-<MMDD>` (the release version, identical to the binaries' `main.version`) and `latest`.
  - Platforms: `linux/amd64` and `linux/arm64`.
  - The local dev tag is `cove-jam:dev`.
- **Image contents:**
  - base `alpine:3.22`;
  - `apk add --no-cache ca-certificates`;
  - user `jam`, uid/gid `10001`, home `/var/lib/jam`;
  - `/usr/local/bin/at-jam` and `/usr/local/bin/at-mint`;
  - `ENV HOME=/var/lib/jam`, `VOLUME /var/lib/jam`, `EXPOSE 8443 8081`;
  - `ENTRYPOINT ["at-jam"]`, `CMD ["serve","--config","/etc/jam/jam.yml"]`.

  It never runs as root, and holds no docker, ssh or git binary.
- **Ports in the container:**
  - `listen: ":8443"` (non-root, so not 443);
  - `admin-listen: ":8081"`. That bind is not loopback, so serve requires `admin-tls`/`tls` plus `operator-auth.oidc` (`validateAdminExposure`).
  - Compose publishes `443:8443` and `127.0.0.1:8081:8081`.
- **Admin access from the host:** go through OIDC, the same as any non-loopback admin. A docker port-forward arrives from the bridge gateway, so loopback trust does not apply. A loopback-trusted CLI is `docker compose exec jam at-jam <verb>`, which reaches the default `http://127.0.0.1:8081` inside the container.

  Never add `dev-identity` or loopback trust to the container config.
- **The container config:**
  - `runtime.studio-managers: true`, and no `runtime.launcher`;
  - `store-postgres.host: postgres`, `port: 5432`;
  - `blobs.endpoint: http://garage:3900`;
  - `credentials-file: /etc/jam/credentials.yml`;
  - TLS files under `/etc/jam/tls/`.
- **Compose:**
  - The service `jam` has `profiles: ["jam"]` and `image: ${JAM_IMAGE:-cove-jam:dev}`.
  - It has `depends_on: postgres: {condition: service_healthy}` and `garage: {condition: service_started}`.
  - Mounts:
    - `./jam.container.yml:/etc/jam/jam.yml:ro`;
    - `./credentials.container.yml:/etc/jam/credentials.yml:ro`;
    - `${JAM_TLS_DIR:-./tls}:/etc/jam/tls:ro`;
    - the volume `jamstate:/var/lib/jam`.
- **Studio Managers** reach the container at the Mac's published `443`. Their `jam-addr` and `colima.runtime-addr` are `jam.local.aethons.tools:443` in dev. The existing `--add-host <jam-host>:host-gateway` mapping still carries studios there.
- **Versions:** Jam and every manager must run the same release `<N>-<MMDD>`. The image tag is that version.
- **Tests are hermetic.** No Docker in the sandbox, so the scripts are tested against a fake `docker` on `PATH`, and configs are tested with Go parsers. CI's PR run exercises the real build and smoke test.
- **Go commands** in this sandbox: prefix them with `GOPROXY=https://proxy.golang.org GOFLAGS=`.

## Review Focus

1. **The container config fails serve's own validation.** For example, a non-loopback admin bind without OIDC, a missing TLS file key, or a leftover `runtime.launcher`. The test must use the real parser and validators, not a YAML shape check. Task 1 adds `TestContainerConfigExampleValidates`.
2. **The image script finds no binary for an arch,** say an `arm64` dist missing after a partial build. It must fail with a message naming the arch and the paths it looked in. It must not build an image with an empty `bin/`. Task 2 adds the `jam-image.test.sh` case `missing arm64 binary fails`.
3. **A PR run pushes an image, or a main run skips the push.** The push is gated on `github.event_name == 'push'`. Task 4's grep check pins it, alongside the PR smoke run.
4. **`just dev-up` starts Jam unasked.** The profile keeps it opt-in. Task 3 adds `TestComposeJamServiceIsOptIn`.
5. **The admin port is exposed on every host interface.** It must be published on `127.0.0.1` only, with OIDC still required. Task 3 adds `TestComposeJamPorts`.

---

### Task 1: The container serve config, validated

**Files:**
- Create: `dev/jam.container.yml.example`
- Create: `dev/credentials.container.yml.example`
- Modify: `.gitignore` (add `dev/jam.container.yml` and `dev/credentials.container.yml`)
- Test: `cmd/at-jam/container_config_test.go`

**Interfaces:**
- Consumes: the existing `loadServeConfigs` / `parseServeConfig` and the validators in `cmd/at-jam/config.go`. Use whichever entry point `cmdServe` uses to load and validate, without opening listeners or the store.
- Produces: the committed example files that Tasks 3 and 5 reference.
- The config example:
  - It is the dev shape of `dev/jam.dev.yml.example`, translated into the container's values from Global Constraints.
  - It keeps the `operator-auth.oidc` block with placeholders, plus `ui-hosts: [jam.local.aethons.tools]`.
  - It has no `dev-identity` and no `runtime.launcher`.
  - Header comments say how to copy and fill it, and that it is dev-only.
- The credentials example has `jam-db` (`value: jam`), `blobs-key` and `blobs-secret` (the compose Garage dev keys). It notes that the `command:` resolvers run inside the container.

- [ ] **Step 1: Write the failing test.** `TestContainerConfigExampleValidates`:
  - It loads `../../dev/jam.container.yml.example` through serve's real load-and-validate path, with the credentials file pointed at the example's sibling.
  - It asserts no error, plus:
    - `Runtime.StudioManagers == true`;
    - `Runtime.Launcher == nil`;
    - listen is `":8443"`;
    - admin-listen is `":8081"`;
    - the store host is `postgres`, port `5432`;
    - the blobs endpoint is `http://garage:3900`;
    - `DevIdentity` is unset.
  - A second case loads the example with its `operator-auth` block removed. That must fail admin-exposure validation, which proves the test exercises the real validator.
- [ ] **Step 2: Run it and see it fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./cmd/at-jam/ -run TestContainerConfigExampleValidates`
Expected: FAIL (the file does not exist)

- [ ] **Step 3: Write the two example files and the `.gitignore` lines.** If validation needs the TLS files to exist at parse time, the test points the TLS paths at temporary files rather than weakening the example.
- [ ] **Step 4: Run the test**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./cmd/at-jam/ -run TestContainerConfigExampleValidates`
Expected: PASS

- [ ] **Step 5: Commit**: `feat(dev): a validated serve config for Jam in a container`

---

### Task 2: The Jam image and its build script

**Files:**
- Create: `images/jam/Dockerfile`
- Create: `scripts/jam-image.sh`
- Create: `scripts/jam-image.test.sh`
- Modify: `justfile` (the `jam-image` recipe)

**Interfaces:**
- Produces:
  - `scripts/jam-image.sh [--dist DIR] [--platforms LIST] [--tag T]... [--push]`:
    - `--dist` defaults to `dist`;
    - `--platforms` defaults to the host's `linux/<go env GOARCH>`;
    - `--tag` is repeatable and defaults to `cove-jam:dev`.
  - For each platform `linux/<arch>`, it finds `at-jam` and `at-mint` in `DIR/linux-<arch>/`, which is the `scripts/build.sh` layout. Failing that, it looks in `DIR/at-jam_linux_<arch>*/at-jam` and `DIR/at-mint_linux_<arch>*/at-mint`, which is the goreleaser layout.
  - It copies them into a temporary context as `bin/linux-<arch>/{at-jam,at-mint}`, together with `images/jam/Dockerfile`.
  - It then runs a single `docker buildx build --platform <list> -t <tag>... <ctx>`, with `--push` when pushing, else `--load`. `--load` with more than one platform is an error: `--load supports one platform; use --push`.
  - A missing binary exits 1 with `jam-image: no at-jam for linux/<arch> (looked in <paths>)` (or the same for at-mint).
  - `just jam-image` runs `scripts/build.sh linux/$(go env GOARCH)` and then `scripts/jam-image.sh`.
  - The Dockerfile is exactly the image contents in Global Constraints. It uses `ARG TARGETARCH` and `COPY bin/linux-${TARGETARCH}/ /usr/local/bin/`.

- [ ] **Step 1: Write the failing tests** in `scripts/jam-image.test.sh`. It puts a fake `docker` on `PATH` that records its argv and the context's file list. The cases:
  - **build layout, host default:** `dist/linux-amd64/{at-jam,at-mint}` with `--platforms linux/amd64` records `buildx build --platform linux/amd64 -t cove-jam:dev --load`. The context has `bin/linux-amd64/at-jam`, `bin/linux-amd64/at-mint` and `Dockerfile`.
  - **goreleaser layout, two platforms, push:** `dist/at-jam_linux_amd64_v1/at-jam`, `dist/at-jam_linux_arm64_v8.0/at-jam` and the matching `at-mint` dirs, with `--platforms linux/amd64,linux/arm64 --tag ghcr.io/aethons-tools/jam:1-0101 --tag ghcr.io/aethons-tools/jam:latest --push`. The argv has both tags and `--push`, and the context has both arch dirs.
  - **missing arm64 binary fails:** exit 1, stderr names `linux/arm64` and the looked-in paths, and the fake docker was not called.
  - **load with two platforms fails:** exit 1 with the `--load supports one platform` message.
  - **Dockerfile contract:** grep checks for `FROM alpine:3.22`, `ca-certificates`, `USER jam` (or `10001`), `ENTRYPOINT ["at-jam"]` and `CMD ["serve","--config","/etc/jam/jam.yml"]`, and that none of `docker`, `openssh` or `git` appears in an `apk add` line.
- [ ] **Step 2: Run them and see them fail**

Run: `bash scripts/jam-image.test.sh`
Expected: FAIL (the script does not exist)

- [ ] **Step 3: Write the Dockerfile, the script (`set -euo pipefail`; `mktemp -d` context, removed on exit) and the `just` recipe.**
- [ ] **Step 4: Run the tests**

Run: `bash scripts/jam-image.test.sh && bash -n scripts/jam-image.sh`
Expected: every case passes

- [ ] **Step 5: Commit**: `feat: the Jam container image (built from release binaries)`

---

### Task 3: The compose service

**Files:**
- Modify: `dev/docker-compose.yml` (the `jam` service, the `jamstate` volume, header comments)
- Modify: `justfile`:
  - `dev-jam-up`: `docker compose -f dev/docker-compose.yml --profile jam up -d`;
  - `dev-jam-logs`: `docker compose -f dev/docker-compose.yml logs -f jam`;
  - `dev-jam *ARGS`: `docker compose -f dev/docker-compose.yml exec jam at-jam {{ARGS}}`.
- Test: `cmd/at-jam/compose_test.go` (in the at-jam package, so `just test` runs it; it reads `../../dev/docker-compose.yml`)

**Interfaces:**
- Consumes: Task 1's example file names; Task 2's tag `cove-jam:dev`.
- Produces: the `jam` compose service, exactly as in Global Constraints.

- [ ] **Step 1: Write the failing tests** (yaml.v3 into a minimal struct):
  - `TestComposeJamServiceIsOptIn`: `services.jam.profiles == ["jam"]`, while `postgres` and `garage` have no profiles.
  - `TestComposeJamPorts`: the ports are exactly `["443:8443", "127.0.0.1:8081:8081"]`.
  - `TestComposeJamWiring`:
    - the image is `${JAM_IMAGE:-cove-jam:dev}`;
    - `depends_on.postgres.condition` is `service_healthy`;
    - the four mounts from Global Constraints are present;
    - the `jamstate` volume is declared.
- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./cmd/at-jam/ -run TestComposeJam`
Expected: FAIL

- [ ] **Step 3: Add the service, the volume and the recipes.** The compose header gains three lines:
  - `just jam-image`;
  - copy the two examples;
  - `just dev-jam-up`.
- [ ] **Step 4: Run the tests**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./cmd/at-jam/ -run TestComposeJam`
Expected: PASS

- [ ] **Step 5: Commit**: `feat(dev): Jam as an opt-in compose service beside Postgres and Garage`

---

### Task 4: Publish the image from the release pipeline

**Files:**
- Modify: `.github/workflows/release.yml`:
  - an env entry `JAM_IMAGE: ghcr.io/aethons-tools/jam`;
  - steps in the `atcove` job after "Check release archives".
- Modify: `.github/workflows/release.yml`'s header comment (the DAG line for the image)

**Interfaces:**
- Consumes: Task 2's `scripts/jam-image.sh` and its goreleaser-layout lookup.
- Produces, in the `atcove` job:
  1. `docker/setup-qemu-action@v3` and `docker/setup-buildx-action@v3`.
  2. **PR:** `if: github.event_name == 'pull_request'`.
     - It runs `bash scripts/jam-image.sh --platforms linux/amd64 --tag cove-jam:ci` (which loads).
     - Then a smoke run: `docker run --rm cove-jam:ci version` must print `at-jam <version>`, with the version from `needs.changes.outputs.version`.
  3. **main:** `if: github.event_name == 'push'`.
     - It logs in with `docker/login-action@v3` (registry `ghcr.io`, `${{ github.actor }}`, `secrets.GITHUB_TOKEN`).
     - It runs `bash scripts/jam-image.sh --platforms linux/amd64,linux/arm64 --tag $JAM_IMAGE:$VERSION --tag $JAM_IMAGE:latest --push`.
     - It appends `Image: $JAM_IMAGE:$VERSION` to `$GITHUB_STEP_SUMMARY`.

- [ ] **Step 1: Write a failing check** in `scripts/jam-image.test.sh`, the case `release.yml wiring`. It greps `.github/workflows/release.yml` for:
  - the `jam-image.sh` push step guarded by `github.event_name == 'push'`;
  - the PR step guarded by `pull_request` without `--push`;
  - the smoke `docker run --rm cove-jam:ci version`.
- [ ] **Step 2: Run it and see it fail**

Run: `bash scripts/jam-image.test.sh`
Expected: the `release.yml wiring` case fails

- [ ] **Step 3: Add the steps.** Parse-check the YAML with `python3 -c 'import yaml,sys; yaml.safe_load(open(".github/workflows/release.yml"))'`, or with the Go YAML package if Python's `yaml` is missing.
- [ ] **Step 4: Run the checks**

Run: `bash scripts/jam-image.test.sh`
Expected: every case passes. The real build is proven by this PR's own `atcove` job.

- [ ] **Step 5: Commit**: `ci: build, smoke-test and publish the Jam image`

---

### Task 5: Docs, including the live-test runbook

**Files:**
- Create: `docs/usage/jam/container.md`, a leaf. It covers:
  - the image and its tags;
  - what is and isn't inside, from the audit;
  - running it with compose (the steps);
  - ports and why they are 8443 and 8081;
  - admin access (OIDC from the host, `just dev-jam` for loopback CLI);
  - the volumes and what must persist (Postgres; `/var/lib/jam` for the relay state and pool store; the mounted config and credentials);
  - that `command:` resolvers run in the container;
  - pointing Studio Managers at the published port;
  - version lockstep.
- Create: `docs/usage/jam/studio-manager-live-test.md`, a leaf. It is the spec's manual live run as a checklist:
  - a containerized Jam plus a bare-metal manager on a Mac;
  - raise, teardown, pause, a kit build, and a draft build;
  - plus reconnect after `docker compose restart jam`, and a manager restart that leaves studios running.

  Each item gets its command and the expected result. The final item says that passing it gates deleting `local`.
- Modify:
  - `docs/usage/jam/serve.md`: the `runtime.launcher` block is marked transitional, configuring the in-process `local` manager only. Link to `container.md` and `studio-managers.md`.
  - `docs/usage/jam/coves.md`: one line linking the container doc where coves' path to Jam is described.
  - `docs/OVERVIEW.md` architecture section: Jam (container or anywhere), Studio Managers (bare-metal next to their substrate, dialing in), studios dialing Jam. It gets a row or bullet with links, not prose.
  - `docs/usage/studio-manager.md`: `jam-addr` for a containerized Jam is the host's published 443.
  - `dev/README.md`: a short "Jam in a container" section that links `container.md`, with no copied facts.
  - `docs/usage/INDEX.md` and `docs/usage/jam/INDEX.md`: rows for the two new leaves.

- [ ] **Step 1: Write the docs** with the docs-author skill. Each fact is in one doc, and the others link to it. Verify every command against the files from Tasks 1 to 4.
- [ ] **Step 2: Run the docs-audit checker.** Nothing new may appear outside `docs/superpowers/`.
- [ ] **Step 3: Commit**: `docs: Jam in a container; the Studio Manager live-test runbook`

---

## Coverage (spec to task)

| Spec item | Task |
|---|---|
| An image for `at-jam serve`, containing only what `serve` shells out to | 2 (audit facts in Architecture) |
| A compose service beside Postgres and Garage, publishing the cove-facing TLS port | 3 |
| Managers and studios on the Mac reach it at the host's published port; `--add-host` still works | 1, 3, 5 |
| Jam needs no docker, Colima, SSH keys, host-gateway or loopback to studios | 1 (no launcher), 2 (no such binaries) |
| Docs: `serve.md`, `coves.md`, `OVERVIEW.md`, and a new usage doc | 5 |
| The manual live run that gates deleting `local` | 5 (runbook) |
| A deploy compose beyond `dev/` (builder's call) | Not done: the dev compose and the published image cover it (ruling) |
