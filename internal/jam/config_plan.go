package jam

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// The plan actions (spec §3).
const (
	ConfigCreate    = "create"
	ConfigUpdate    = "update"
	ConfigDelete    = "delete"
	ConfigUnchanged = "unchanged"
	ConfigAdopt     = "adopt"
)

// ConfigOp is one object's planned action.
type ConfigOp struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"`
	Drift  bool   `json:"drift,omitempty"`
	Path   string `json:"path,omitempty"`
	Before string `json:"before,omitempty"` // Jam's canonical form (update, delete)
	After  string `json:"after,omitempty"`  // the repo's (create, update)
}

// ConfigPlan is a config tree planned against a Jam: every object's action,
// the live objects the repo does not manage, the refusals (any one blocks
// the apply), the serve-config comparison, and a digest of all of it.
type ConfigPlan struct {
	Ops         []ConfigOp      `json:"ops"`
	Unmanaged   []ConfigKey     `json:"unmanaged,omitempty"`
	Refusals    []string        `json:"refusals,omitempty"`
	ServeConfig ServeConfigDiff `json:"serve_config"`
	Digest      string          `json:"digest"`
	write       ConfigWrite
}

// Counts is the number of ops by action.
func (o ConfigPlan) Counts() map[string]int {
	n := map[string]int{}
	for _, op := range o.Ops {
		n[op.Action]++
	}
	return n
}

// ConfigEnv is what the planner needs from the serve beyond the store.
type ConfigEnv struct {
	CredExists     func(string) bool
	PoolConfigured bool
	Serve          map[string]any // the running serve's portable config; nil = unknown
}

// ConfigPlanInput is one PlanConfig run's input.
type ConfigPlanInput struct {
	Tree      *ConfigTree
	Snapshot  ConfigSnapshot
	Instances []Instance
	Marks     []ConfigMark
	Env       ConfigEnv
	Source    gitsource.Source // {Repo: ConfigDirRepo} for --dir
	Commit    string           // ConfigDirCommit for --dir
	Force     bool
	Now       time.Time
}

// seededConfigObjects are the objects every serve creates for itself: on a
// fresh replica they exist unmanaged, so a differing repo copy is adopted and
// updated instead of refused.
var seededConfigObjects = map[ConfigKey]bool{{ConfigKindModelSpec, DefaultModelSpec}: true}

type planner struct {
	in    ConfigPlanInput
	m     *memState // the planned state: the Jam with the plan's writes applied so far
	store *MemStore // m as a Store (kit resolution, model-spec checks)
	live  map[ConfigKey]configObject
	marks map[ConfigKey]ConfigMark
	repo  map[ConfigKey]bool
	plan  ConfigPlan
	w     ConfigWrite
}

// PlanConfig plans in.Tree against the Jam in in.Snapshot: it classifies
// every repo object (§3), runs each create and update through the admin
// API's rules against a scratch copy of the Jam that accumulates the plan's
// writes (so references resolve against the planned state), and collects the
// one ConfigWrite an apply hands to Store.ApplyConfig. It never writes.
func PlanConfig(in ConfigPlanInput) ConfigPlan {
	if in.Tree == nil {
		in.Tree = &ConfigTree{}
	}
	m := stateFromSnapshot(in.Snapshot, in.Instances)
	live, err := exportObjects(m, nil)
	p := &planner{in: in, m: m, store: &MemStore{memState: m}, live: live, marks: map[ConfigKey]ConfigMark{}, repo: map[ConfigKey]bool{}}
	if err != nil {
		p.refuse("jam: %s", err)
	}
	for _, mk := range in.Marks {
		p.marks[mk.Key()] = mk
	}
	p.plan.Refusals = append(p.plan.Refusals, in.Tree.Refusals...)
	p.planServe()
	p.planUsers()
	p.planConnections()
	p.planDestinations()
	p.planModelSpecs()
	p.planKits()
	p.planProjects()
	p.planRoles()
	p.planJamContext()
	p.planUnmanaged()
	slices.Sort(p.plan.Refusals)
	p.plan.Refusals = slices.Compact(p.plan.Refusals)
	p.plan.write = p.w
	p.plan.Digest = planDigest(p.plan)
	return p.plan
}

