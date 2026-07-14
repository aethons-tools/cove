package kit

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SecretConfig is a kit's *demand* for a secret (keyed by its env var name in a
// secrets map): a name plus a human description of its purpose. How the value is
// produced is a machine-side concern (see internal/usersecret) — a kit never
// carries a resolver command.
type SecretConfig struct {
	Description string `yaml:"description"`
}

// baseEnvKeys are the /etc/environment variables the sealed hardening layer
// owns. A kit's image.env may not set these: overriding them would breach the
// additive guarantee (e.g. an image.env PATH would, since pam_env is last-wins,
// produce a second PATH= line and clobber the base PATH; a proxy var would
// weaken the egress gate). Keep in sync with the Dockerfile's /etc/environment
// block.
var baseEnvKeys = map[string]bool{
	"PATH":              true,
	"CLAUDE_CONFIG_DIR": true,
	"http_proxy":        true,
	"https_proxy":       true,
	"HTTP_PROXY":        true,
	"HTTPS_PROXY":       true,
	"no_proxy":          true,
	"NO_PROXY":          true,
}

// ImageConfig declares additive, build-time customisations of the sandbox image.
// cove translates each field to the correct sealed mechanism; every field is
// additive to the hardened baseline and never overrides it.
type ImageConfig struct {
	SetupScripts   []string          `yaml:"setup-scripts"`   // kit-relative scripts run as root at build, in place
	Paths          []string          `yaml:"paths"`           // appended to PATH in /etc/environment
	Env            map[string]string `yaml:"env"`             // KEY=VALUE written to /etc/environment
	AllowedDomains []string          `yaml:"allowed-domains"` // added to the squid egress allow-list
}

const commonKey = "<common>"

// Worker declares an autonomous handler class: the role prompt at-cove sends the
// agent (own-only, required) plus scheduling attrs that may be inherited from the
// workers <common> base.
type Worker struct {
	Prompt      string `yaml:"prompt,omitempty"`
	Timeout     string `yaml:"timeout,omitempty"` // Go duration
	Concurrency int    `yaml:"concurrency,omitempty"`
}

// ResolvedWorker returns the named worker with the workers <common> base merged
// in (own overrides <common>; prompt is own-only). It errors if class is empty,
// the reserved <common> key, or absent.
func (c Config) ResolvedWorker(class string) (Worker, error) {
	if class == "" || class == commonKey {
		return Worker{}, fmt.Errorf("kit %q: %q is not a dispatchable worker class", c.Name, class)
	}
	own, ok := c.Workers[class]
	if !ok {
		return Worker{}, fmt.Errorf("kit %q declares no worker class %q", c.Name, class)
	}
	base := c.Workers[commonKey] // zero value if absent
	if own.Timeout == "" {
		own.Timeout = base.Timeout
	}
	if own.Concurrency == 0 {
		own.Concurrency = base.Concurrency
	}
	return own, nil
}

// SourceControl names the code host + repo the kit targets — a tagged union
// (exactly one host; github only today). It is the single source of the repo
// identity and the host kind.
type SourceControl struct {
	GitHub *GitHubSource `yaml:"github,omitempty"`
}

type GitHubSource struct {
	Project    string                  `yaml:"project"`               // "owner/name"
	MainBranch string                  `yaml:"main-branch,omitempty"` // base branch; default "main"
	Secrets    map[string]SecretConfig `yaml:"secrets,omitempty"`     // well-known: AT_TASK_GIT_TOKEN
}

// GitTokenName reports the code-host token demand the kit declares under
// source-control.github.secrets, if present. The value is supplied machine-side;
// the kit only names the demand (the structural air-gap: it lives at a distinct
// schema location from the root/agent secrets).
func (c Config) GitTokenName() (string, bool) {
	if c.SourceControl == nil || c.SourceControl.GitHub == nil {
		return "", false
	}
	if _, ok := c.SourceControl.GitHub.Secrets["AT_TASK_GIT_TOKEN"]; !ok {
		return "", false
	}
	return "AT_TASK_GIT_TOKEN", true
}

// Active returns the set host, or an error if not exactly one.
func (s *SourceControl) Active() (string, error) {
	n, name := 0, ""
	if s.GitHub != nil {
		n, name = n+1, "github"
	}
	if n != 1 {
		return "", errors.New("must set exactly one host (github)")
	}
	return name, nil
}

// Tracker names the issue tracker the kit's scheduler drives — a tagged union
// (exactly one provider; linear only today). Consumed by `at-cove dispatch`.
type Tracker struct {
	Linear *LinearTracker `yaml:"linear,omitempty"`
}

