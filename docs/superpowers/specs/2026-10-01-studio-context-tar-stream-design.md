# jam: studio context as a streamed tar (no extraction)

**Status:** design approved (user chose tar+stream), pre-plan
**Scope:** replace the studio context **zip → extract-to-dir → `docker build <dir>`** path (COV-225/226/227) with a **tar → stream to `docker build -`** path. Docker reads a tar context on stdin and **preserves file modes natively**, which fixes the lost-exec-bit bug (`./script.sh` → exit 126) with no `chmod`, removes our own extract/zip-slip/mode code, and lets docker own context-extraction safety.
**Builds on:** COV-225 (`Base` forms), COV-226 (`context-dir` packer), COV-227 (`.dockerignore`), COV-228 (standard build args); the Runner already has `RunStdin`/`RunIO` (stdin piping, recorded by `runner.Fake`) — **no runner change**; `baseimage.{ParseLayers,DescendsFrom,Verify}` + `basedigest.BlessedRefs()` provide the reusable provenance gate.
**Does not change:** the provenance gate (ON, no `--allow-unverified`); the two-identity build-digest model; COV-208; the hardening/kit-image build (`BuildKitImage`/`buildFromBase`, dir-based, unchanged); the interactive `at-cove` install base path (`baseimage.Spec` with `KitDir`/`Base`/`DefaultRef`).
**Supersedes:** the proposed COV-229 "preserve modes in the zip extractor" — unnecessary, docker preserves modes. **Breaking** the just-shipped zip `context` format, **no shim** (unreleased, this session).

## The pivot

`docker build` doesn't accept a zip; it accepts a **tar** (gzip auto-detected) on stdin. So the studio `context` becomes a base64 **tar.gz**, and the base build **streams it to `docker build -q … -`** instead of extracting to a temp dir.

## studio package

- **`Base.Context`** stays a base64 string but now holds a **tar.gz** (was a zip). Doc + validation updated. `ContextDir`/`ContextFiles`/`Image` unchanged in role; `Kind()` unchanged.
- **`Base.ContextTar() ([]byte, error)`** — produces the context tar.gz bytes to stream, per form:
  - `BaseContextZip` (now tar.gz): `base64.StdEncoding.Decode(Context)` → the bytes.
  - `BaseContextFiles` (inline tree): build a **deterministic tar.gz in memory** from the tree — the reserved `dockerfile` → root `Dockerfile`, other entries by their single-segment paths, files mode `0644`, dirs implied. (No mode info in YAML, so inline scripts need `sh`/`chmod` in the Dockerfile — documented.)
  - `BaseImage`/`BaseDefault`: not a context — caller doesn't call `ContextTar`.
