// Package colima implements the Backend interface over local Docker (Colima).
package colima

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/naming"
	"github.com/aethons-tools/cove/internal/runner"
)

func init() {
	backend.Register("colima", func(r runner.Runner) backend.Backend { return New(r) })
}

// dockerContext is the docker context colima creates for its default profile.
// Every docker invocation is pinned to it so cove never silently talks to a
// different daemon (e.g. Docker Desktop) than the colima backend intends —
// the failure mode where a stopped colima falls through to the wrong backend.
const dockerContext = "colima"

type Colima struct{ r runner.Runner }

func New(r runner.Runner) backend.Backend { return &Colima{r: r} }

// dargs prepends the pinned docker context to a docker subcommand's arguments.
func dargs(args ...string) []string {
	return append([]string{"--context", dockerContext}, args...)
}

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

// shadowArgs emits, for a shared workspace's declared shadow-dirs, one -v
// overmount per dir (a per-sandbox volume named via naming.ShadowVolume) plus a
// single -e COVE_SHADOW_DIRS the entrypoint reads to chown the fresh mountpoints.
// It returns the volume names used so Create can record them for teardown. Empty
// for a non-shared mount or when no shadow-dirs are declared (COV-132).
func shadowArgs(container string, ws backend.WorkspaceMount) (args, names []string) {
	if ws.Mode != backend.Shared || len(ws.ShadowDirs) == 0 {
		return nil, nil
	}
	for _, d := range ws.ShadowDirs {
		name := naming.ShadowVolume(container, d)
		args = append(args, "-v", name+":/home/agent/workspace/"+d)
		names = append(names, name)
	}
	args = append(args, "-e", "COVE_SHADOW_DIRS="+strings.Join(ws.ShadowDirs, " "))
	return args, names
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

// requireSysboxRuntime fails fast with an actionable message when the colima VM's
// docker daemon does not register the sysbox-runc runtime, which a docker:true
// instance needs (COV-117). at-cove detects but never installs Sysbox — the
// message points at the VM-side prerequisite. It parses `docker info -f '{{json
// .Runtimes}}'`, a map of runtime name → config, and checks for the sysbox-runc
// key. Preflight has already confirmed the daemon is reachable.
func (c *Colima) requireSysboxRuntime() error {
	out, err := c.r.Output("docker", dargs("info", "-f", "{{json .Runtimes}}")...)
	if err != nil {
		return fmt.Errorf("colima: cannot query docker runtimes for the docker:true preflight (docker: %v)", err)
	}
	var runtimes map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &runtimes); err != nil {
		return fmt.Errorf("colima: cannot parse docker runtimes %q: %w", strings.TrimSpace(out), err)
	}
	if _, ok := runtimes["sysbox-runc"]; !ok {
		return fmt.Errorf("docker:true needs the Sysbox runtime (sysbox-runc) in the colima VM, but `docker info` does not list it. at-cove detects but does not install it — install Sysbox CE in the colima Lima VM and make it persist across `colima stop/start` via a colima provision hook, then retry. See docs/superpowers/specs/2026-08-08-sysbox-docker-in-sandbox-design.md §H.")
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
	if err := c.r.Probe("docker", dargs("info")...); err != nil {
		// %v, not %w: wrapping the *ExitError would make main treat this as a
		// child command that already printed its own message and exit silently
		// with the code — swallowing this guidance. A plain error gets printed.
		return fmt.Errorf("colima is not reachable (docker context %q is stopped or not set up). Start it with:\n  colima start\n(docker: %v)", dockerContext, err)
	}
	return nil
}

// dockerBuild is the single docker-build site (COV-38): it resolves + gates the
// base, then builds buildDir FROM it and tags the result. Only Install routes its
// build through here, so `docker build` appears in exactly one place and the gate
// can never be bypassed. The run paths never build — create/recreate, chat, and
// work/dispatch all run the image Install already produced.
func (c *Colima) dockerBuild(buildDir, tag string, base backend.BaseSpec) (resolvedBase, digest string, err error) {
	if err := c.preflight(); err != nil {
		return "", "", err
	}
	resolvedBase, err = c.resolveBase(base)
	if err != nil {
		return "", "", err
	}
	// --progress=plain: line-by-line build output. BuildKit's default TTY progress
	// renderer right-aligns each step's duration and pads to a width that can
	// overflow the terminal by a column, wrapping the trailing "s" of "0.0s" onto
	// its own line. Plain output avoids that artifact.
	if err := c.r.Run("docker", dargs("build", "--progress=plain", "--build-arg", "BASE="+resolvedBase, "-t", tag, buildDir)...); err != nil {
		return "", "", err
	}
	// Capture the built image's OWN sha256 (its image ID) so runs can pin it
	// (COV-78). `docker build -t` only moves the mutable tag; inspecting {{.Id}} on
	// the tag right after the build reads back the exact image the tag now points
	// at. This is distinct from resolvedBase, which is the FROM-base digest.
	out, err := c.r.Output("docker", dargs("inspect", "--format", "{{.Id}}", tag)...)
	if err != nil {
		return "", "", err
	}
	return resolvedBase, strings.TrimSpace(out), nil
}

// runImage picks the image reference to `docker run`: the built-image digest when
// install captured one (an immutable pin), else the mutable tag for a legacy
// manifest that predates digest pinning (COV-78).
func runImage(tag, digest string) string {
	if digest != "" {
		return digest
	}
	return tag
}

// Install builds + gates + tags a kit's hardened image and reports the result —
// the only build path (COV-38). Run commands consume the tagged image; the base
// is resolved and the provenance gate runs exactly here.
func (c *Colima) Install(ctx backend.InstallContext) (backend.InstalledImage, error) {
	img := naming.Image(ctx.Kit)
	base, digest, err := c.dockerBuild(ctx.BuildDir, img, ctx.Base)
	if err != nil {
		return backend.InstalledImage{}, err
	}
	return backend.InstalledImage{Ref: img, Digest: digest, BaseDigest: base}, nil
}

// Create runs a pre-built image (COV-38): `docker run` only. The image comes
// from install.json (via ctx.Image) — Create never builds, resolves a base, or
// runs the gate; those live in Install.
func (c *Colima) Create(ctx backend.CreateContext) (backend.Instance, error) {
	if err := c.preflight(); err != nil {
		return backend.Instance{}, err
	}
	// docker:true needs the Sysbox runtime in the VM; detect + guide before we run
	// (at-cove does not install it) — COV-117.
	if ctx.Docker {
		if err := c.requireSysboxRuntime(); err != nil {
			return backend.Instance{}, err
		}
	}
	// Pin the run to the built-image digest install captured (COV-78); a legacy
	// manifest without one falls back to the mutable tag. The tag is still recorded
	// on the Instance (below) for display/diagnostics.
	img := runImage(ctx.Image, ctx.Digest)
	// Name the volumes once here — via the naming helper, the single source of
	// the name format (COV-77) — and record them once on the returned Instance
	// (into state), so Destroy consumes them from there rather than re-deriving
	// (COV-76). ctx.Name is the instance's atcove-{kit}-{class} base. A shared
	// workspace is a host bind-mount, so it records no workspace volume.
	vols := backend.VolumeSet{State: naming.AgentDataVolume(ctx.Name)}
	ws := ctx.Workspace.HostPath + ":/home/agent/workspace"
	if ctx.Workspace.Mode != backend.Shared {
		vols.Workspace = naming.WorkspaceVolume(ctx.Name)
		ws = vols.Workspace + ":/home/agent/workspace"
	}
	// docker:true records a persistent /var/lib/docker cache volume, so Destroy
	// removes exactly it (COV-117) — named once here via the naming helper.
	if ctx.Docker {
		vols.Docker = naming.DockerVolume(ctx.Name)
	}
	shadowRun, shadowVols := shadowArgs(ctx.Name, ctx.Workspace)
	vols.Shadow = shadowVols
	runArgs := []string{"run", "-d",
		"--name", ctx.Name,
	}
	runArgs = append(runArgs, initArgs(ctx.Docker)...)
	runArgs = append(runArgs, "--cap-add=NET_ADMIN")
	runArgs = append(runArgs, dnsArgs(ctx.DNS)...)
	runArgs = append(runArgs, dockerArgs(ctx.Docker, vols.Docker)...)
	runArgs = append(runArgs,
		"-p", "127.0.0.1::2222",
		"-v", vols.State+":/agent-data",
		"-v", ws,
	)
	runArgs = append(runArgs, shadowRun...)
	runArgs = append(runArgs, img)
	if err := c.r.Run("docker", dargs(runArgs...)...); err != nil {
		return backend.Instance{}, err
	}
	return backend.Instance{
		Backend:     "colima",
		Container:   ctx.Name,
		Image:       ctx.Image, // the tag, for display; the run above pinned img (digest when present)
		ImageDigest: ctx.Digest,
		Workspace:   ctx.Workspace,
		Volumes:     vols,
	}, nil
}

// nonEmpty returns the non-empty entries of ss, preserving order — used to drop
// an absent workspace volume from the `docker volume rm` argv.
func nonEmpty(ss []string) []string {
	out := ss[:0:0]
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (c *Colima) Dial(name string) (backend.Endpoint, func(), error) {
	if err := c.preflight(); err != nil {
		return backend.Endpoint{}, func() {}, err
	}
	out, err := c.r.Output("docker", dargs("port", name, "2222")...)
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

func (c *Colima) Destroy(inst backend.Instance, keepVolumes bool) error {
	if err := c.preflight(); err != nil {
		return err
	}
	// Force-remove the container (the rm itself never carries -v; volumes are
	// named, not anonymous, so -v wouldn't touch them anyway). recreate passes
	// keepVolumes=true so /agent-data (saved login) and the workspace survive.
	if err := c.r.Run("docker", dargs("rm", "-f", inst.Container)...); err != nil {
		return err
	}
	// A real destroy purges the instance's named volumes now that the container
	// (their only user) is gone. The names come from the recorded Instance
	// (COV-76) — Create named them (via naming), so there is no second derivation
	// site. A legacy instance (state written before volumes were recorded) has no
	// State name; fall back to the historical <container>-state/-workspace shape
	// so pre-rename sandboxes still tear down under their own (old) names — this
	// is deliberately NOT the naming helper's -agent-data, which would miss the
	// old -state volume. Best-effort: a missing volume (e.g. a shared workspace
	// records no workspace volume) must not fail the teardown.
	if !keepVolumes {
		vols := append([]string{inst.Volumes.State, inst.Volumes.Workspace, inst.Volumes.Docker}, inst.Volumes.Shadow...)
		if inst.Volumes.State == "" {
			vols = []string{inst.Container + "-state", inst.Container + "-workspace"}
		}
		args := append([]string{"volume", "rm", "-f"}, nonEmpty(vols)...)
		_ = c.r.Run("docker", dargs(args...)...)
	}
	// The image is deliberately NOT removed: it is an `install` artifact (COV-38),
	// not a per-create build. create/recreate/work consume it without rebuilding,
	// so removing it here would break `recreate` (destroy→create finds no image)
	// and leave a still-present install.json pointing at a deleted image (COV-63).
	// Image lifecycle belongs to `install` (a re-install overwrites the tag).
	return nil
}

// RemoveImage removes a kit's compiled image (`docker rmi`) — the inverse of
// Install and the sole image-teardown path (COV-64). It is invoked only by
// `at-cove uninstall`; the container lifecycle (Destroy/recreate) never removes
// the image, which is an `install` artifact (COV-63). The command layer treats a
// failure here as best-effort (the image may already be gone), so RemoveImage
// simply reports what `docker rmi` did.
func (c *Colima) RemoveImage(image string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	return c.r.Run("docker", dargs("rmi", image)...)
}

func (c *Colima) GetStatus(name string) (backend.State, error) {
	if err := c.preflight(); err != nil {
		return backend.StateAbsent, err
	}
	out, err := c.r.Output("docker", dargs("inspect", "-f", "{{.State.Running}}", name)...)
	if err != nil {
		return backend.StateAbsent, nil // no such container
	}
	if strings.TrimSpace(out) == "true" {
		return backend.StateRunning, nil
	}
	return backend.StateStopped, nil
}
