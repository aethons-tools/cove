package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/kit"
)

// claudeMCPConfigPath is the per-run MCP config the harness generates
// (messaging + the kit's servers) and passes as --mcp-config. tmpfs, like the
// prompt/context cove-master is launched with; it holds no secret values —
// kit header values are ${VAR} references Claude Code expands at start.
const claudeMCPConfigPath = "/dev/shm/cove-agent-mcp.json"

// claudeSettingsPath is the per-run settings file rendered from a model-spec's
// claude.settings (preferences only — validated Jam-side) and passed as
// --settings; tmpfs like the MCP config.
const claudeSettingsPath = "/dev/shm/cove-agent-settings.json"

// claudeVersionTimeout bounds the `claude --version` pre-flight.
const claudeVersionTimeout = 30 * time.Second

// claudeMessaging is the guaranteed Jam messaging server entry (exactly the
// entry the image's /etc/claude-code/mcp.json carried before COV-240).
var claudeMessaging = struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}{"cove-master", []string{"mcp"}}

// Claude is the Claude Code harness: claude -p with stream-json in and out
// (see docs/usage/jam/session-events.md). The zero value uses the production
// paths.
type Claude struct {
	// MCPConfigPath is the generated --mcp-config file; empty = claudeMCPConfigPath.
	MCPConfigPath string
	// KitMCPServersPath is the kit's baked mcp-servers JSON (name → server);
	// empty = kit.MCPServersImagePath.
	KitMCPServersPath string
	// SettingsPath is the generated --settings file; empty = claudeSettingsPath.
	SettingsPath string
	// CLIVersion returns `claude --version`'s output; nil runs the real binary.
	// Called only when a model-spec's version constraint needs checking.
	CLIVersion func() (string, error)
}

var _ Harness = Claude{}

func (c Claude) mcpConfig() string {
	if c.MCPConfigPath == "" {
		return claudeMCPConfigPath
	}
	return c.MCPConfigPath
}

func (c Claude) settings() string {
	if c.SettingsPath == "" {
		return claudeSettingsPath
	}
	return c.SettingsPath
}

func (c Claude) cliVersion() (string, error) {
	if c.CLIVersion != nil {
		return c.CLIVersion()
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeVersionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "--version").Output()
	return string(out), err
}

func (c Claude) kitMCPServers() string {
	if c.KitMCPServersPath == "" {
		return kit.MCPServersImagePath
	}
	return c.KitMCPServersPath
}

// Validate is the pre-flight for spec (nil = built-in defaults). It fails loud
// rather than launch a broken agent (COV-190):
//
//   - a spec for another harness family is refused, and so is a policy.mode
//     that is not an accepted Claude permission mode
//     (modelspec.CheckPermissionMode — plan included: a headless cove could
//     never leave it). Defense in depth over Jam's write-time validation;
//   - the CLI version check: `claude --version` must satisfy spec.Version
//     (modelspec.ParseConstraint; an empty or "*" constraint skips the check).
//     A missing or unrunnable claude, an unparseable version, or a mismatch is
//     an error naming the spec, the constraint and what was found;
//   - the run's one MCP config — the guaranteed messaging server plus the kit's
//     servers — is generated: claude with --mcp-config pointing at a missing
//     file registers no servers, so the agent would have no intercom read/send
//     tools and flail. --strict-mcp-config (see Command) then loads only these.
//     A missing kit file (a stale image built before COV-240), a kit entry named
//     messaging, or a literal header value is refused too — re-checked here as
//     defense in depth over kit validation;
//   - spec's claude.settings, when non-empty, is written to the per-run
//     settings file Command passes as --settings (removed otherwise).
func (c Claude) Validate(spec *modelspec.Spec) error {
	if spec != nil {
		if spec.Type != modelspec.HarnessClaude {
			return fmt.Errorf("agentrun: model-spec %q is for harness %q, not %q", spec.Name, spec.Type, modelspec.HarnessClaude)
		}
		if err := modelspec.CheckPermissionMode(spec.Policy.Mode); err != nil {
			return fmt.Errorf("agentrun: model-spec %q: %w", spec.Name, err)
		}
		if err := c.checkVersion(spec); err != nil {
			return err
		}
	}
	if err := c.writeMCPConfig(); err != nil {
		return err
	}
	return c.writeSettings(spec)
}

// checkVersion runs `claude --version` and matches it against spec.Version.
func (c Claude) checkVersion(spec *modelspec.Spec) error {
	con, err := modelspec.ParseConstraint(spec.Version)
	if err != nil {
		return fmt.Errorf("agentrun: model-spec %q: %w", spec.Name, err)
	}
	if con.Any() {
		return nil
	}
	out, err := c.cliVersion()
	if err != nil {
		return fmt.Errorf("agentrun: model-spec %q requires claude %s, but `claude --version` failed (is Claude Code installed in this image?): %w", spec.Name, con, err)
	}
	v, err := modelspec.ParseVersion(out)
	if err != nil {
		return fmt.Errorf("agentrun: model-spec %q requires claude %s, but `claude --version` printed no version: %w", spec.Name, con, err)
	}
	if !con.Allows(v) {
		return fmt.Errorf("agentrun: model-spec %q requires claude %s, but this image has claude %s — rebuild the image or change the spec's version", spec.Name, con, v)
	}
	return nil
}