- **`PackContextDir(dir) (b64 string, err error)`** (client, COV-226) now emits a **deterministic tar.gz preserving file modes**: walk (EvalSymlinks root, COV-226), honor `.dockerignore` (moby matcher, COV-227), reject symlinks/irregular/abs/`..`, enforce caps; write entries via `tar.FileInfoHeader` (carries the unix mode — the exec bit survives). Determinism: sort entries by name, zero `ModTime`/`Uid`/`Gid`/`Uname`/`Gname`, keep `Mode().Perm()`; gzip with a zeroed header `ModTime`. base64 result ≤ `maxEncodedZip`.
- **Validation (register, in `Validate`)**: for `BaseContextZip`, cheap checks only — valid base64, the bytes are a readable gzip+tar, encoded ≤ `maxEncodedZip` (1 MiB). No decompression beyond reading the gzip/tar header stream lazily — actually a full header walk is fine and cheap; keep decompression bounded by the size cap during the scan.
- **`ScanContextTar(bytes) error`** (prepare-time, fail-closed) — walk tar headers (through gzip) enforcing: ≤ `maxZipEntries` (2000) entries; cumulative uncompressed ≤ `maxDecompressedZip` (64 MiB) via a counting reader (not the header's `Size`); reject `TypeSymlink`/`TypeLink`/non-regular; reject absolute or `..`-containing names; require a root `Dockerfile`. Caps stay tunable vars.
- **Remove** `materialize.go`'s extract-to-dir path entirely (zip reader, zip-slip, symlink-at-extract, `materializeTree`, `materializeZip`, `MaterializeInto`). Their safety role moves to `ScanContextTar` (pre-flight) + docker (actual extraction). Keep the caps constants, the Anthropic ceiling, `BuildDigest`, `ContextTree`/`ContextNode`, `Kind`, `Validate` (minus the extract helpers).
- Caps/consts keep their names (`maxEncodedZip`/`maxDecompressedZip`/`maxZipEntries`); rename to `…Archive…` optional — keep as-is to limit churn, update the doc comment to say "tar".

## backend / colima

- **`KitImageBuilder`**: replace `ResolveKitBaseDockerfile(contextDir string)` with **`ResolveKitBaseTar(ctx io.Reader) (resolvedBase string, err error)`**. Colima impl:
  1. Stream the build: `r.RunIO(ctx, &stdout, &stderr, "docker", dargs("build","-q","--build-arg","COVE_BASE_IMAGE="+base,"--build-arg","AT_JAM_STUDIO_BASE_IMAGE="+base,"--build-arg","AT_JAM_STUDIO_TARGET_ARCH="+runtime.GOARCH,"-")...)` where `base = basedigest.DefaultRef()`. `stdout` → the built image id.
  2. Tag it `cove-kit-base:<hex>` (as `dockerImg.Build` does) so it's `FROM`/inspect-able.
  3. **Gate** (ON, no escape hatch): `layers := dockerImg{c,base}.Layers(tag)`; `Verify(layers, blessedLayers)` where blessed layers come from `basedigest.BlessedRefs()` via `Layers`. Reject (loud error naming the base) if it doesn't descend; return the tag on pass.
  - Reuse `dockerImg.Layers` + `baseimage.{ParseLayers,Verify}`; this mirrors `baseimage.Resolve`'s gate for the DockerfileDir case, specialized to a streamed tar.
- **Remove** `BaseSpec.DockerfileDir` (studio-only, COV-223) and the `resolveBase` branch that consumed it; the interactive `KitDir`→`image/`→`DockerfileDir` path stays (it's `KitDir`-driven, not the removed field). `dockerImg.Build(dir)` stays for the interactive path.

## launcher

- **`resolveStudioBase`**: for `BaseContextFiles`/`BaseContextZip` → `tarBytes, err := def.Kit.Base.ContextTar()`; `if err…`; `studio.ScanContextTar(tarBytes)` (fail-closed); `return l.cfg.Ops.ResolveKitBaseTar(bytes.NewReader(tarBytes))`. For `BaseImage`/`BaseDefault` → `ResolveKitBase(def.Kit.Base.Image)`. **No per-ref base dir, no MaterializeInto.**
- `PrepareKit` otherwise unchanged (the kit-image/hardening build via `AssembleContext`+`BuildKitImage` still uses a dir).
- The launcher fake (`fakeOps`) replaces `ResolveKitBaseDockerfile` with `ResolveKitBaseTar(io.Reader)` (record the read bytes / that it was called; return a resolved base or an error for the gate-fail test).

## `at-jam kit push`

Unchanged in shape (parse → `ResolveContextDir` → `ToJSON` → send). `ResolveContextDir` now packs a tar.gz (via the updated `PackContextDir`). The `ToJSON` guard on an unresolved `context-dir` stays.

## Modes & the exit-126 fix

Because docker extracts the streamed tar and applies its unix modes, a `context`/`context-dir` script keeps its `+x` — the `./install-go.sh` 126 failure is fixed with no `chmod`. `context-files` (inline YAML, no modes) files are `0644`; inline scripts must be invoked `sh script.sh` or `chmod +x`'d in the Dockerfile — documented in `kits.md`.

## Security

- Encoded-size cap at register (registry-row protection); `ScanContextTar` caps (entries, cumulative uncompressed via counting reader) protect the Colima VM from a bomb before streaming.
- Reject symlinks/hardlinks/irregular + absolute/`..` names in the pre-scan (fail-closed, **stricter than docker** — conservative for an untrusted context).
- Deterministic archive (zeroed mtimes) — no host metadata leaks into the stored kit.
- Provenance gate unchanged: the built base must descend from a blessed `cove-base-image`; unblessed → loud prepare failure.
- No new dependency; `archive/tar` + `compress/gzip` are stdlib. moby/patternmatcher (COV-227) stays for `.dockerignore`.

## Testing (hermetic)

- **Deterministic pack**: `PackContextDir(dir)` twice → identical base64; `PackContextDir` then gunzip+untar yields the tree with **modes preserved** (a `0755` file stays `0755` — the exec-bit regression).
- **context-files → tar**: `ContextTar()` yields a readable tar.gz with `Dockerfile` at root + nested files.
- **ScanContextTar**: rejects >2000 entries, >64 MiB uncompressed (shrunk cap var), symlink/hardlink/irregular, absolute/`..` names, missing root Dockerfile; accepts a good tar.
- **`.dockerignore`** parity cases carry over (exclusion, root-anchoring, `!` negation incl. under a pruned dir, `Dockerfile`/`.dockerignore` kept) against the tar packer.
- **ResolveKitBaseTar** (colima, `runner.Fake`): the `docker build -q … -` argv carries the three build-args and `-`, the **tar is piped on stdin** (`Fake` records `Stdin`), the built id is tagged `cove-kit-base:*`, the gate runs (fake layers → `Verify`), and an unblessed base errors with no tag returned.
- **launcher**: a `context`/`context-files` kit routes through `ScanContextTar` + `ResolveKitBaseTar(<tar>)`; an `image` kit routes through `ResolveKitBase`; a gate failure fails the prepare closed (no kit image built).
- **BuildDigest** stable (context-files hashes the tree; context hashes the stored base64) — unchanged.

Real-substrate behind `integration`; the live loop's `dev/verify-dockerfile-kit.sh` + a `context-dir` kit with a `0755` script confirm the mode fix end-to-end.

## Additive / reversible

`at-cove`, the hardening build, and the interactive base path are untouched. The change is contained to `internal/studio` (tar producer + packer + scan, minus the extractor), `internal/backend` (`ResolveKitBaseTar`), and `internal/jam/launcher` (`resolveStudioBase` + fake). One intentional break: the zip `context` format → tar.gz, no shim.