func (p *planner) credExists(name string) bool {
	return p.in.Env.CredExists != nil && p.in.Env.CredExists(name)
}

// classify is §3's table for an object the repo holds. ok=false: refused.
func (p *planner) classify(key ConfigKey, repo configObject) (action string, drift, ok bool) {
	p.repo[key] = true
	cur, exists := p.live[key]
	mark, managed := p.marks[key]
	switch {
	case !exists:
		return ConfigCreate, false, true
	case !managed && bytes.Equal(cur.Canon, repo.Canon):
		return ConfigAdopt, false, true
	case !managed && seededConfigObjects[key]:
		return ConfigUpdate, false, true
	case !managed:
		p.refuse("%s exists and is not managed; config write --all it into the repo, or rename", key)
		return "", false, false
	case bytes.Equal(cur.Canon, repo.Canon):
		return ConfigUnchanged, false, true
	}
	drift = cur.Digest() != mark.Digest
	if drift && !p.in.Force {
		p.refuseDrift(key)
		p.op(key, ConfigUpdate, true, cur.Canon, repo.Canon) // the diff shows both sides
		return "", true, false
	}
	return ConfigUpdate, drift, true
}

// refuseDrift refuses a write over a managed object edited in Jam since its
// last apply.
func (p *planner) refuseDrift(key ConfigKey) {
	p.refuse("%s drifted (edited in Jam since config @%s); re-run with --force or config write it first", key, shortCommit(p.marks[key].Commit))
}

// upsert classifies key and, for a create or update, runs write against the
// planned state; a write error is a refusal located at the object's file.
func (p *planner) upsert(key ConfigKey, repo configObject, write func(create bool) error) {
	action, drift, ok := p.classify(key, repo)
	if !ok {
		return
	}
	if action == ConfigCreate || action == ConfigUpdate {
		if err := write(action == ConfigCreate); err != nil {
			p.refuse("%s: %s", key.Path(), err)
			return
		}
	}
	p.record(key, action, drift, repo)
}

// record emits key's op and its new mark.
func (p *planner) record(key ConfigKey, action string, drift bool, repo configObject) {
	var before, after []byte
	if action == ConfigUpdate {
		before = p.live[key].Canon
	}
	if action == ConfigCreate || action == ConfigUpdate {
		after = repo.Canon
	}
	p.op(key, action, drift, before, after)
	p.w.Marks = append(p.w.Marks, ConfigMark{Kind: key.Kind, Name: key.Name, Commit: p.in.Commit, Digest: repo.Digest(), AppliedAt: p.in.Now})
}

func (p *planner) refuse(format string, a ...any) {
	p.plan.Refusals = append(p.plan.Refusals, fmt.Sprintf(format, a...))
}

