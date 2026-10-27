---
summary: The manual live-test checklist for remote Studio Managers — a containerized Jam plus a bare-metal `at-studio-manager` on a Mac, covering raise, teardown, pause, a kit build, a draft build, a Jam restart and a manager restart. Passing it gates deleting `local`.
read_when: You are running the Studio Manager live test (a Jam container plus a manager on a Mac), checking whether `local`, `runtime.launcher` and the `at-jam colima` verbs may be deleted, or reproducing a manager problem end to end.
owns: the Studio Manager live-test procedure, its expected results, and the gate it sets for deleting the in-process launcher
prereqs: container.md (running Jam in a container); ../studio-manager.md (the manager's config); studio-managers.md (Jam's side)
tier: leaf
updated: 2026-10-27
---

# Studio Manager live test

This is the one manual run that proves remote Studio Managers work without the in-process launcher.
It uses a Jam in its container and a bare-metal `at-studio-manager` on a Mac with Colima.
Do the items in order. Each one gives the command and the result to expect.
**Passing every item gates deleting `local`, `runtime.launcher` and the `at-jam colima` verbs.**

## Setup

- A Mac with Colima running, and the repo checked out.
- A trusted cert for `jam.local.aethons.tools` in the secrets directory's `tls/`, and the OIDC tenant from the container config ([container.md](container.md#running-it-with-the-dev-compose)).
- Jam and the manager on one version ([lockstep](container.md#version-lockstep)): `just build && just jam-image` from one checkout, or a release image with that release's `at-studio-manager`.
- The `anthropic` and `git` destinations need credentials in the container: uncomment the `anthropic-key` and `git-pat` placeholders in `dev/jam.container.yml` and the secrets directory's `credentials.yml`, fill in real values, and restart Jam (`just dev-jam-up`).
- After step 2, a project and a role that grant the `anthropic` and `git` destinations ([roster.md](roster.md), [coves.md](coves.md#raising-a-real-managed-studio)), and a kit for the role ([kits.md](kits.md)).
- `at-jam` on the Mac, signed in: `at-jam login --admin-url https://jam.local.aethons.tools:8081` ([admin access](container.md#admin-access)).

The examples use the manager name `mac-1`, the role `acme/guest` and the kit `app`.

## The checklist

1. **Jam comes up in its container.**
   `just dev-jam-up`, then `just dev-jam-logs`.
   Expect: serve starts and logs `Jam managers`, with no `Jam launcher: colima` line. `docker compose -f dev/docker-compose.yml ps` shows `jam` running.
   `just dev-jam manager list` prints nothing and exits 0.

2. **The host CLI reaches the admin API over OIDC.**
   `at-jam whoami`, then `at-jam manager list`.
   Expect: your operator identity, and the same empty list.
   The published `127.0.0.1:8081` is not treated as loopback, so a request with no token is refused.

3. **Register the manager.**
   `at-jam manager add mac-1`.
   Expect: `manager mac-1 token (shown once): <token>`. Save the token in the manager's `token-file`.

4. **Run the manager.**
   Write `~/.config/at-studio-manager/config.yml` with `name: mac-1`, `jam-addr` and `colima.runtime-addr` set to `jam.local.aethons.tools:443`, and `colima.jam-host` set to `jam.local.aethons.tools` ([pointing managers at it](container.md#pointing-studio-managers-at-it)). Then run `at-studio-manager run`.
   Expect: it connects with no refusal. `at-jam manager list` shows `mac-1	connected=true	version=<Jam's>	studios=0	…	strays=0`.

5. **Raise a studio on the manager.**
   `at-jam studio raise --id live-1 --role guest --project acme --manager mac-1 --prompt-file task.md`.
   Expect: `studio list` shows `live-1` going `raising` → `live` with `manager=mac-1`.
   On the Mac, `docker ps --filter label=harbor.cove.manager=mac-1` shows `atcove.mac-1.cove-<id>`.
   The agent reaches Anthropic and git through Jam (activity `running`, then `waiting`).

6. **A kit build on the manager.**
   Change a build-affecting field of the role's kit and `at-jam kit push <dir>`, then raise `live-2` as in item 5.
   Expect: the manager builds the new image, and the raise waits for it, then goes `live`.
   `at-jam role images acme/guest` shows a `mac-1` row with the new tag.

7. **Pause and unpause.** There is no pause verb: Jam pauses through the idle ladder ([wake-on](intercom.md#waiting-for-a-reply-wake-on)).
   Leave `live-1` `waiting` past `runtime.wake.warm-timeout` (default 60s).
   Expect: `studio list` shows phase `idled`, and `docker ps` shows the container `(Paused)`.
   Then send the session a message in the Inbox (`/app/user`).
   Expect: Jam unpauses it, it reconnects, and it is woken: phase `live`, activity `running`.

8. **A draft build on the manager.**
   `at-jam kit draft open app`, edit a file with `kit draft put`, then `at-jam kit draft build app --manager mac-1` ([drafts](kits-drafts.md#test-build)).
   Expect: the build log streams, ending in `built draft app @<tree> (digest sha256:…)`. `kit draft build app --show` names `mac-1`.
   Optionally `studio raise … --kit-draft app --manager mac-1` reuses that image.

9. **Jam restarts and the manager reconnects.**
   `docker compose -f dev/docker-compose.yml restart jam`.
   Expect: the studios on the Mac keep running throughout. While the manager is reconnecting (its backoff is at most 30s), Probe answers Unknown, so nothing is reaped ([disconnect](studio-managers.md#disconnect)).
   Afterwards, `manager list` shows `mac-1 connected=true` with the same `studios=` count and `strays=0`, and `studio list` shows the same studios still `live`.

10. **The manager restarts and leaves its studios running.**
    Stop `at-studio-manager run` (Ctrl-C).
    Expect: `docker ps` still shows its studios, and `manager list` shows `connected=false` within 30s.
    Run it again.
    Expect: `connected=true`, the same `studios=` count, `strays=0`, and no studio marked lost ([reconnect](studio-managers.md#reconnect)).

11. **Teardown.**
    `at-jam studio teardown --id live-1`, and the same for `live-2`.
    Expect: each goes `terminating` → `gone`. Jam logs the `cove agent log (tail, captured on teardown)` line, and the containers are gone from `docker ps -a`.

12. **Final state.**
    `at-jam manager list`.
    Expect: `mac-1	connected=true	…	studios=0	…	strays=0`.

13. **The gate.** All of items 1 to 12 passed against the containerized Jam with no `runtime.launcher`.
    That is what clears deleting `local`, `runtime.launcher` and the `at-jam colima` verbs in the follow-up.
    Record the version tested and anything that needed a workaround on the follow-up ticket.

Clean up with `just dev-down` (or `just dev-down -v` to wipe the volumes) and `at-jam manager revoke mac-1`.
