package jam

import (
	"errors"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/kittree"
)

// ErrNotFound reports that a named record (e.g. a kit) does not exist.
var ErrNotFound = errors.New("not found")

// Store records enrolled actors (by token hash), roles (by project+name), and the
// destination table, and the model-spec table.
type Store interface {
	RegistryStore
	StandingSessionStore
	ChannelStore
	ShareGrantStore
	PushSubscriptionStore
	SetupSnapshotStore

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
	// RenameProject renames a live project (by name or id): one record
	// changes, as everything refers to it by id. ErrProjectExists for a taken
	// name; the default project is never renamed.
	RenameProject(ref, name string) error
	ListProjects() []string

	// PushKit records config as the next version: a one-file tree (kit.yml),
	// without the push-time checks — seeding and tests; operator pushes go
	// through jam.PushKitFiles.
	PushKit(name, config string) (int, error)
	// PushKitVersion records tree as the kit's next version (current), with
	// meta and, when track is non-nil, the tracked source. objs are written
	// in the same transaction; tree must be among them or already stored, and
	// a tree whose blobs are neither, or a blob that is not its sha256, refuses
	// the call with nothing written.
	PushKitVersion(name, tree string, objs KitObjects, meta KitVersionMeta, track *KitSource) (int, error)
	// SetKitSource sets (or, when nil, clears) a kit's tracked source;
	// ErrNotFound for an unknown kit.
	SetKitSource(name string, src *KitSource) error
	// PutKitDocs replaces each named kit's whole doc (versions, meta, source,
	// current) in one atomic step: every kit is written or none is. An empty
	// name refuses the whole call. The store does not enforce version
	// immutability; the caller (jam) owns that, building each doc from copyKit
	// of the current one plus new versions. objs (the trees and blobs the
	// docs reference) are written in the same step; a doc naming a tree that
	// is neither written nor stored refuses the call.
	PutKitDocs(kits []Kit, objs KitObjects) error
	GetKit(name string) (Kit, bool)
	// KitTree and KitBlob read a stored tree or blob by sha256 (copies).
	KitTree(sha string) (kittree.Tree, bool)
	KitBlob(sha string) ([]byte, bool)
	// GCKitObjects deletes the trees no kit version references and the blobs
	// no remaining tree names, reporting how many of each went.
	GCKitObjects() (KitGC, error)
	PinKit(name string, version int) error
	ListKits() []Kit
	RemoveKit(name string) error
	RoleReferencingKit(name string) (project, role string, ok bool)

	PutInstance(i Instance) error
	GetInstance(actorID string) (Instance, bool)
	ListInstances() []Instance
	RemoveInstance(actorID string) error
	AdvanceCommitCursor(actorID, upToID string, upToSeq int64) (Instance, error) // monotonic forward on upToSeq; no-op if upToSeq <= current CommitSeq; error if actor absent

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
	// configSnapshot is ExportConfig sharing the stored blob slices: the
	// read-only input of in-process scratch state (planner, drift, export
	// tree), never handed out.
	configSnapshot() ConfigSnapshot
	// ImportConfig restores a snapshot into an EMPTY store, fail-closed: it
	// returns ErrConfigNotEmpty (writing nothing) if any config aggregate has
	// entries, or ErrUnsupportedConfigVersion for a bad version.
	ImportConfig(s ConfigSnapshot) error

	GetProject(name string) (Project, bool)
	SetEscalationPolicy(project, category string, tiers []EscalationTier) error
	// SetChatService sets project's chat service to a connection, by id or
	// name, or by chat kind for its implicit connection ("" clears).
	SetChatService(project, ref string) error
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

	// ListConfigMarks lists the config-in-git managed marks, sorted by kind,
	// then name. ApplyConfig writes one config apply's whole set: every
	// record or none.
	ListConfigMarks() []ConfigMark
	ApplyConfig(w ConfigWrite) error
	// GetConfigSource / SetConfigSource read and replace the Jam's tracked
	// config source (nil clears). ApplyConfig also writes it, with the apply
	// (ConfigWrite.ConfigSource).
	GetConfigSource() (ConfigSource, bool)
	SetConfigSource(src *ConfigSource) error
}
