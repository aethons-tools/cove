// Package colima implements the backend surfaces Jam's launcher drives
// (DispatchOps, VolumeOps, RoleEgress, SSHCA, KitImageBuilder) over local
// Docker (Colima).
package colima

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
)

// dockerContext is the docker context colima creates for its default profile.
// Every docker invocation is pinned to it so cove never silently talks to a
// different daemon (e.g. Docker Desktop) than the colima backend intends —
// the failure mode where a stopped colima falls through to the wrong backend.
const dockerContext = "colima"

type Colima struct {
	r       runner.Runner
	context string // the pinned docker context ("" = dockerContext); see NewWithContext
	// quiet, when set, runs the cache-miss checks (HasKitImage, HasKitBase,
	// the base's local inspect before a pull) in place of r: WithBuildLog's
	// r copies output to the build log, and a miss's "No such image" does
	// not belong there. See quietRunner.
	quiet runner.Runner
}

// quietRunner is the runner for a cache-miss check: quiet when set, else r.
func (c *Colima) quietRunner() runner.Runner {
	if c.quiet != nil {
		return c.quiet
	}
	return c.r
}

// NewWithContext returns a Colima backend pinned to a docker context — typically
// colima-<profile>, the context `colima start --profile <profile>` creates — so
// a caller (e.g. at-studio-manager's colima.docker-context) can target a specific
// colima instance. Empty keeps the default colima context.
func NewWithContext(r runner.Runner, context string) *Colima {
	return &Colima{r: r, context: context}
}

// contextName is the context every docker call is pinned to.
func (c *Colima) contextName() string {
	if c.context == "" {
		return dockerContext
	}
	return c.context
}

// dargs prepends the pinned docker context to a docker subcommand's arguments.
func (c *Colima) dargs(args ...string) []string {
	return append([]string{"--context", c.contextName()}, args...)
}

// DockerArgs is dargs for other packages: a docker subcommand's argv pinned
// to this backend's context, so a reader (Jam's setup inspection) queries
// exactly the daemon the launcher builds and runs on.
func (c *Colima) DockerArgs(args ...string) []string { return c.dargs(args...) }

// dnsArgs renders the docker run --dns flags for the container's resolvers, one
// pair per IP. Empty dns yields no flags, so the container inherits Docker's
// default resolver — on colima that chains to the VM/host resolver, so a
// split-DNS VPN (e.g. GlobalProtect) resolves a self-hosted host correctly.
func dnsArgs(dns []string) []string {
	var a []string
	for _, ns := range dns {
		a = append(a, "--dns", ns)
	}
	return a
}

// addHostArgs renders docker run --add-host <h>:host-gateway for each host, so
// the container can reach a host-run service (e.g. a loopback-bound Jam
// broker) by name — COV-138. Empty yields no flags.
func addHostArgs(hosts []string) []string {
	var a []string
	for _, h := range hosts {
		a = append(a, "--add-host", h+":host-gateway")
	}
	return a
}

// dockerArgs renders the docker run flags that activate docker-in-sandbox via the
// Sysbox runtime when the kit opts in with docker:true (COV-117): the sandbox
// container runs under --runtime=sysbox-runc (so a rootful dockerd can run inside
// the unprivileged container), COVE_DOCKER=1 signals the entrypoint, and a
// persistent named volume backs the inner /var/lib/docker cache. When docker is
// false it yields no flags, so the run argv is byte-for-byte unchanged. It never
// emits --privileged, a socket mount, --device, or --security-opt — that is the
// whole point of Sysbox (see the design §C/§G).
func dockerArgs(docker bool, volume string) []string {
	if !docker {
		return nil
	}
	return []string{
		"--runtime=sysbox-runc",
		"-e", "COVE_DOCKER=1",
		"-v", volume + ":/var/lib/docker",
	}
}

// initArgs renders the tini flag (--init). A non-docker sandbox uses tini as PID 1
// (reaps zombies for the sshd process tree). A docker:true sandbox instead boots
// systemd as PID 1 — the entrypoint execs /sbin/init under Sysbox (COV-118), and
// systemd *must* be PID 1; running it under tini would make it a child process and
// break it. So --init is omitted exactly for docker:true, and kept otherwise, so
// the non-docker argv is byte-for-byte unchanged.
func initArgs(docker bool) []string {
	if docker {
		return nil
	}
	return []string{"--init"}
}