func (t *Tracker) Active() (string, error) {
	if t.Linear != nil {
		return "linear", nil
	}
	return "", errors.New("must set exactly one provider (linear)")
}

// LinearTracker wires the scheduler to one Linear team.
type LinearTracker struct {
	Team             string                  `yaml:"team"`
	PollInterval     string                  `yaml:"poll-interval"`
	ClassLabelPrefix string                  `yaml:"class-label-prefix"`
	States           StateMap                `yaml:"states"`
	Secrets          map[string]SecretConfig `yaml:"secrets"`
}

// StateMap binds the design's lifecycle roles to a team's real state names.
type StateMap struct {
	Ready      string `yaml:"ready"`
	InProgress string `yaml:"in-progress"`
	InReview   string `yaml:"in-review"`
	Done       string `yaml:"done"`
	NeedsInput string `yaml:"needs-input"`
	Blocked    string `yaml:"blocked"`
}

// Dispatch holds the scheduler policy knobs (consumed by `at-cove dispatch`).
type Dispatch struct {
	Concurrency      int    `yaml:"concurrency"`
	ReaperTimeout    string `yaml:"reaper-timeout"`
	DispatchOverhead string `yaml:"dispatch-overhead"`
}

// Collaborator declares an interactive (chat) handler class: an optional role
// prompt injected into the session, an optional default marker, and a secrets
// bucket (inherited from the collaborators <common> base). GitHub/Linear ride
// the human's connectors, so secrets are often empty.
type Collaborator struct {
	Prompt  string                  `yaml:"prompt,omitempty"`
	Default bool                    `yaml:"default,omitempty"`
	Secrets map[string]SecretConfig `yaml:"secrets,omitempty"`
}

// ResolvedCollaborator returns the named collaborator with the collaborators
// <common> secrets merged in (own key wins). Errors like ResolvedWorker.
func (c Config) ResolvedCollaborator(class string) (Collaborator, error) {
	if class == "" || class == commonKey {
		return Collaborator{}, fmt.Errorf("kit %q: %q is not a collaborator class", c.Name, class)
	}
	own, ok := c.Collaborators[class]
	if !ok {
		return Collaborator{}, fmt.Errorf("kit %q declares no collaborator class %q", c.Name, class)
	}
	merged := map[string]SecretConfig{}
	for k, v := range c.Collaborators[commonKey].Secrets {
		merged[k] = v
	}
	for k, v := range own.Secrets {
		merged[k] = v
	}
	own.Secrets = merged
	return own, nil
}