// writeSettings renders spec's claude.settings to the per-run settings file,
// or removes a stale one when the spec sets none.
func (c Claude) writeSettings(spec *modelspec.Spec) error {
	settings := claudeSettings(spec)
	if len(settings) == 0 {
		if err := os.Remove(c.settings()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("agentrun: stale settings %q could not be removed: %w", c.settings(), err)
		}
		return nil
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("agentrun: model-spec %q claude.settings: %w", spec.Name, err)
	}
	if err := writeFileAtomic(c.settings(), append(b, '\n')); err != nil {
		return fmt.Errorf("agentrun: settings %q could not be written: %w", c.settings(), err)
	}
	return nil
}

func claudeSettings(spec *modelspec.Spec) map[string]any {
	if spec == nil || spec.Claude == nil {
		return nil
	}
	return spec.Claude.Settings
}

// writeMCPConfig generates the run's one MCP config (see Validate).
func (c Claude) writeMCPConfig() error {
	raw, err := os.ReadFile(c.kitMCPServers())
	if err != nil {
		return fmt.Errorf("agentrun: kit MCP servers %q missing or unreadable: %w", c.kitMCPServers(), err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var servers map[string]kit.MCPServer
	if err := dec.Decode(&servers); err != nil {
		return fmt.Errorf("agentrun: kit MCP servers %q: %w", c.kitMCPServers(), err)
	}
	if err := kit.ValidateMCPServers("agentrun: kit MCP servers", servers); err != nil {
		return err
	}
	all := map[string]any{}
	for name, s := range servers {
		all[name] = s
	}
	all[kit.MCPReservedName] = claudeMessaging // last: never kit-overridable
	b, err := json.Marshal(map[string]any{"mcpServers": all})
	if err != nil {
		return fmt.Errorf("agentrun: MCP config: %w", err)
	}
	if err := writeFileAtomic(c.mcpConfig(), append(b, '\n')); err != nil {
		return fmt.Errorf("agentrun: MCP config %q could not be written: %w", c.mcpConfig(), err)
	}
	return nil
}

// writeFileAtomic writes data to a temp file beside path and renames it over
// path, so claude never reads a partial config and a pre-planted symlink at
// path is replaced rather than followed.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// Command builds claude's argv for one episode. ep.Continued prepends
// --continue. The prompt is not in argv: it is the first stream-json message
// on stdin. From ep.Spec it applies the permission policy (see claudePolicy),
// model.id (--model), model.effort (--effort), a non-empty claude.settings
// (--settings, the file Validate wrote) and the provider env (see claudeEnv).
// The MCP config is not taken from the spec (yet): the generated --mcp-config
// stays; claude.plugins are not applied (yet).
func (c Claude) Command(ep Episode) (string, []string, map[string]string) {
	args := []string{"-p"}
	if ep.Continued {
		args = append(args, "--continue")
	}
	// stream-json stdout is the session event source (see docs/usage/jam/session-events.md).
	args = append(args, "--input-format", "stream-json", "--output-format", "stream-json", "--verbose")
	args = append(args, claudePolicy(ep.Spec)...)
	args = append(args, "--mcp-config", c.mcpConfig(), "--strict-mcp-config")
	if spec := ep.Spec; spec != nil {
		if spec.Model.ID != "" {
			args = append(args, "--model", spec.Model.ID)
		}
		if spec.Model.Effort != "" {
			args = append(args, "--effort", spec.Model.Effort)
		}
		if len(claudeSettings(spec)) > 0 {
			args = append(args, "--settings", c.settings())
		}
	}
	if ep.ContextCore != "" {
		// snapshot off: the default replays the first turn's system prompt on
		// every --continue, which would hide context updates. (claude hard-fails
		// on a missing --append-system-prompt-file; Run passes "" if the context
		// could not be written.)
		args = append(args, "--append-system-prompt-file", ep.ContextCore, "--system-prompt-snapshot", "off")
	}
	return "claude", args, claudeEnv(ep.Spec)
}

// workerResultRel is the worker-result file the agent is told to write (the
// session boilerplate and resumePrompt), relative to its cwd, the work dir.
const workerResultRel = ".at-task/worker-result.json"

// claudeAlwaysAllowed are the allow rules every non-bypass mode gets ahead of
// the spec's own: the guaranteed messaging server's tools (mcp__SERVER allows
// all of a server's tools), so a headless agent can always read and send on
// the intercom, and the worker-result file the run's outcome is read from
// (Edit rules cover the Write tool; the path is relative to the agent's cwd,
// the work dir). A spec deny rule still wins over these.
var claudeAlwaysAllowed = []string{
	"mcp__" + kit.MCPReservedName,
	"Edit(" + workerResultRel + ")",
}

// claudePolicy renders spec.policy as claude flags (COV-239):
//
//   - mode empty (or no spec) or modelspec.ModeBypassPermissions →
//     --dangerously-skip-permissions, byte-identical to the argv before
//     model-specs. Both spellings reach the same bypassPermissions session in
//     today's image; the flag is kept so claude-default and a legacy
//     (spec-less) cove launch exactly as before, whatever the one-time bypass
//     acceptance (managed bypassPermissionsModeAccepted /
//     skipDangerousModePermissionPrompt) does.
//   - any other mode → one --permission-mode=MODE element (Validate has
//     already refused an unknown mode, and plan), then claudeAlwaysAllowed.
//   - each allow / deny rule → one --allowedTools=<rule> / --disallowedTools=<rule>
//     element (claude accumulates repeated flags). The = form keeps a rule one
//     argv element that can never be read as a flag, and keeps the variadic
//     flag from swallowing the next argument. Deny applies in every mode,
//     bypassPermissions included; allow only matters where claude would ask.
//
// Flags, not a permissions block in the --settings file: Jam refuses
// permissions in claude.settings (policy owns it), the rules stay visible in
// argv, and session flags don't merge with settings-file permissions.
func claudePolicy(spec *modelspec.Spec) []string {
	var p modelspec.Policy
	if spec != nil {
		p = spec.Policy
	}
	var args []string
	if p.Mode == "" || p.Mode == modelspec.ModeBypassPermissions {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		args = append(args, "--permission-mode="+p.Mode)
		for _, r := range claudeAlwaysAllowed {
			args = append(args, "--allowedTools="+r)
		}
	}
	for _, r := range p.Allow {
		args = append(args, "--allowedTools="+r)
	}
	for _, r := range p.Deny {
		args = append(args, "--disallowedTools="+r)
	}
	return args
}

// claudeEnv is the provider env a spec implies: vertex sets
// CLAUDE_CODE_USE_VERTEX=1, bedrock CLAUDE_CODE_USE_BEDROCK=1, anthropic
// nothing; then claude.provider-env. A protected, reserved or
// credential-carrying key is dropped (defense in depth: Jam refuses them at
// write). nil when there is nothing to set.
func claudeEnv(spec *modelspec.Spec) map[string]string {
	if spec == nil || spec.Claude == nil {
		return nil
	}
	env := map[string]string{}
	for k, v := range spec.Claude.ProviderEnv {
		if kit.ProtectedEnvKey(k) || modelspec.CredentialEnvKey(k) ||
			strings.HasPrefix(k, "AT_JAM_") || strings.HasPrefix(k, "AT_HARBOR_") {
			continue
		}
		env[k] = v
	}
	switch spec.Claude.Provider {
	case "vertex":
		env["CLAUDE_CODE_USE_VERTEX"] = "1"
	case "bedrock":
		env["CLAUDE_CODE_USE_BEDROCK"] = "1"
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

// EncodeInput encodes text as one stream-json stdin line.
func (Claude) EncodeInput(text string) []byte {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Message msg    `json:"message"`
	}{"user", msg{"user", text}})
	return append(b, '\n')
}

type claudeEvent struct {
	Type            string `json:"type"`
	Subtype         string `json:"subtype"`
	TaskID          string `json:"task_id"`
	QueuedTurnCount int    `json:"queued_turn_count"`
	Tasks           []struct {
		TaskID      string `json:"task_id"`
		Description string `json:"description"`
	} `json:"tasks"`
}

// ParseEvent maps one claude stream-json stdout line:
//   - result → TurnEnd (QueuedEmpty when queued_turn_count is 0)
//   - system/background_tasks_changed → BackgroundTasks (the full list).
//     claude empties the list BEFORE the task's task_notification arrives.
//   - system/task_notification → BackgroundDone (the notification starts a turn)
//   - system/init, assistant, user → TurnStart
//   - anything else → Other
func (Claude) ParseEvent(line []byte) (Event, error) {
	var ev claudeEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return Event{}, err
	}
	switch {
	case ev.Type == "result":
		return Event{Kind: EventTurnEnd, QueuedEmpty: ev.QueuedTurnCount == 0}, nil
	case ev.Type == "system" && ev.Subtype == "background_tasks_changed":
		tasks := make([]BackgroundTask, len(ev.Tasks))
		for i, k := range ev.Tasks {
			tasks[i] = BackgroundTask{ID: k.TaskID, Description: k.Description}
		}
		return Event{Kind: EventBackgroundTasks, Tasks: tasks}, nil
	case ev.Type == "system" && ev.Subtype == "task_notification":
		return Event{Kind: EventBackgroundDone, TaskID: ev.TaskID}, nil
	case ev.Type == "system" && ev.Subtype == "init",
		ev.Type == "assistant", ev.Type == "user":
		return Event{Kind: EventTurnStart}, nil
	}
	return Event{Kind: EventOther}, nil
}