// HasSysboxRuntime reports whether the docker daemon behind c's pinned context
// registers the sysbox-runc runtime a docker:true instance needs (COV-117). It
// parses `docker info -f '{{json .Runtimes}}'`, a map of runtime name → config.
// An unreachable daemon or unparseable output is an error. Shared by the
// the launcher's preflight and `at-studio-manager colima check-docker`, so the two can't drift.
func (c *Colima) HasSysboxRuntime() (bool, error) {
	out, err := c.r.Output("docker", c.dargs("info", "-f", "{{json .Runtimes}}")...)
	if err != nil {
		return false, fmt.Errorf("colima: cannot query docker runtimes (docker: %v)", err)
	}
	var runtimes map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &runtimes); err != nil {
		return false, fmt.Errorf("colima: cannot parse docker runtimes %q: %w", strings.TrimSpace(out), err)
	}
	_, ok := runtimes["sysbox-runc"]
	return ok, nil
}

// requireSysboxRuntime fails fast with an actionable message when the colima VM
// lacks the sysbox-runc runtime. The launcher detects but never installs Sysbox — the
// message points at the VM-side prerequisite. Preflight has already confirmed
// the daemon is reachable.
func (c *Colima) requireSysboxRuntime() error {
	ok, err := c.HasSysboxRuntime()
	if err != nil {
		return fmt.Errorf("docker:true preflight: %w", err)
	}
	if !ok {
		return fmt.Errorf("docker:true needs the Sysbox runtime (sysbox-runc) in the colima VM, but `docker info` does not list it. Jam detects but does not install it — run `at-studio-manager colima setup-docker` on the host (then `colima restart`), or install Sysbox CE in the colima Lima VM by hand and make it persist across `colima stop/start` via a colima provision hook, then retry. See docs/usage/docker-in-sandbox.md.")
	}
	return nil
}

// preflight fails fast with an actionable message when the colima docker context
// is not reachable (colima stopped or never set up), rather than letting docker
// fall through to another daemon or emit a cryptic error. `docker info` exits
// non-zero only when the daemon behind the context is unreachable.
func (c *Colima) preflight() error {
	// Probe (not Output): we want only the exit status. `docker info` writes
	// benign daemon warnings (e.g. "bridge-nf-call-iptables is disabled") to
	// stderr, which Output would stream to the terminal on every connect.
	if err := c.r.Probe("docker", c.dargs("info")...); err != nil {
		// %v, not %w: wrapping the *ExitError would make main treat this as a
		// child command that already printed its own message and exit silently
		// with the code — swallowing this guidance. A plain error gets printed.
		return fmt.Errorf("colima is not reachable (docker context %q is stopped or not set up). Start it with:\n  %s\n(docker: %v)", c.contextName(), startHint(c.contextName()), err)
	}
	return nil
}

// startHint is the command that starts the colima instance behind context:
// colima-<profile> is the context of `colima start --profile <profile>`.
func startHint(context string) string {
	if p, ok := strings.CutPrefix(context, dockerContext+"-"); ok && p != "" {
		return "colima start --profile " + p
	}
	return "colima start"
}

