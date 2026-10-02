package jam

// Store records enrolled actors (by token hash), roles (by project+name), and the
// destination table.
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
	// ExportConfig snapshots the config aggregates (actors, roles, kits,
	// destinations, projects); it never reads instances or unread cursors.
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
}