func (p *planner) op(key ConfigKey, action string, drift bool, before, after []byte) {
	p.plan.Ops = append(p.plan.Ops, ConfigOp{Kind: key.Kind, Name: key.Name, Action: action, Drift: drift,
		Path: key.Path(), Before: string(before), After: string(after)})
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// provenanceCommit is the KitSource.Commit a kit version records: none for a
// --dir apply.
func provenanceCommit(c string) string {
	if c == ConfigDirCommit {
		return ""
	}
	return c
}

func planDigest(p ConfigPlan) string {
	b, _ := json.Marshal(struct {
		Ops       []ConfigOp
		Unmanaged []ConfigKey
		Refusals  []string
	}{p.Ops, p.Unmanaged, p.Refusals})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ---- serve config ----

func (p *planner) planServe() {
	tree := p.in.Tree
	p.plan.Refusals = append(p.plan.Refusals, HostKeyRefusals("jam.yml", tree.Serve)...)
	switch {
	case p.in.Env.Serve == nil:
		p.plan.ServeConfig = ServeConfigDiff{State: "unknown"}
	case tree.Serve == nil:
		p.plan.ServeConfig = ServeConfigDiff{State: "absent"}
	default:
		if keys := DiffServeConfig(PortableServeConfig(tree.Serve), p.in.Env.Serve); len(keys) > 0 {
			p.plan.ServeConfig = ServeConfigDiff{State: "differs", Keys: keys}
		} else {
			p.plan.ServeConfig = ServeConfigDiff{State: "same"}
		}
	}
}

// ---- users, connections, destinations, model specs ----

func (p *planner) planUsers() {
	for _, n := range slices.Sorted(maps.Keys(p.in.Tree.Users)) {
		f := p.in.Tree.Users[n]
		o := newConfigObject(f)
		p.upsert(o.Key, o, func(create bool) error {
			var u User
			var err error
			if create {
				u, err = p.m.prepareCreateUser(User{Name: f.Name, Logins: slices.Clone(f.Logins)})
			} else {
				cur, _ := p.m.liveUserNamed(f.Name)
				u, err = p.m.prepareSetUserLogins(cur.ID, f.Logins)
			}
			if err != nil {
				return err
			}
			p.m.applyPutUser(u)
			p.w.Users = append(p.w.Users, u)
			return nil
		})
	}
}

func (p *planner) planConnections() {
	for _, n := range slices.Sorted(maps.Keys(p.in.Tree.Connections)) {
		f := p.in.Tree.Connections[n]
		o := newConfigObject(f)
		p.upsert(o.Key, o, func(create bool) error {
			var c Connection
			var err error
			if create {
				c, err = p.m.prepareCreateConnection(Connection{Kind: f.Service, Name: f.Name, CredName: f.Cred})
			} else {
				cur, _ := p.m.liveConnectionNamed(f.Name)
				if cur.Kind != f.Service {
					return fmt.Errorf("service cannot change (%s → %s); remove the file, apply, then add it back", cur.Kind, f.Service)
				}
				c, err = p.m.prepareSetConnectionCred(cur.ID, f.Cred)
			}
			if err != nil {
				return err
			}
			p.m.applyPutConnection(c)
			p.w.Connections = append(p.w.Connections, c)
			return nil
		})
	}
}

func (p *planner) planDestinations() {
	for _, n := range slices.Sorted(maps.Keys(p.in.Tree.Destinations)) {
		f := p.in.Tree.Destinations[n]
		o := newConfigObject(f)
		p.upsert(o.Key, o, func(bool) error {
			d := copyDestination(f.Destination)
			if err := ValidateDestination(d, p.credExists); err != nil {
				return err
			}
			p.m.applyPutDestination(d)
			p.w.Destinations = append(p.w.Destinations, d)
			return nil
		})
	}
}

func (p *planner) planModelSpecs() {
	for _, n := range slices.Sorted(maps.Keys(p.in.Tree.ModelSpecs)) {
		f := p.in.Tree.ModelSpecs[n]
		o := newConfigObject(f)
		p.upsert(o.Key, o, func(bool) error {
			if err := ValidateModelSpec(f.spec(), p.credExists, p.in.Env.PoolConfigured); err != nil {
				return err
			}
			c, err := prepareModelSpec(f.spec())
			if err != nil {
				return err
			}
			p.m.applyPutModelSpec(c)
			p.w.ModelSpecs = append(p.w.ModelSpecs, c)
			return nil
		})
	}
}

// ---- kits ----

// planKits resolves the tree's kits as one parents-first set against the
// planned state (path parents pinned to the version their parent ends at),
// classifies each by its stored config, and builds the new docs of the ones
// it creates or updates.
func (p *planner) planKits() {
	kits := p.in.Tree.Kits
	for _, e := range kits {
		p.repo[ConfigKey{ConfigKindKit, e.Name}] = true
	}
	entries, err := resolveKitEntries(p.store, kits, nil)
	if err != nil {
		p.refuse("kits: %s", err)
		return
	}
	var changed []kitSetEntry
	for _, e := range entries {
		key := ConfigKey{ConfigKindKit, e.Name}
		repo := configObject{Key: key, Canon: []byte(e.text)}
		action, drift, ok := p.classify(key, repo)
		if !ok {
			continue
		}
		if action == ConfigCreate || action == ConfigUpdate {
			changed = append(changed, e)
		}
		p.record(key, action, drift, repo)
	}
	docs := buildKitDocs(p.store, changed, gitsource.Source{Repo: p.in.Source.Repo, Ref: p.in.Source.Ref},
		provenanceCommit(p.in.Commit), p.in.Now, false)
	for _, d := range docs {
		p.m.kits[d.Name] = copyKit(d)
	}
	p.w.Kits = docs
}

// ---- projects ----

func (p *planner) planProjects() {
	for _, n := range slices.Sorted(maps.Keys(p.in.Tree.Projects)) {
		f := p.in.Tree.Projects[n]
		o := newConfigObject(f)
		p.upsert(o.Key, o, func(create bool) error { return p.writeProject(f, create) })
	}
}

// resolveTiers turns a file's tiers into stored ones: a user:<name> target
// (no glob) becomes user:<id>.
func (p *planner) resolveTiers(field string, tiers []EscalationTierFile) ([]EscalationTier, error) {
	var out []EscalationTier
	for i, t := range tiers {
		d, err := parseConfigDuration(fmt.Sprintf("%s[%d].timeout", field, i), t.Timeout)
		if err != nil {
			return nil, err
		}
		tier := EscalationTier{Timeout: d}
		for _, tg := range t.Targets {
			if n, ok := strings.CutPrefix(tg, "user:"); ok && !strings.ContainsAny(n, "*?[") {
				u, ok := p.m.liveUserNamed(n)
				if !ok {
					return nil, fmt.Errorf("escalation names user %s, who is not in the repo or Jam", n)
				}
				tg = "user:" + string(u.ID)
			}
			tier.Targets = append(tier.Targets, tg)
		}
		out = append(out, tier)
	}
	return out, nil
}

// writeProject writes the project record, then its members and rooms, to the
// planned state and the write set. The record is applied first so the role
// and member checks after it resolve against it even when a later part fails.
func (p *planner) writeProject(f ProjectFile, create bool) error {
	var proj Project
	if create {
		if err := p.m.checkCreateProject(f.Name); err != nil {
			return err
		}
		proj = newProject(f.Name)
	} else {
		proj = copyProject(p.m.projects[f.Name])
	}
	esc, err := p.resolveTiers("escalation", f.Escalation)
	if err != nil {
		return err
	}
	proj.Escalation = esc
	proj.EscalationByCategory = nil
	for _, c := range slices.Sorted(maps.Keys(f.EscalationByCategory)) {
		tiers, err := p.resolveTiers("escalation-by-category."+c, f.EscalationByCategory[c])
		if err != nil {
			return err
		}
		proj = setEscalation(proj, c, tiers)
	}
	proj.ChatService = ""
	if f.ChatService != "" {
		c, ok := p.m.liveConnectionNamed(f.ChatService)
		if !ok {
			return fmt.Errorf("chat-service connection %s is not in the repo or Jam", f.ChatService)
		}
		if !slices.Contains(ChatKinds, c.Kind) {
			return fmt.Errorf("connection %q is a %s connection; a chat service must be one of %v", c.Name, c.Kind, ChatKinds)
		}
		proj.ChatService = string(c.ID)
	}
	if err := validateProjectContext(f.Context, f.Resources); err != nil {
		return err
	}
	proj = setProjectContext(proj, f.Context, f.Resources)
	p.m.applyPutProject(proj)
	p.w.Projects = append(p.w.Projects, proj)
	if err := p.writeMembers(proj.ID, f.Members); err != nil {
		return err
	}
	return p.writeRooms(proj.ID, f.Rooms)
}

// sameDelivery compares deliveries on service and address only (the bound
// service account, UserID, is not config).
func sameDelivery(a, b []DeliveryProfile) bool {
	return slices.EqualFunc(a, b, func(x, y DeliveryProfile) bool { return x.Service == y.Service && x.Address == y.Address })
}

func (p *planner) writeMembers(pid ident.ID, members []MemberFile) error {
	want := map[ident.ID]bool{}
	for _, mf := range members {
		u, ok := p.m.liveUserNamed(mf.User)
		if !ok {
			return fmt.Errorf("member %s is not a user in the repo or Jam", mf.User)
		}
		want[u.ID] = true
		ms := Membership{ProjectID: pid, UserID: u.ID}
		for _, d := range mf.Delivery {
			ms.Delivery = append(ms.Delivery, DeliveryProfile{Service: d.Service, Address: d.Address})
		}
		ms, err := p.m.preparePutMembership(ms)
		if err != nil {
			return fmt.Errorf("member %s: %w", mf.User, err)
		}
		cur, exists := p.m.members[pid][u.ID]
		if exists && sameDelivery(cur.Delivery, ms.Delivery) {
			continue
		}
		for i, d := range ms.Delivery { // keep a delivery's bound service account
			if j := slices.IndexFunc(cur.Delivery, func(c DeliveryProfile) bool { return c.Service == d.Service && c.Address == d.Address }); j >= 0 {
				ms.Delivery[i].UserID = cur.Delivery[j].UserID
			}
		}
		p.m.applyPutMembership(ms)
		p.w.Memberships = append(p.w.Memberships, ms)
	}
	for _, uid := range slices.Sorted(maps.Keys(p.m.members[pid])) {
		if !want[uid] {
			p.w.RemoveMemberships = append(p.w.RemoveMemberships, Membership{ProjectID: pid, UserID: uid})
			p.m.applyRemoveMember(pid, uid)
		}
	}
	return nil
}

// sameBindings compares binding sets, order-insensitively.
func sameBindings(a, b []Binding) bool {
	key := func(x, y Binding) int {
		return cmp.Or(strings.Compare(string(x.ConnectionID), string(y.ConnectionID)), strings.Compare(x.Ref, y.Ref),
			strings.Compare(string(x.Mode), string(y.Mode)))
	}
	return slices.Equal(slices.SortedFunc(slices.Values(a), key), slices.SortedFunc(slices.Values(b), key))
}

// writeRooms makes pid's live rooms the file's. Bindings are released before
// they are taken, so a room renamed with its binding or a binding moved
// between rooms never holds an ingress binding twice — in the planned state
// or in the write's order (a store may check that per write): a first pass
// archives the rooms the file drops and writes each kept room minus the
// bindings it loses; a second creates and updates.
func (p *planner) writeRooms(pid ident.ID, rooms []RoomFile) error {
	put := func(c Channel) {
		p.m.applyPutChannel(c)
		p.w.Channels = append(p.w.Channels, c)
	}
	bindings := map[string][]Binding{}
	for _, rf := range rooms {
		var bs []Binding
		for _, b := range rf.Bindings {
			c, ok := p.m.liveConnectionNamed(b.Connection)
			if !ok {
				return fmt.Errorf("room %s: connection %s is not in the repo or Jam", rf.Key, b.Connection)
			}
			bs = append(bs, Binding{ConnectionID: c.ID, Ref: b.Ref, Mode: cmp.Or(BindingMode(b.Mode), BindBoth)})
		}
		bindings[rf.Key] = bs
	}
	for _, c := range p.m.ListChannels(pid, SourceRoom) {
		bs, inFile := bindings[c.Key]
		if !inFile {
			ch, err := p.m.prepareArchiveChannel(c.ID)
			if err != nil {
				return fmt.Errorf("room %s: %w", c.Key, err)
			}
			put(ch)
			continue
		}
		kept := slices.DeleteFunc(slices.Clone(c.Bindings), func(b Binding) bool { return !slices.Contains(bs, b) })
		if len(kept) < len(c.Bindings) {
			ch := copyChannel(c)
			ch.Bindings = kept
			put(ch)
		}
	}
	for _, rf := range rooms {
		bs, label := bindings[rf.Key], cmp.Or(rf.Label, rf.Key)
		if cur, ok := p.m.liveChannelByKey(pid, SourceRoom, rf.Key); ok {
			if cur.Label == label && sameBindings(cur.Bindings, bs) {
				continue
			}
			ch := copyChannel(cur)
			if err := p.m.checkBindings(bs, ch.ID); err != nil {
				return fmt.Errorf("room %s: %w", rf.Key, err)
			}
			ch.Label, ch.Bindings = label, bs
			put(ch)
			continue
		}
		if err := ValidateEntityName(rf.Key); err != nil {
			return fmt.Errorf("room %s: %w", rf.Key, err)
		}
		ch, err := p.m.prepareCreateChannel(Channel{ProjectID: pid, Kind: SourceRoom, Key: rf.Key, Label: label, Bindings: bs})
		if err != nil {
			return fmt.Errorf("room %s: %w", rf.Key, err)
		}
		put(ch)
	}
	return nil
}

// ---- roles and the Jam context ----

func (p *planner) planRoles() {
	for _, n := range slices.Sorted(maps.Keys(p.in.Tree.Roles)) {
		f := p.in.Tree.Roles[n]
		o := newConfigObject(f)
		p.upsert(o.Key, o, func(bool) error {
			proj, ok := p.m.projects[f.Project]
			if !ok || proj.Status == StatusRemoved {
				return fmt.Errorf("project %s is not in the repo or Jam", f.Project)
			}
			r, err := f.role()
			if err != nil {
				return err
			}
			if err := validateConfigRole(p.store, &r, p.credExists); err != nil {
				return err
			}
			p.m.applyPutRole(proj.ID, r)
			p.w.Roles = append(p.w.Roles, ConfigRoleWrite{ProjectID: proj.ID, Role: r})
			return nil
		})
	}
}

// validateConfigRole is the admin API's role-write rule set (POST /admin/roles
// plus the egress, standing and context writers), over the planned state.
func validateConfigRole(store Store, r *Role, credExists func(string) bool) error {
	if err := r.Collaborate.Validate(); err != nil {
		return err
	}
	if err := ValidateTurnEnd(r.TurnEnd); err != nil {
		return err
	}
	if r.Kit != "" {
		if _, ok := store.GetKit(r.Kit); !ok {
			return fmt.Errorf("kit %s not found (not in the repo or Jam)", r.Kit)
		}
	}
	if err := checkRoleModelSpec(store, r.ModelSpec); err != nil {
		return err
	}
	if err := ValidateCredentials(r.Scope, credExists); err != nil {
		return err
	}
	if err := sessionctx.ValidateLayer(r.Context, sessionctx.BudgetRole); err != nil {
		return fmt.Errorf("context: %w", err)
	}
	var seen []StandingSession
	for _, s := range r.Allocation.Standing {
		if err := checkStandingDecl(seen, s); err != nil {
			return err
		}
		seen = append(seen, s)
	}
	return nil
}

// planJamContext plans the Jam-wide context.md. An empty one counts as
// absent: no op (a managed one is the deletes').
func (p *planner) planJamContext() {
	l := p.in.Tree.JamContext
	if l == nil || l.Empty() {
		return
	}
	layer := sessionctx.Layer{Core: l.Core, Leaves: slices.Clone(l.Leaves)}
	o := newConfigObject(JamContextFile{Layer: layer})
	p.upsert(o.Key, o, func(bool) error {
		if err := sessionctx.ValidateLayer(layer, sessionctx.BudgetJam); err != nil {
			return err
		}
		p.m.applySetJamContext(layer)
		p.w.JamContext = &layer
		return nil
	})
}

// planUnmanaged lists the live objects the repo neither holds nor manages.
func (p *planner) planUnmanaged() {
	for k := range p.live {
		if _, managed := p.marks[k]; !managed && !p.repo[k] {
			p.plan.Unmanaged = append(p.plan.Unmanaged, k)
		}
	}
	slices.SortFunc(p.plan.Unmanaged, func(a, b ConfigKey) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Name, b.Name))
	})
}