// SelectCollaborator resolves the CLI's optional collaborator positional to a
// class. explicit=="" means "choose the default": the sole class, or the one
// marked default:true, or an error if ambiguous. No collaborators defined ->
// ("", false, nil): a plain session. <common> is never selectable.
func (c Config) SelectCollaborator(explicit string) (string, bool, error) {
	names := make([]string, 0, len(c.Collaborators))
	for name := range c.Collaborators {
		if name != commonKey {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	if explicit != "" {
		if explicit == commonKey {
			return "", false, fmt.Errorf("%q is not a selectable collaborator", commonKey)
		}
		if _, ok := c.Collaborators[explicit]; !ok {
			return "", false, fmt.Errorf("kit %q declares no collaborator %q (have: %s)", c.Name, explicit, strings.Join(names, ", "))
		}
		return explicit, true, nil
	}
	switch len(names) {
	case 0:
		return "", false, nil
	case 1:
		return names[0], true, nil
	default:
		for _, name := range names {
			if c.Collaborators[name].Default {
				return name, true, nil
			}
		}
		return "", false, fmt.Errorf("kit %q has multiple collaborators; specify one of: %s", c.Name, strings.Join(names, ", "))
	}
}

// Config is the parsed contents of a kit's config.yml.
type Config struct {
	Name          string                  `yaml:"name"`
	Secrets       map[string]SecretConfig `yaml:"secrets"`
	Image         ImageConfig             `yaml:"image"`
	Workers       map[string]Worker       `yaml:"workers"`
	SourceControl *SourceControl          `yaml:"source-control,omitempty"`
	Tracker       *Tracker                `yaml:"tracker,omitempty"`
	Dispatch      *Dispatch               `yaml:"dispatch,omitempty"`
	Collaborators map[string]Collaborator `yaml:"collaborators,omitempty"`
}

// ParseConfig unmarshals and validates config.yml bytes. Unknown fields are
// rejected to catch typos early.
func ParseConfig(data []byte) (Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("config.yml: %w", err)
	}
	if cfg.Name == "" {
		return Config{}, fmt.Errorf("config.yml: name is required")
	}
	for name := range cfg.Secrets {
		if strings.TrimSpace(name) == "" {
			return Config{}, fmt.Errorf("config.yml: secrets: a secret name (map key) must not be empty")
		}
	}
	if err := rejectReservedSecretNames("secrets", cfg.Secrets); err != nil {
		return Config{}, err
	}
	for i, s := range cfg.Image.SetupScripts {
		if strings.TrimSpace(s) == "" {
			return Config{}, fmt.Errorf("config.yml: image.setup-scripts[%d]: must not be empty", i)
		}
	}
	for i, p := range cfg.Image.Paths {
		if strings.TrimSpace(p) == "" {
			return Config{}, fmt.Errorf("config.yml: image.paths[%d]: must not be empty", i)
		}
		if strings.Contains(p, "\n") {
			return Config{}, fmt.Errorf("config.yml: image.paths[%d]: must not contain a newline", i)
		}
	}
	for k, v := range cfg.Image.Env {
		if strings.TrimSpace(k) == "" {
			return Config{}, fmt.Errorf("config.yml: image.env: keys must not be empty")
		}
		if strings.ContainsAny(k, "=\n") {
			return Config{}, fmt.Errorf("config.yml: image.env: key %q must not contain '=' or a newline", k)
		}
		if baseEnvKeys[k] {
			return Config{}, fmt.Errorf("config.yml: image.env: %q is owned by the base image and cannot be overridden", k)
		}
		if strings.Contains(v, "\n") {
			return Config{}, fmt.Errorf("config.yml: image.env: value for %q must not contain a newline", k)
		}
	}
	for i, d := range cfg.Image.AllowedDomains {
		if strings.TrimSpace(d) == "" {
			return Config{}, fmt.Errorf("config.yml: image.allowed-domains[%d]: must not be empty", i)
		}
	}
	for class, w := range cfg.Workers {
		if strings.TrimSpace(class) == "" {
			return Config{}, fmt.Errorf("config.yml: workers: a class name (map key) must not be empty")
		}
		if isReservedAngleKey(class) {
			return Config{}, fmt.Errorf("config.yml: workers: %q is not a valid key (only %q is reserved)", class, commonKey)
		}
		if class == commonKey {
			// base: prompt not allowed; scalars validated below
			if strings.TrimSpace(w.Prompt) != "" {
				return Config{}, fmt.Errorf("config.yml: workers[%q]: the base must not set a prompt", commonKey)
			}
		} else if strings.TrimSpace(w.Prompt) == "" {
			return Config{}, fmt.Errorf("config.yml: workers[%q]: prompt is required", class)
		}
		if w.Timeout != "" {
			if err := checkKitDuration(fmt.Sprintf("workers[%q].timeout", class), w.Timeout); err != nil {
				return Config{}, err
			}
		}
		if w.Concurrency < 0 {
			return Config{}, fmt.Errorf("config.yml: workers[%q].concurrency must be >= 0", class)
		}
	}
	if cfg.SourceControl != nil {
		if _, err := cfg.SourceControl.Active(); err != nil {
			return Config{}, fmt.Errorf("config.yml: source-control: %w", err)
		}
		if gh := cfg.SourceControl.GitHub; gh != nil {
			if parts := strings.Split(gh.Project, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return Config{}, fmt.Errorf("config.yml: source-control.github.project must be \"owner/name\", got %q", gh.Project)
			}
			if gh.MainBranch == "" {
				gh.MainBranch = "main"
			}
			if len(gh.Secrets) > 0 {
				if err := checkWellKnownSecrets("source-control.github.secrets", gh.Secrets, "AT_TASK_GIT_TOKEN"); err != nil {
					return Config{}, err
				}
			}
		}
	}
	if cfg.Tracker != nil {
		if _, err := cfg.Tracker.Active(); err != nil {
			return Config{}, fmt.Errorf("config.yml: tracker: %w", err)
		}
		if lt := cfg.Tracker.Linear; lt != nil {
			if strings.TrimSpace(lt.Team) == "" {
				return Config{}, fmt.Errorf("config.yml: tracker.linear.team is required")
			}
			if err := checkKitDuration("tracker.linear.poll-interval", lt.PollInterval); err != nil {
				return Config{}, err
			}
			if lt.ClassLabelPrefix == "" {
				lt.ClassLabelPrefix = "class:"
			}
			states := map[string]string{
				"ready": lt.States.Ready, "in-progress": lt.States.InProgress,
				"in-review": lt.States.InReview, "done": lt.States.Done,
				"needs-input": lt.States.NeedsInput, "blocked": lt.States.Blocked,
			}
			for name, v := range states {
				if strings.TrimSpace(v) == "" {
					return Config{}, fmt.Errorf("config.yml: tracker.linear.states.%s is required", name)
				}
			}
			if err := checkWellKnownSecrets("tracker.linear.secrets", lt.Secrets,
				"AT_DISPATCH_TRACKER_TOKEN", "AT_DISPATCH_WEBHOOK_SECRET"); err != nil {
				return Config{}, err
			}
		}
	}
	if cfg.Dispatch != nil {
		if cfg.Dispatch.Concurrency < 1 {
			return Config{}, fmt.Errorf("config.yml: dispatch.concurrency must be >= 1, got %d", cfg.Dispatch.Concurrency)
		}
		if err := checkKitDuration("dispatch.reaper-timeout", cfg.Dispatch.ReaperTimeout); err != nil {
			return Config{}, err
		}
		if cfg.Dispatch.DispatchOverhead == "" {
			cfg.Dispatch.DispatchOverhead = "15m"
		}
		if err := checkKitDuration("dispatch.dispatch-overhead", cfg.Dispatch.DispatchOverhead); err != nil {
			return Config{}, err
		}
	}
	if err := validateClassTree("collaborators", collaboratorKeys(cfg.Collaborators)); err != nil {
		return Config{}, err
	}
	defaults := 0
	for name, col := range cfg.Collaborators {
		if err := rejectReservedSecretNames(fmt.Sprintf("collaborators[%q].secrets", name), col.Secrets); err != nil {
			return Config{}, err
		}
		if name == commonKey {
			if col.Prompt != "" || col.Default {
				return Config{}, fmt.Errorf("config.yml: collaborators[%q]: the base must not set a prompt or default", commonKey)
			}
			continue
		}
		if col.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return Config{}, fmt.Errorf("config.yml: collaborators: at most one may set default: true (got %d)", defaults)
	}
	return cfg, nil
}

// reservedSecretNames are the subsystem well-known secret names, which must be
// declared only under source-control/tracker, never in the general (root or
// collaborator) secrets buckets.
var reservedSecretNames = map[string]bool{
	"AT_TASK_GIT_TOKEN":          true,
	"AT_DISPATCH_TRACKER_TOKEN":  true,
	"AT_DISPATCH_WEBHOOK_SECRET": true,
}

// rejectReservedSecretNames forbids the subsystem well-known secret names in a
// general (VM-injected / collaborator) secrets map, so a misconfigured entry
// can never shadow the host-side, air-gapped subsystem credentials.
func rejectReservedSecretNames(field string, got map[string]SecretConfig) error {
	for k := range got {
		if reservedSecretNames[k] {
			return fmt.Errorf("config.yml: %s: %q is a reserved subsystem secret and must be declared under source-control/tracker, not here", field, k)
		}
	}
	return nil
}

// checkWellKnownSecrets requires each allowed secret name to be present (the
// value itself is supplied machine-side, e.g. from the user's secrets.yml, at
// run time) and rejects any name outside the allowed set.
func checkWellKnownSecrets(field string, got map[string]SecretConfig, allowed ...string) error {
	want := map[string]bool{}
	for _, a := range allowed {
		want[a] = true
		if _, ok := got[a]; !ok {
			return fmt.Errorf("config.yml: %s.%s is required", field, a)
		}
	}
	for k := range got {
		if !want[k] {
			return fmt.Errorf("config.yml: %s: unknown secret %q (allowed: %v)", field, k, allowed)
		}
	}
	return nil
}

// validateClassTree rejects reserved-looking keys other than <common> and empty
// keys in a class map.
func validateClassTree(field string, keys []string) error {
	for _, k := range keys {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("config.yml: %s: a class name (map key) must not be empty", field)
		}
		if isReservedAngleKey(k) {
			return fmt.Errorf("config.yml: %s: %q is not a valid key (only %q is reserved)", field, k, commonKey)
		}
	}
	return nil
}

func collaboratorKeys(m map[string]Collaborator) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// isReservedAngleKey reports whether key is <…>-wrapped but not the one allowed
// reserved base key <common>.
func isReservedAngleKey(key string) bool {
	if !strings.HasPrefix(key, "<") || !strings.HasSuffix(key, ">") {
		return false
	}
	return key != commonKey
}

func checkKitDuration(field, v string) error {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fmt.Errorf("config.yml: %s must be a positive Go duration (e.g. 30m), got %q", field, v)
	}
	return nil
}
