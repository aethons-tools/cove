package jam

import "github.com/aethons-tools/cove/internal/jam/sessionctx"

// Store records enrolled actors (by token hash), roles (by project+name), and the
// destination table, and the model-spec table.
type Store interface {
	AddActor(a Actor) error // error if the id already exists or a grant names an unknown project
	Lookup(tokenHash string) (Actor, bool)
	RemoveActor(id string) error
	ListActors() []Actor

	AddGrant(actorID string, g Grant) error // upsert by (project,role); error if actor or project absent
	RemoveGrant(actorID, project, role string) error

	PutRole(project string, r Role) error // upsert; ErrProjectNotFound for an unknown project
	GetRole(project, name string) (Role, bool)
	RemoveRole(project, name string) error
	ListRoles(project string) []Role

	// CreateProject records a new, empty project (ErrProjectExists if present).
	// RemoveProject deletes one, refusing with ErrProjectInUse while a role or
	// grant references it. ListProjects lists the records. Project-scoped
	// writes (roles, grants, roster, escalation, chat service) require the
	// project to exist, except DefaultProject, which they materialize.
	CreateProject(name string) error
	RemoveProject(name string) error
	ListProjects() []string

	PushKit(name, config string) (int, error)
	GetKit(name string) (Kit, bool)
	KitConfig(name string, version int) (string, bool)
	PinKit(name string, version int) error
	ListKits() []Kit
	RemoveKit(name string) error
	RoleReferencingKit(name string) (project, role string, ok bool)

	PutInstance(i Instance) error
	GetInstance(actorID string) (Instance, bool)
	ListInstances() []Instance
	RemoveInstance(actorID string) error
	AdvanceCommitCursor(actorID, upToID string, upToSeq int64) (Instance, error) // monotonic forward on upToSeq; no-op if upToSeq <= current CommitSeq; error if actor absent

	// CommitUnread advances the intercom-UI unread cursor for (participant,
	// channel) to seq. Monotonic forward-only: a backward/equal seq is a no-op
	// success. participant and channel are free-form (no backing entity); both
	// must be non-empty. UnreadCursor reads one pair; UnreadCursors reads all of
	// a participant's channel cursors (the map ProjectChannels consumes).
	CommitUnread(participant, channel string, seq int64) error
	UnreadCursor(participant, channel string) (int64, bool)
	UnreadCursors(participant string) map[string]int64

	AddDestination(d Destination) error
	RemoveDestination(name string) error
	ListDestinations() []Destination
	Match(reqPath string) (Destination, bool)
	// PutModelSpec upserts a model-spec by name (validation is the caller's:
	// see ValidateModelSpec); GetModelSpec / ListModelSpecs return copies, the
	// list sorted by name; RemoveModelSpec errors for an absent name and
	// refuses with ErrModelSpecInUse while a role resolves to it.
	PutModelSpec(m ModelSpec) error
	GetModelSpec(name string) (ModelSpec, bool)
	// PrincipalHeaderRules returns a copy of the named model-spec's
	// principal.headers and its claude provider ("" without a claude body) —
	// the broker's per-request read, without copying the whole spec.
	PrincipalHeaderRules(name string) (provider string, rules []ModelHeaderRule, ok bool)
	ListModelSpecs() []ModelSpec
	RemoveModelSpec(name string) error
	// ExportConfig snapshots the config aggregates (actors, roles, kits,
	// destinations, model-specs, projects); it never reads instances or unread cursors.
	ExportConfig() ConfigSnapshot
	// ImportConfig restores a snapshot into an EMPTY store, fail-closed: it
	// returns ErrConfigNotEmpty (writing nothing) if any config aggregate has
	// entries, or ErrUnsupportedConfigVersion for a bad version.
	ImportConfig(s ConfigSnapshot) error

	AddHuman(project string, h Human) error // upsert by name
	AddChannel(project string, c Channel) error
	RemoveHuman(project, name string) error
	RemoveChannel(project, name string) error
	GetProject(name string) (Project, bool)
	GetRoster(project string) (Roster, bool)
	SetEscalationPolicy(project, category string, tiers []EscalationTier) error
	SetChatService(project, service string) error
	// SetProjectContext replaces a project's authored session context and
	// resources (ErrProjectNotFound for an unknown project).
	SetProjectContext(project string, l sessionctx.Layer, rs []sessionctx.Resource) error
	// GetJamContext / SetJamContext read and replace the Jam-wide authored
	// session-context layer; an empty layer clears it.
	GetJamContext() sessionctx.Layer
	SetJamContext(l sessionctx.Layer) error
	// ModelSpecSchema / SetModelSpecSchema read and record which one-time
	// model-spec store migrations have run (ModelSpecSchemaVersion; 0 = none,
	// a store written before COV-242). See MigrateModelSpecs.
	ModelSpecSchema() int
	SetModelSpecSchema(v int) error
}
