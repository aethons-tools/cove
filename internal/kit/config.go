package kit

import (
	"bytes"
	"errors"
	"fmt"
	"net"
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

// ImageConfig declares the image a kit hardens and its additive egress. Build-time
// customization now lives in the kit's image/Dockerfile (COV-34); config.yml no
// longer carries setup-scripts/env/paths.
type ImageConfig struct {
	Base           string   `yaml:"base"`            // image ref to harden; mutually exclusive with an image/Dockerfile
	AllowedDomains []string `yaml:"allowed-domains"` // added to the squid egress allow-list
	// DNS pins the container's resolver IPs (docker run --dns). Empty (the default)
	// emits no --dns flag, so the container inherits Docker's default resolver —
	// which on colima chains to the VM/host resolver and so honors a split-DNS VPN
	// (e.g. a corporate GitLab reachable only via GlobalProtect). Set it to an
	// internal resolver IP when the default resolver can't see a self-hosted host.
	DNS []string `yaml:"dns,omitempty"`
}

const commonKey = "<common>"

// Worker declares an autonomous handler class: the role prompt at-cove sends the
// agent (own-only, required) plus scheduling attrs that may be inherited from the
// workers <common> base.
type Worker struct {
	Prompt  string `yaml:"prompt,omitempty"`
	Timeout string `yaml:"timeout,omitempty"` // Go duration
	// Concurrency is a pointer so an unset value (nil — inherits <common>) is
	// distinct from an explicit 0 (COV-87 footgun: 0 removes the per-class cap
	// rather than pausing the class, so it is rejected at validation). Use
	// ConcurrencyOrZero for the resolved cap (0 == "no per-class cap").
	Concurrency    *int                    `yaml:"concurrency,omitempty"`
	Secrets        map[string]SecretConfig `yaml:"secrets,omitempty"`
	AllowedDomains []string                `yaml:"allowed-domains,omitempty"` // added to the class's session egress (unioned with the workers <common> list)
}

// ConcurrencyOrZero is the resolved per-class concurrency cap: the set value, or
// 0 when unset (which the scheduler reads as "no per-class cap").
func (w Worker) ConcurrencyOrZero() int {
	if w.Concurrency == nil {
		return 0
	}
	return *w.Concurrency
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
	if own.Concurrency == nil {
		own.Concurrency = base.Concurrency
	}
	merged := map[string]SecretConfig{}
	for k, v := range base.Secrets {
		merged[k] = v
	}
	for k, v := range own.Secrets {
		merged[k] = v
	}
	own.Secrets = merged
	return own, nil
}

// ResolvedWorkerDomains returns the deduped, order-normalized union of the
// workers <common> allowed-domains and the named class's own list — the
// per-session egress *delta* squid receives at session start. Root
// (image.allowed-domains) is delivered separately by the baked kit file and is
// NOT part of this resolver, mirroring how root secrets sit outside
// ResolvedWorker. Errors like ResolvedWorker (empty / <common> / absent class).
func (c Config) ResolvedWorkerDomains(class string) ([]string, error) {
	if class == "" || class == commonKey {
		return nil, fmt.Errorf("kit %q: %q is not a dispatchable worker class", c.Name, class)
	}
	own, ok := c.Workers[class]
	if !ok {
		return nil, fmt.Errorf("kit %q declares no worker class %q", c.Name, class)
	}
	return unionDomains(c.Workers[commonKey].AllowedDomains, own.AllowedDomains), nil
}

// SourceControl names the code host + repo the kit targets — a tagged union
// (exactly one host; github or gitlab). It is the single source of the repo
// identity and the host kind.
type SourceControl struct {
	GitHub *GitHubSource `yaml:"github,omitempty"`
	GitLab *GitLabSource `yaml:"gitlab,omitempty"`
}

type GitHubSource struct {
	Project    string                  `yaml:"project"`               // "owner/name"
	MainBranch string                  `yaml:"main-branch,omitempty"` // base branch; default "main"
	Secrets    map[string]SecretConfig `yaml:"secrets,omitempty"`     // well-known: AT_TASK_GIT_TOKEN
}

// GitLabSource names a GitLab repo — gitlab.com or a self-hosted host. The project
// path may be nested (group/subgroup/name, ≥2 segments), unlike GitHub's owner/name.
type GitLabSource struct {
	Host       string                  `yaml:"host,omitempty"`        // bare hostname; default gitlab.com
	Project    string                  `yaml:"project"`               // "group/.../name"
	MainBranch string                  `yaml:"main-branch,omitempty"` // default "main"
	Secrets    map[string]SecretConfig `yaml:"secrets,omitempty"`     // well-known: AT_TASK_GIT_TOKEN
}

// GitTokenName reports the code-host token demand under the active provider's
// source-control.<provider>.secrets, if present.
func (c Config) GitTokenName() (string, bool) {
	if c.SourceControl == nil {
		return "", false
	}
	var secrets map[string]SecretConfig
	switch {
	case c.SourceControl.GitHub != nil:
		secrets = c.SourceControl.GitHub.Secrets
	case c.SourceControl.GitLab != nil:
		secrets = c.SourceControl.GitLab.Secrets
	default:
		return "", false
	}
	if _, ok := secrets["AT_TASK_GIT_TOKEN"]; !ok {
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
	if s.GitLab != nil {
		n, name = n+1, "gitlab"
	}
	if n != 1 {
		return "", errors.New("must set exactly one host (github or gitlab)")
	}
	return name, nil
}

// Repo is the resolved, provider-neutral repo identity a run command needs.
type Repo struct {
	Provider   string // "github" | "gitlab"
	Host       string // github.com | gitlab.com | self-hosted host
	Project    string // owner/name or group/.../name
	MainBranch string
}

// CloneURL is the HTTPS clone URL for the repo.
func (r Repo) CloneURL() string { return "https://" + r.Host + "/" + r.Project + ".git" }

// Repo returns the active provider's repo identity, or ok=false when no
// source-control is configured. GitHub has no host field — it is always github.com.
func (s *SourceControl) Repo() (Repo, bool) {
	if s == nil {
		return Repo{}, false
	}
	switch {
	case s.GitHub != nil:
		return Repo{Provider: "github", Host: "github.com", Project: s.GitHub.Project, MainBranch: s.GitHub.MainBranch}, true
	case s.GitLab != nil:
		return Repo{Provider: "gitlab", Host: s.GitLab.Host, Project: s.GitLab.Project, MainBranch: s.GitLab.MainBranch}, true
	}
	return Repo{}, false
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
	Prompt         string                  `yaml:"prompt,omitempty"`
	Default        bool                    `yaml:"default,omitempty"`
	ShareRepoDir   bool                    `yaml:"share-repo-dir,omitempty"` // opt this class's VM into a Shared bind-mount of the kit's repo dir (per-class only; rejected on <common>)
	Secrets        map[string]SecretConfig `yaml:"secrets,omitempty"`
	AllowedDomains []string                `yaml:"allowed-domains,omitempty"` // added to the class's session egress (unioned with the collaborators <common> list)
}

// ModelProvider switches the sandbox's agent off first-party Anthropic and onto a
// third-party Claude provider — a union keyed by provider name (vertex only today).
// Its presence is the switch; absent, the Anthropic auth paths are unchanged.
type ModelProvider struct {
	Vertex *VertexProvider `yaml:"vertex,omitempty"`
}

// VertexProvider configures Claude on Google Vertex AI. Because Claude Code's
// Vertex configuration is entirely environment-driven, the payload is a non-secret
// env map: at-cove demands the required keys and passes any other (non-protected)
// key through. The GCP *credential* is not here — it is a host-supplied file
// (see cmd/at-cove: the GOOGLE_APPLICATION_CREDENTIALS_JSON demand).
type VertexProvider struct {
	Env map[string]string `yaml:"env"`
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

// ResolvedCollaboratorDomains returns the deduped, order-normalized union of the
// collaborators <common> allowed-domains and the named class's own list — the
// per-session egress delta. Root is separate (see ResolvedWorkerDomains). Errors
// like ResolvedCollaborator.
func (c Config) ResolvedCollaboratorDomains(class string) ([]string, error) {
	if class == "" || class == commonKey {
		return nil, fmt.Errorf("kit %q: %q is not a collaborator class", c.Name, class)
	}
	own, ok := c.Collaborators[class]
	if !ok {
		return nil, fmt.Errorf("kit %q declares no collaborator class %q", c.Name, class)
	}
	return unionDomains(c.Collaborators[commonKey].AllowedDomains, own.AllowedDomains), nil
}

// unionDomains returns the deduped, sorted set union of the given domain lists.
// Domains are additive (a set), unlike the map-overwrite secrets merge.
func unionDomains(lists ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range lists {
		for _, d := range list {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	sort.Strings(out)
	return out
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
	ModelProvider *ModelProvider          `yaml:"model-provider,omitempty"`
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
	if err := validateSecretNames("secrets", cfg.Secrets, false); err != nil {
		return Config{}, err
	}
	if err := rejectReservedSecretNames("secrets", cfg.Secrets); err != nil {
		return Config{}, err
	}
	for i, d := range cfg.Image.AllowedDomains {
		if strings.TrimSpace(d) == "" {
			return Config{}, fmt.Errorf("config.yml: image.allowed-domains[%d]: must not be empty", i)
		}
	}
	for i, ns := range cfg.Image.DNS {
		if strings.TrimSpace(ns) == "" {
			return Config{}, fmt.Errorf("config.yml: image.dns[%d]: must not be empty", i)
		}
		if net.ParseIP(strings.TrimSpace(ns)) == nil {
			return Config{}, fmt.Errorf("config.yml: image.dns[%d]: %q is not a valid IP address (docker --dns requires an IP, not a hostname)", i, ns)
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
		if w.Concurrency != nil {
			if *w.Concurrency < 0 {
				return Config{}, fmt.Errorf("config.yml: workers[%q].concurrency must be >= 0", class)
			}
			if *w.Concurrency == 0 {
				// An explicit 0 is a footgun: unset already inherits <common>, and 0
				// removes the per-class cap (unbounded up to the global cap) rather than
				// pausing the class — the opposite of the likely intent.
				return Config{}, fmt.Errorf("config.yml: workers[%q].concurrency: 0 is not allowed — omit the field to inherit <common>, and to pause a class use tracker state, not concurrency 0 (which removes the per-class cap rather than pausing)", class)
			}
		}
		if err := validateSecretNames(fmt.Sprintf("workers[%q].secrets", class), w.Secrets, true); err != nil {
			return Config{}, err
		}
		if err := rejectReservedSecretNames(fmt.Sprintf("workers[%q].secrets", class), w.Secrets); err != nil {
			return Config{}, err
		}
		for i, d := range w.AllowedDomains {
			if strings.TrimSpace(d) == "" {
				return Config{}, fmt.Errorf("config.yml: workers[%q].allowed-domains[%d]: must not be empty", class, i)
			}
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
				if err := validateSecretNames("source-control.github.secrets", gh.Secrets, false); err != nil {
					return Config{}, err
				}
				if err := checkWellKnownSecrets("source-control.github.secrets", gh.Secrets, "AT_TASK_GIT_TOKEN"); err != nil {
					return Config{}, err
				}
			}
		}
		if gl := cfg.SourceControl.GitLab; gl != nil {
			if strings.ContainsAny(gl.Host, "/:") {
				return Config{}, fmt.Errorf("config.yml: source-control.gitlab.host must be a bare hostname (no scheme or path), got %q", gl.Host)
			}
			segs := strings.Split(gl.Project, "/")
			if len(segs) < 2 {
				return Config{}, fmt.Errorf("config.yml: source-control.gitlab.project must be \"group/…/name\" (≥2 segments), got %q", gl.Project)
			}
			for _, s := range segs {
				if strings.TrimSpace(s) == "" {
					return Config{}, fmt.Errorf("config.yml: source-control.gitlab.project has an empty path segment: %q", gl.Project)
				}
			}
			if gl.Host == "" {
				gl.Host = "gitlab.com"
			}
			if gl.MainBranch == "" {
				gl.MainBranch = "main"
			}
			if len(gl.Secrets) > 0 {
				if err := validateSecretNames("source-control.gitlab.secrets", gl.Secrets, false); err != nil {
					return Config{}, err
				}
				if err := checkWellKnownSecrets("source-control.gitlab.secrets", gl.Secrets, "AT_TASK_GIT_TOKEN"); err != nil {
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
			if err := validateSecretNames("tracker.linear.secrets", lt.Secrets, false); err != nil {
				return Config{}, err
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
		if err := validateSecretNames(fmt.Sprintf("collaborators[%q].secrets", name), col.Secrets, false); err != nil {
			return Config{}, err
		}
		if err := rejectReservedSecretNames(fmt.Sprintf("collaborators[%q].secrets", name), col.Secrets); err != nil {
			return Config{}, err
		}
		for i, d := range col.AllowedDomains {
			if strings.TrimSpace(d) == "" {
				return Config{}, fmt.Errorf("config.yml: collaborators[%q].allowed-domains[%d]: must not be empty", name, i)
			}
		}
		if name == commonKey {
			if col.Prompt != "" || col.Default || col.ShareRepoDir {
				return Config{}, fmt.Errorf("config.yml: collaborators[%q]: the base must not set a prompt, default, or share-repo-dir", commonKey)
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
	if err := validateModelProvider(cfg); err != nil {
		return Config{}, err
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
	gcpADCDemandName:             true,
}

// gcpADCDemandName is the well-known demand name a Vertex kit's GCP Application
// Default Credentials are supplied under (mirrors cmd/at-cove's gcpADCDemand
// const — duplicated here rather than imported, since internal/kit must not
// depend on cmd/at-cove). It is resolved host-side (from secrets.yml, never
// config.yml) and seeded into the VM as a file; it must never be declared as a
// general secret, which would also inject it into the agent's session env.
const gcpADCDemandName = "GOOGLE_APPLICATION_CREDENTIALS_JSON"

// agentBearerNames are agent-auth credentials that must NOT live in any
// non-worker secrets bucket: those are injected into `chat`/session envs, where
// an Anthropic bearer outranks the subscription login and disables the session's
// connectors. They belong only under workers.<class>.secrets.
var agentBearerNames = map[string]bool{
	"ANTHROPIC_AUTH_TOKEN": true,
	"ANTHROPIC_API_KEY":    true,
}

// validateSecretNames enforces the bucket-agnostic secret-name rules shared by
// every secrets: bucket: a name (map key) is never empty, and — outside the
// worker buckets, where the agent bearer is the intended credential — an
// Anthropic agent bearer is never declared. A bearer in any other bucket is
// injected into a `chat`/session env, where it outranks the subscription login
// and disables that session's connectors. allowBearers is true only for
// workers.<class>.secrets (including workers.<common>.secrets).
func validateSecretNames(field string, got map[string]SecretConfig, allowBearers bool) error {
	for k := range got {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("config.yml: %s: a secret name (map key) must not be empty", field)
		}
		if !allowBearers && agentBearerNames[k] {
			return fmt.Errorf("config.yml: %s: %q is an Anthropic agent bearer and must be declared under workers.<class>.secrets (or workers.<common>.secrets), not here — a bearer in this bucket is injected into a `chat`/session env, where it outranks the subscription login and disables that session's connectors; move it under `workers:`", field, k)
		}
	}
	return nil
}

// rejectReservedSecretNames forbids the subsystem well-known secret names in a
// general (VM-injected / collaborator) secrets map, so a misconfigured entry
// can never shadow the host-side, air-gapped subsystem credentials.
func rejectReservedSecretNames(field string, got map[string]SecretConfig) error {
	for k := range got {
		if !reservedSecretNames[k] {
			continue
		}
		if k == gcpADCDemandName {
			return fmt.Errorf("config.yml: %s: %q is the Vertex GCP credential — it is supplied host-side (secrets.yml) and seeded as a file, and must not be declared as a secret here (that would inject it into the agent's session env)", field, k)
		}
		return fmt.Errorf("config.yml: %s: %q is a reserved subsystem secret and must be declared under source-control/tracker, not here", field, k)
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

// vertexProtectedEnvKeys are sealed-owned or security-relevant variables a kit's
// model-provider env map must never set. Unlike a *secret* (demanded by the kit,
// supplied by the machine — the operator is the gate), an env value is
// kit-authored with no host gate, and the per-session env file is *sourced* in the
// session shell, so an unchecked value would shadow the sealed /etc/environment
// (e.g. the proxy vars) and could defeat egress. Rejected at validation and
// dropped defensively at injection ("additive, sealed-wins" for env).
var vertexProtectedEnvKeys = map[string]bool{
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"CLAUDE_CONFIG_DIR":              true,
	"GOOGLE_APPLICATION_CREDENTIALS": true,
	"PATH":                           true,
}

// vertexRequiredEnvKeys must be present in the provider env map.
var vertexRequiredEnvKeys = []string{"ANTHROPIC_VERTEX_PROJECT_ID", "CLOUD_ML_REGION"}

// Vertex returns the Vertex provider config and true when the kit targets Vertex.
func (c Config) Vertex() (*VertexProvider, bool) {
	if c.ModelProvider == nil || c.ModelProvider.Vertex == nil {
		return nil, false
	}
	return c.ModelProvider.Vertex, true
}

// VertexEnv returns the effective non-secret session env for a Vertex kit:
// CLAUDE_CODE_USE_VERTEX=1 (at-cove-owned) plus every non-protected key from the
// kit's env map. Protected keys are dropped defensively (validation already
// rejected them). Returns nil for a non-Vertex kit. GOOGLE_APPLICATION_CREDENTIALS
// (the ADC file pointer) is set by connect, not here.
func (c Config) VertexEnv() map[string]string {
	v, ok := c.Vertex()
	if !ok {
		return nil
	}
	env := map[string]string{"CLAUDE_CODE_USE_VERTEX": "1"}
	for k, val := range v.Env {
		if vertexProtectedEnvKeys[k] {
			continue
		}
		env[k] = val
	}
	return env
}

// ProviderDomains returns the additive egress domains a model provider needs,
// derived from the provider config, or nil when the kit targets no provider.
// For Vertex: the aiplatform inference host (region-templated), plus the GCP
// auth endpoints google-auth uses to refresh ADC access tokens in-VM.
func ProviderDomains(c Config) []string {
	v, ok := c.Vertex()
	if !ok {
		return nil
	}
	domains := []string{
		"aiplatform.googleapis.com", // default / global inference host
		"oauth2.googleapis.com",     // authorized_user ADC token refresh
		"sts.googleapis.com",        // WIF / external_account
		"iamcredentials.googleapis.com",
	}
	switch region := strings.TrimSpace(v.Env["CLOUD_ML_REGION"]); region {
	case "", "global":
		// aiplatform.googleapis.com already covers the global endpoint.
	case "us", "eu":
		domains = append(domains, "aiplatform."+region+".rep.googleapis.com")
	default:
		domains = append(domains, region+"-aiplatform.googleapis.com")
	}
	return domains
}

// SourceControlDomains returns the additive egress domain a self-hosted source
// control host needs. gitlab.com and github.com are in the sealed base, so only a
// non-default GitLab host is derived here.
func SourceControlDomains(c Config) []string {
	if c.SourceControl == nil || c.SourceControl.GitLab == nil {
		return nil
	}
	host := strings.TrimSpace(c.SourceControl.GitLab.Host)
	if host == "" || host == "gitlab.com" {
		return nil
	}
	return []string{host}
}

// RootDomains is the kit's effective baked egress allow-list: the kit's own
// image.allowed-domains unioned with any provider-derived domains. Assemble bakes
// this into allowed_domains.kit.txt.
func RootDomains(c Config) []string {
	return unionDomains(c.Image.AllowedDomains, ProviderDomains(c), SourceControlDomains(c))
}

// validateModelProvider enforces the provider union, required keys, and the
// hardening denylist.
func validateModelProvider(cfg Config) error {
	if cfg.ModelProvider == nil {
		return nil
	}
	v := cfg.ModelProvider.Vertex
	if v == nil {
		return fmt.Errorf("config.yml: model-provider: must set exactly one provider (vertex)")
	}
	for _, req := range vertexRequiredEnvKeys {
		if strings.TrimSpace(v.Env[req]) == "" {
			return fmt.Errorf("config.yml: model-provider.vertex.env.%s is required", req)
		}
	}
	for k := range v.Env {
		if vertexProtectedEnvKeys[k] {
			return fmt.Errorf("config.yml: model-provider.vertex.env: %q is a sealed-owned/security-relevant variable and cannot be set by a kit", k)
		}
	}
	return nil
}