// buildFromBase is the actual `docker build` — assemble context in, tagged image
// out — FROM an already-resolved base passed as the Dockerfile's BASE arg. It is
// BuildKitImage's body (the caller resolved and gated the base already).
// Context-pinned via dargs, so the build and any later RunEphemeral share the
// colima daemon.
func (c *Colima) buildFromBase(buildDir, tag, resolvedBase string, kitArgs map[string]string, noCache bool) (digest string, err error) {
	// --progress=plain: line-by-line build output (BuildKit's TTY renderer can
	// overflow the terminal by a column). --no-cache: rebuild every layer so the
	// build-time `claude`/plugin install re-runs instead of reusing cached layers.
	buildArgs := []string{"build", "--progress=plain"}
	if noCache {
		buildArgs = append(buildArgs, "--no-cache")
	}
	buildArgs = append(buildArgs, "--build-arg", "BASE="+resolvedBase)
	// kit build-args (sorted for a deterministic argv), after BASE, before -t.
	keys := make([]string, 0, len(kitArgs))
	for k := range kitArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		buildArgs = append(buildArgs, "--build-arg", k+"="+kitArgs[k])
	}
	buildArgs = append(buildArgs, "-t", tag, buildDir)
	if err := c.r.Run("docker", c.dargs(buildArgs...)...); err != nil {
		return "", err
	}
	// Capture the built image's OWN sha256 (its image ID) so runs can pin it
	// (COV-78). `docker build -t` only moves the mutable tag; inspecting {{.Id}} on
	// the tag right after the build reads back the exact image the tag now points
	// at. This is distinct from resolvedBase, which is the FROM-base digest.
	out, err := c.r.Output("docker", c.dargs("inspect", "--format", "{{.Id}}", tag)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// BuildKitImage builds a studio kit's assembled context into the tagged image on
// the colima daemon, FROM the base already resolved+gated by ResolveKitBase. It
// is the Jam launcher's build step — see backend.KitImageBuilder. Context-pinned, so the image lands in the same daemon
// RunEphemeral runs it from.
func (c *Colima) BuildKitImage(buildDir, tag, base string, buildArgs map[string]string, noCache bool) (digest string, err error) {
	if err := c.preflight(); err != nil {
		return "", err
	}
	if base == "" {
		return "", fmt.Errorf("build kit image %s: base is required (the Dockerfile is FROM ${BASE})", tag)
	}
	return c.buildFromBase(buildDir, tag, base, buildArgs, noCache)
}

// ResolveKitBase resolves + gates declaredBase for a role-named studio kit (the
// provenance gate is ON — no --allow-unverified escape hatch for brokered coves;
// "" resolves to the blessed default). See backend.KitImageBuilder.
func (c *Colima) ResolveKitBase(declaredBase string) (string, error) {
	return c.resolveBase(backend.BaseSpec{Base: declaredBase})
}

// ResolveKitBaseTar streams the studio kit's build context tar to `docker build -`
// (docker preserves file modes natively), tags the built image, and gates it
// (gate ON), per backend.KitImageBuilder. The build injects the blessed
// COVE_BASE_IMAGE arg, so a context that does `FROM ${COVE_BASE_IMAGE}` descends
// from the blessed base.
func (c *Colima) ResolveKitBaseTar(ctx io.Reader) (string, error) {
	return c.resolveBaseTar(ctx)
}

// ResolveKitBaseTarOn builds one kit-chain stage: the context tar streams to
// `docker build -` FROM parentRef, is gated against the blessed refs, and only
// then is tagged stageTag (per backend.KitImageBuilder).
func (c *Colima) ResolveKitBaseTarOn(ctx io.Reader, parentRef, stageTag string) (string, error) {
	return c.resolveBaseTarOn(ctx, parentRef, stageTag)
}

// HasKitBase reports whether a chain stage tag exists on the colima daemon; an
// absent tag is a (false, nil) miss, as in HasKitImage.
func (c *Colima) HasKitBase(tag string) (bool, error) { return c.HasKitImage(tag) }

// HasKitImage reports whether the tagged image exists on the colima daemon.
// `docker image inspect` exits non-zero when absent → (false, nil), the normal
// miss that drives PrepareKit, never surfaced as an error. Context-pinned so it
// queries the same daemon BuildKitImage/RunEphemeral use.
func (c *Colima) HasKitImage(tag string) (bool, error) {
	if _, err := c.quietRunner().Output("docker", c.dargs("image", "inspect", tag)...); err != nil {
		return false, nil
	}
	return true, nil
}

// WithBuildLog is c with every docker command run under ctx (a done ctx
// stops the build) and its output copied to log (runner.Scoped), per
// backend.LoggedKitBuilder. The build steps, argv and gate are c's own.
// Image-presence checks run under ctx too, their output dropped.
func (c *Colima) WithBuildLog(ctx context.Context, log io.Writer) backend.KitImageBuilder {
	return &Colima{r: runner.Scoped(ctx, c.r, log), context: c.context, quiet: runner.Scoped(ctx, c.r, io.Discard)}
}

var _ backend.LoggedKitBuilder = (*Colima)(nil)

// runImage picks the image reference to `docker run`: the built-image digest when
// install captured one (an immutable pin), else the mutable tag for a legacy
// manifest that predates digest pinning (COV-78).
func runImage(tag, digest string) string {
	if digest != "" {
		return digest
	}
	return tag
}

func (c *Colima) Dial(name string) (backend.Endpoint, func(), error) {
	if err := c.preflight(); err != nil {
		return backend.Endpoint{}, func() {}, err
	}
	out, err := c.r.Output("docker", c.dargs("port", name, "2222")...)
	if err != nil {
		return backend.Endpoint{}, func() {}, err
	}
	hostport := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]) // "127.0.0.1:49153"
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return backend.Endpoint{}, func() {}, fmt.Errorf("colima: cannot parse docker port output %q", out)
	}
	port, err := strconv.Atoi(hostport[i+1:])
	if err != nil {
		return backend.Endpoint{}, func() {}, fmt.Errorf("colima: bad port in %q: %w", hostport, err)
	}
	return backend.Endpoint{Host: hostport[:i], Port: port, User: "agent"}, func() {}, nil
}

func (c *Colima) GetStatus(name string) (backend.State, error) {
	if err := c.preflight(); err != nil {
		return backend.StateAbsent, err
	}
	out, err := c.r.Output("docker", c.dargs("inspect", "-f", "{{.State.Running}}", name)...)
	if err != nil {
		return backend.StateAbsent, nil // no such container
	}
	if strings.TrimSpace(out) == "true" {
		return backend.StateRunning, nil
	}
	return backend.StateStopped, nil
}
