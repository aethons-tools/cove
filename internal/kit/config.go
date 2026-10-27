// Package kit holds the kit-facing types Jam shares across packages: a studio
// kit's secret demands (SecretConfig, IsReservedSecretName), its MCP servers
// (mcp.go), and the tracker blocks Jam's runtime.requisitioner reads
// (LinearTracker, GitHubTracker, StateMap).
package kit

// SecretConfig is a kit's *demand* for a secret (keyed by its env var name in a
// secrets map): a name plus a human description of its purpose. How the value is
// produced is a machine-side concern (see internal/usersecret) — a kit never
// carries a resolver command.
type SecretConfig struct {
	Description string `yaml:"description" json:"description"`
}

// LinearTracker wires the Requisitioner to one Linear team.
type LinearTracker struct {
	Team             string `yaml:"team"`
	PollInterval     string `yaml:"poll-interval"`
	ClassLabelPrefix string `yaml:"class-label-prefix"`
	// DispatchLabelPrefix gates which ready issues the Requisitioner
	// raises a cove for: only issues carrying a label with this prefix. Defaults
	// to "dispatch:" (presence-only; the value after the prefix is unused).
	DispatchLabelPrefix string   `yaml:"dispatch-label-prefix"`
	States              StateMap `yaml:"states"`
	// Secrets is still parsed so an existing serve config keeps its shape; Jam
	// does not read it (the tracker token comes from the requisitioner's connection).
	Secrets map[string]SecretConfig `yaml:"secrets"`
}

// GitHubTracker wires the Requisitioner to one GitHub repo's Issues. Unlike
// Linear, Done is not a lifecycle role — a closed issue IS done — so States
// reuses StateMap but only the five non-terminal roles (ready, in-progress,
// in-review, needs-input, blocked) are required; States.Done is ignored.
type GitHubTracker struct {
	Repo             string `yaml:"repo,omitempty"` // "owner/name"; required (githubissues.NewJam validates it)
	PollInterval     string `yaml:"poll-interval"`
	ClassLabelPrefix string `yaml:"class-label-prefix"`
	// DispatchLabelPrefix gates which ready issues the Requisitioner raises a
	// studio for (presence-only; default "dispatch:", applied by
	// githubissues.NewJam).
	DispatchLabelPrefix string   `yaml:"dispatch-label-prefix"`
	States              StateMap `yaml:"states"`
	// Secrets is still parsed so an existing serve config keeps its shape; Jam
	// does not read it (the tracker token comes from the requisitioner's connection).
	Secrets map[string]SecretConfig `yaml:"secrets"`
}

// StateMap binds the lifecycle roles to a tracker's real state names (Linear)
// or status labels (GitHub).
type StateMap struct {
	Ready      string `yaml:"ready"`
	InProgress string `yaml:"in-progress"`
	InReview   string `yaml:"in-review"`
	Done       string `yaml:"done"`
	NeedsInput string `yaml:"needs-input"`
	Blocked    string `yaml:"blocked"`
}

// reservedSecretNames are well-known subsystem secret names a studio kit may
// not declare as a general secret demand (internal/studio rejects them).
var reservedSecretNames = map[string]bool{
	"GOOGLE_APPLICATION_CREDENTIALS_JSON": true,
}

// IsReservedSecretName reports whether n is a reserved subsystem secret name.
// Exported so other packages (internal/studio build-arg validation) can reject
// collisions without duplicating the set.
func IsReservedSecretName(n string) bool { return reservedSecretNames[n] }
