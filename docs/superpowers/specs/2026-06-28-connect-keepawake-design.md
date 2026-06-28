# Keep the host Mac awake while `connect` is active

## Goal

While an `at-cove connect` session is running,
the host Mac must not idle-sleep,
so the Colima VM keeps running and the agent's work does not stall.
macOS is the only host implementation needed for now;
other platforms compile and run unchanged (no-op).

## Scope

- Prevent **idle system sleep only** (`caffeinate -i`).
  The display may still sleep; battery/lid-close assertions are out of scope.
- The assertion covers **only the interactive session** (`Transport.Launch`).
  It is *not* held during secret resolution, status check, dial,
  or the interactive `claude auth login` step —
  no agent work happens until the session itself starts.
- Sleep prevention is **best-effort**:
  if the assertion cannot be established,
  `connect` warns on stderr and proceeds.
  It never blocks or fails a connect.

Out of scope (YAGNI):
reason-string plumbing,
a config/flag toggle,
display or battery/AC options.

## Design

### New package `internal/awake`

A small, platform-isolated sleep inhibitor.

```go
// Inhibitor asks the host OS to stay awake until release() is called.
type Inhibitor interface {
    Inhibit() (release func(), err error)
}

func New() Inhibitor // build-tagged constructor
```

- `inhibitor_darwin.go` (`//go:build darwin`):
  `Inhibit()` starts `caffeinate -i -w <cove-pid>` via `exec.Command(...).Start()`
  and returns a `release` that kills and reaps the process,
  guarded by `sync.Once` so a double release is safe.
  - `-i` asserts against idle system sleep.
  - `-w <pid>` ties caffeinate's lifetime to cove's own pid
    as a crash safety-net (caffeinate self-exits if cove dies);
    `release` still kills explicitly for prompt teardown on clean exit.
- `inhibitor_other.go` (`//go:build !darwin`):
  `New()` returns a `noop{}` whose `Inhibit()` returns a do-nothing
  release and a `nil` error.

### Wiring into `connect.Connect`

The inhibitor is a collaborator, passed alongside `b, r, t`:

```go
func Connect(b backend.Backend, r runner.Runner, t Transport, aw awake.Inhibitor, o Options) error
```

It is acquired immediately before `t.Launch`,
after `ensureAuthenticated` has returned:

```go
if !o.SkipAuth {
    if err := ensureAuthenticated(r, tgt); err != nil {
        return err
    }
}
if release, err := aw.Inhibit(); err != nil {
    fmt.Fprintf(stderr, "at-cove: warning: could not prevent host sleep: %v\n", err)
} else {
    defer release()
}
return t.Launch(tgt, env)
```

The deferred `release` runs when `Connect` returns —
on normal session exit, error, or Ctrl-C propagating through the blocking ssh.

The warning needs a writer,
so `Options` gains `Stderr io.Writer`,
defaulting to `os.Stderr` when nil.

### `main.go`

`doConnect` constructs `awake.New()`
and threads it into the `connect.Connect(...)` call,
passing its existing `stderr` as `Options.Stderr`.

## Data flow

```
doConnect
  └─ connect.Connect(b, r, t, awake.New(), Options{Stderr: stderr, ...})
       resolve secrets → check running → dial → known_hosts
       ensureAuthenticated (NOT covered by assertion)
       aw.Inhibit() ──► caffeinate -i -w <pid>   (darwin) / no-op (other)
       t.Launch(...)        ◄── machine stays awake here
       defer release() ──► kill+reap caffeinate
```

## Error handling

- `Inhibit()` error → warn on `Stderr`, continue to `t.Launch` with no release.
- `release()` is idempotent (`sync.Once`); safe on any exit path.
- caffeinate self-terminates via `-w <pid>` if cove crashes before release.

## Testing

- `internal/connect/connect_test.go`:
  a `fakeInhibitor` records `Inhibit`/`release` calls.
  Assert the inhibitor is acquired before `Launch` and released after;
  assert an `Inhibit` error still proceeds to `Launch`
  and writes the warning to `Stderr`;
  assert `SkipAuth`/auth paths are unaffected.
- `internal/awake`:
  a noop contract test that runs on every platform;
  a darwin-tagged test (skipped off-Mac) that `Inhibit()` starts
  and `release()` tears down without error.
