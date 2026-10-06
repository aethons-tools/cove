// Package standing is Jam's standing-session reconciler: a resident loop that
// keeps exactly one live cove per standing session declared on a role
// (RoleAllocation.Standing). A declared name with no cove is granted and raised;
// one whose cove died is raised again as the same session (the standing-session
// map; a new declaration starts a new one), and resumes:
// its /agent-data and workspace volumes survive and cove-master continues the
// prior conversation (COV-249); a cove whose name is no longer declared, or
// whose role is gone, is torn down. Persisted state is purged level-triggered:
// every pass sweeps the state of names no longer declared (once no cove uses
// it), and ResetStanding purges a declared name's state, holding it back from
// being raised until that's done. A queued upgrade (QueueUpgrade, COV-251)
// re-raises a declared name on the current image, keeping its state: each pass
// prepares the image first, waits for the session to be idle, then tears it
// down and raises it again. A name whose raise keeps failing backs off
// exponentially. It lives outside internal/jam core (Jam must not import
// it) and is wired from cmd/at-jam whenever Jam serves.
package standing

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/allocator"
	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// Roster reads the declarations and keeps the standing-session map — which
// session each declaration currently is (satisfied by jam.Store).
type Roster interface {
	ListProjects() []string
	ListRoles(project string) []jam.Role
	LookupName(k ident.Kind, name string) (ident.ID, bool)
	StandingSessionID(project ident.ID, role, name string) (string, bool)
	PutStandingSession(project ident.ID, role, name, sessionID string) error
	RemoveStandingSession(project ident.ID, role, name string) error
	ListStandingSessions() []jam.StandingSessionRef
}

// Registry reads the durable Instance registry (satisfied by jam.Store).
type Registry interface {
	ListInstances() []jam.Instance
}

// Granter is Jam's capacity authority (satisfied by *allocator.Allocator).
type Granter interface {
	Grant(ctx context.Context, req allocator.Request) (bool, error)
	RecordRelease(ctx context.Context, project, role, reservationID string) error
}

// Supervisor raises and tears down coves and manages their persisted state
// (satisfied by *jam.Supervisor). Teardown records the reservation release
// itself and never touches state; PurgeState refuses state still in use.
type Supervisor interface {
	Raise(ctx context.Context, spec jam.RaiseSpec) (jam.Instance, string, string, error)
	Teardown(ctx context.Context, actorID string) error
	PurgeState(ctx context.Context, actorID string) error
	StateOwners(ctx context.Context) ([]string, error)
	// CurrentImage is what a raise for the role would run now (cheap, cached).
	CurrentImage(project, role string) (jam.CurrentImage, error)
	// PrepareImage makes that image ready; it may build for minutes, so the
	// reconciler runs it off its lock.
	PrepareImage(ctx context.Context, project, role string) (jam.CurrentImage, jam.KitStatus, error)
}

// Actors is the actor store, used to clear a leftover identity. A crash between
// Raise enrolling a standing cove's actor and recording its instance leaves an
// actor with the standing id and no cove; every later Raise would then fail
// "already exists". Satisfied by jam.Store.
type Actors interface {
	ListActors() []jam.Actor
	RemoveActor(id string) error
}

// Backoff bounds for a name whose raise fails: the first retry waits
// BackoffInitial, each further failure doubles it, up to BackoffMax.
const (
	DefaultInterval = 30 * time.Second
	BackoffInitial  = 30 * time.Second
	BackoffMax      = 30 * time.Minute
)

// backoff is one name's raise-failure state (in memory; a restart retries at once).
type backoff struct {
	fails int
	next  time.Time
}

// Reconciler keeps the declared standing sessions running.
type Reconciler struct {
	roster   Roster
	registry Registry
	granter  Granter
	sup      Supervisor
	actors   Actors // optional; nil ⇒ leftover identities are not cleared
	interval time.Duration
	now      func() time.Time
	log      *slog.Logger
	// mu serializes Tick and ResetStanding, so a reset can't interleave with
	// an ensure that would re-raise the name on its old state.
	mu      sync.Mutex
	backoff map[string]backoff // actor id → raise-failure backoff
	// stMu guards resetting and upgrades, which the admin API reads and queues
	// into without waiting on mu (a pass may hold it across raises). Writers of
	// resetting hold both; every access to upgrades holds stMu.
	stMu sync.Mutex
	// resetting holds the actor ids whose reset is pending (teardown or purge
	// failed): ensure won't raise them; each Tick retries. In memory only.
	resetting map[string]bool
	// resetKeys is each pending reset's declaration, whose map entry the
	// reset drops once done (the next raise starts a new session).
	resetKeys map[string]declKey
	// upgrades holds the pending upgrades by actor id. In memory only: a Jam
	// restart drops them.
	upgrades map[string]*upgrade
	// kick asks Run for a pass now (a prepare finished, an upgrade queued).
	kick chan struct{}
	// spawn runs a prepare off the lock (go f(); tests run it inline).
	spawn func(f func())
}

// upgrade is one name's pending upgrade.
type upgrade struct {
	project, role, name string
	force               bool
	state, detail       string // jam.Upgrade* state and why
	readyKey            string // the image key PrepareImage last reported ready
	preparing           bool   // a PrepareImage is in flight
	prepErr             string // the last prepare's failure
}

func (u *upgrade) status() string {
	if u.detail == "" {
		return u.state
	}
	return u.state + ": " + u.detail
}

// New builds a Reconciler that ticks every interval (DefaultInterval if <= 0).
// log may be nil.
func New(roster Roster, registry Registry, granter Granter, sup Supervisor, interval time.Duration, log *slog.Logger) *Reconciler {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Reconciler{
		roster: roster, registry: registry, granter: granter, sup: sup,
		interval: interval, now: time.Now, log: log, backoff: map[string]backoff{}, resetting: map[string]bool{}, resetKeys: map[string]declKey{},
		upgrades: map[string]*upgrade{}, kick: make(chan struct{}, 1), spawn: func(f func()) { go f() },
	}
}

// SetActors lets the reconciler clear a leftover identity for a declared name
// that has no live cove (see Actors).
func (r *Reconciler) SetActors(a Actors) { r.actors = a }

// Run reconciles until ctx is cancelled: an immediate tick, then every interval
// (mirrors Supervisor.Run).
func (r *Reconciler) Run(ctx context.Context) {
	r.Tick(ctx)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Tick(ctx)
		case <-r.kick:
			r.Tick(ctx)
		}
	}
}

// declKey identifies one declaration; it is matched against an Instance's
// (Project, Role, Name).
type declKey struct{ project, role, name string }

// sessionID is the session the declaration (project, role, name) currently
// is, from the standing-session map; when it has none and mint is set, a new
// session is started (minted and recorded). Restarts and upgrades keep the
// entry; a reset drops it.
func (r *Reconciler) sessionID(project, role, name string, mint bool) (string, error) {
	pid, ok := r.roster.LookupName(ident.Project, project)
	if !ok {
		return "", fmt.Errorf("project %q has no id", project)
	}
	if id, ok := r.roster.StandingSessionID(pid, role, name); ok {
		return id, nil
	}
	if !mint {
		return "", nil
	}
	id := string(ident.New(ident.Session))
	if err := r.roster.PutStandingSession(pid, role, name, id); err != nil {
		return "", fmt.Errorf("recording the new session: %w", err)
	}
	r.log.Info("standing: new session", "id", id, "project", project, "role", role, "name", name)
	return id, nil
}

// Tick runs one reconcile pass: finish pending resets, advance pending
// upgrades, ensure a cove for every declared name, tear down standing coves
// that are no longer declared, then sweep the persisted state of names no
// longer declared.
func (r *Reconciler) Tick(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.resetting {
		if err := r.finishReset(ctx, id); err != nil {
			r.log.Warn("standing: reset still pending", "id", id, "err", err.Error())
		}
	}
	r.advanceUpgrades(ctx)

	byID := map[string]jam.Instance{}
	for _, inst := range r.registry.ListInstances() {
		if inst.Phase != jam.PhaseGone {
			byID[inst.ActorID] = inst
		}
	}

	declared := map[declKey]bool{}
	declaredIDs := map[string]bool{}
	pids := map[ident.ID]string{} // project id → name, for the map's entries
	for _, project := range r.roster.ListProjects() {
		if pid, ok := r.roster.LookupName(ident.Project, project); ok {
			pids[pid] = project
		}
		for _, role := range r.roster.ListRoles(project) {
			for _, s := range role.Allocation.Standing {
				declared[declKey{project, role.Name, s.Name}] = true
				id, err := r.sessionID(project, role.Name, s.Name, true)
				if err != nil {
					r.log.Warn("standing: no session for declaration", "project", project, "role", role.Name, "name", s.Name, "err", err.Error())
					continue
				}
				declaredIDs[id] = true
				_ = r.ensure(ctx, id, project, role.Name, s, byID) // logged inside; retried next pass
			}
		}
	}
	// A dismissed declaration's entry goes; its session ends (its state is
	// swept below once its cove is gone).
	for _, e := range r.roster.ListStandingSessions() {
		if declared[declKey{pids[e.ProjectID], e.Role, e.Name}] || r.resetting[e.SessionID] {
			continue
		}
		if err := r.roster.RemoveStandingSession(e.ProjectID, e.Role, e.Name); err != nil {
			r.log.Warn("standing: dropping a dismissed session's entry failed", "id", e.SessionID, "err", err.Error())
		}
	}

	for _, inst := range byID {
		if inst.SessionKind != jam.SessionKindStanding {
			continue // ephemeral and personal coves are not ours
		}
		if declared[declKey{inst.Project, inst.Role, inst.Name}] {
			continue
		}
		if err := r.sup.Teardown(ctx, inst.ActorID); err != nil {
			r.log.Warn("standing: teardown of dismissed session failed", "id", inst.ActorID, "err", err.Error())
			continue
		}
		delete(r.backoff, inst.ActorID)
		r.log.Info("standing: dismissed session torn down", "id", inst.ActorID, "project", inst.Project, "role", inst.Role, "name", inst.Name)
	}

	r.sweep(ctx, declaredIDs)
}

// sweep purges the persisted state of every actor id not declared on any role:
// a dismissed name (even one dismissed while down), a removed role, a purge
// that failed before. State still in use (its cove not yet gone) is left for
// a later pass.
func (r *Reconciler) sweep(ctx context.Context, declaredIDs map[string]bool) {
	owners, err := r.sup.StateOwners(ctx)
	if err != nil {
		r.log.Warn("standing: listing persisted state failed", "err", err.Error())
		return
	}
	for _, id := range owners {
		if declaredIDs[id] {
			continue
		}
		if err := r.sup.PurgeState(ctx, id); err != nil {
			r.log.Info("standing: undeclared session's state not purged yet; retrying next pass", "id", id, "err", err.Error())
			continue
		}
		r.log.Info("standing: undeclared session's state purged", "id", id)
	}
}

// ResetStanding resets the declared standing session name of (project, role):
// its cove is torn down and its persisted state purged, the declaration kept,
// so the next pass raises it fresh. It clears the name's backoff. Serialized
// with Tick; until the purge succeeds the name is held back from raising and
// each Tick retries — an error here means the reset is pending, not lost. The
// caller (jam.ResetStanding) has checked the name is declared.
func (r *Reconciler) ResetStanding(ctx context.Context, project, role, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, err := r.sessionID(project, role, name, false)
	if err != nil {
		return err
	}
	if id == "" {
		return nil // never started: nothing to reset
	}
	delete(r.backoff, id)
	r.stMu.Lock()
	r.resetting[id] = true
	r.resetKeys[id] = declKey{project, role, name}
	delete(r.upgrades, id) // the reset raises it fresh, on the current image
	r.stMu.Unlock()
	r.log.Info("standing: reset requested", "id", id, "project", project, "role", role, "name", name)
	return r.finishReset(ctx, id)
}

// QueueUpgrade records a pending upgrade of the declared standing session
// name of (project, role) (COV-251): later passes re-raise it on the current
// image, keeping its state (see advanceUpgrade). It only records intent and
// never waits on a pass. A re-queue keeps the one upgrade, OR-ing force. Errors
// wrap jam.ErrStandingNotDeclared or jam.ErrStandingResetPending.
func (r *Reconciler) QueueUpgrade(project, role, name string, force bool) error {
	if _, ok := r.declaration(project, role, name); !ok {
		return fmt.Errorf("%w: %q on role %s/%s", jam.ErrStandingNotDeclared, name, project, role)
	}
	id, err := r.sessionID(project, role, name, true)
	if err != nil {
		return err
	}
	r.stMu.Lock()
	if r.resetting[id] {
		r.stMu.Unlock()
		return fmt.Errorf("%w: %s", jam.ErrStandingResetPending, id)
	}
	if u, ok := r.upgrades[id]; ok {
		u.force = u.force || force
	} else {
		r.upgrades[id] = &upgrade{project: project, role: role, name: name, force: force, state: jam.UpgradeQueued}
	}
	r.stMu.Unlock()
	r.log.Info("standing: upgrade queued", "id", id, "force", force)
	r.kickNow()
	return nil
}

// UpgradeState is the name's pending upgrade state ("" none); see
// jam.StandingUpgrader.
func (r *Reconciler) UpgradeState(project, role, name string) string {
	id, _ := r.sessionID(project, role, name, false)
	r.stMu.Lock()
	defer r.stMu.Unlock()
	if u, ok := r.upgrades[id]; ok && id != "" {
		return u.status()
	}
	return ""
}

// kickNow asks Run for a pass now, without blocking.
func (r *Reconciler) kickNow() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// setUpgrade records id's pending state and why (if it's still pending).
func (r *Reconciler) setUpgrade(id, state, detail string) {
	r.stMu.Lock()
	defer r.stMu.Unlock()
	if u, ok := r.upgrades[id]; ok {
		u.state, u.detail = state, detail
	}
}

// advanceUpgrades moves every pending upgrade along. Caller holds mu.
func (r *Reconciler) advanceUpgrades(ctx context.Context) {
	r.stMu.Lock()
	pending := make([]upgrade, 0, len(r.upgrades))
	ids := make([]string, 0, len(r.upgrades))
	for id, u := range r.upgrades {
		ids, pending = append(ids, id), append(pending, *u)
	}
	r.stMu.Unlock()
	if len(ids) == 0 {
		return
	}
	insts := map[string]jam.Instance{}
	for _, inst := range r.registry.ListInstances() {
		if inst.Phase != jam.PhaseGone {
			insts[inst.ActorID] = inst
		}
	}
	for i, id := range ids {
		r.advanceUpgrade(ctx, id, pending[i], insts)
	}
}

// advanceUpgrade takes one pending upgrade (a snapshot u) as far as it can go
// this pass. Caller holds mu.
//
//  1. Prepare first: until the image a raise would run now is ready, start (or
//     await) an off-lock PrepareImage and stay "preparing". Nothing is torn
//     down and the name's backoff is untouched.
//  2. Re-check under the lock, right before teardown: the cove (if any) must be
//     this session's and idle (jam.UpgradeBusy) unless forced; a busy one stays
//     "waiting-for-idle".
//  3. Plain Teardown (state kept); a failure stays "teardown-failed" and the
//     next pass retries. Then clear the backoff and the upgrade, and raise —
//     a raise failure backs the name off as usual.
func (r *Reconciler) advanceUpgrade(ctx context.Context, id string, u upgrade, insts map[string]jam.Instance) {
	s, ok := r.declaration(u.project, u.role, u.name)
	if !ok || r.resetting[id] {
		r.dropUpgrade(id)
		r.log.Info("standing: upgrade dropped (name dismissed or reset)", "id", id)
		return
	}
	cur, err := r.sup.CurrentImage(u.project, u.role)
	if err != nil {
		r.setUpgrade(id, jam.UpgradeError, "resolving the current image: "+err.Error())
		return
	}
	if key := cur.Key(); cur.HasKit && u.readyKey != key {
		if !u.preparing {
			r.startPrepare(ctx, id, u)
		}
		r.stMu.Lock()
		if p, ok := r.upgrades[id]; ok {
			p.state, p.detail = jam.UpgradePreparing, p.prepErr // the latest prepare's outcome
		}
		r.stMu.Unlock()
		return
	}
	if inst, live := insts[id]; live {
		if inst.SessionKind != jam.SessionKindStanding || inst.Project != u.project || inst.Role != u.role || inst.Name != u.name {
			r.setUpgrade(id, jam.UpgradeError, "actor id held by another cove")
			return
		}
		if busy := jam.UpgradeBusy(inst); busy != "" && !u.force {
			r.setUpgrade(id, jam.UpgradeWaitingIdle, busy)
			return
		}
		if err := r.sup.Teardown(ctx, id); err != nil {
			r.log.Warn("standing: upgrade teardown failed; retrying next pass", "id", id, "err", err.Error())
			r.setUpgrade(id, jam.UpgradeTeardownFailed, err.Error())
			return
		}
	}
	delete(r.backoff, id)
	r.dropUpgrade(id)
	if err := r.ensure(ctx, id, u.project, u.role, s, map[string]jam.Instance{}); err != nil {
		r.log.Warn("standing: upgrade re-raise failed", "id", id, "err", err.Error())
		return
	}
	r.log.Info("standing: session upgraded", "id", id, "image", cur.Key())
}

// startPrepare runs PrepareImage for u's role off the lock, recording the
// outcome on id's upgrade and kicking a pass.
func (r *Reconciler) startPrepare(ctx context.Context, id string, u upgrade) {
	r.stMu.Lock()
	if p, ok := r.upgrades[id]; ok {
		p.preparing = true
	}
	r.stMu.Unlock()
	r.spawn(func() {
		cur, st, err := r.sup.PrepareImage(ctx, u.project, u.role)
		r.stMu.Lock()
		if p, ok := r.upgrades[id]; ok {
			p.preparing = false
			switch {
			case err != nil:
				p.prepErr = "last attempt failed: " + err.Error()
			case st.State == jam.KitReady:
				p.readyKey, p.prepErr = cur.Key(), ""
			case st.Err != "":
				p.prepErr = st.Err
			}
		}
		r.stMu.Unlock()
		if err != nil {
			r.log.Warn("standing: upgrade prepare failed; retrying next pass", "id", id, "err", err.Error())
		}
		r.kickNow()
	})
}

func (r *Reconciler) dropUpgrade(id string) {
	r.stMu.Lock()
	delete(r.upgrades, id)
	r.stMu.Unlock()
}

// declaration returns the declared standing session name of (project, role).
func (r *Reconciler) declaration(project, role, name string) (jam.StandingSession, bool) {
	for _, ro := range r.roster.ListRoles(project) {
		if ro.Name != role {
			continue
		}
		for _, s := range ro.Allocation.Standing {
			if s.Name == name {
				return s, true
			}
		}
	}
	return jam.StandingSession{}, false
}

// finishReset tears id's cove down, purges its state and ends the session —
// its map entry goes, so the next pass starts a new one — clearing its reset
// mark on success. Caller holds mu.
func (r *Reconciler) finishReset(ctx context.Context, id string) error {
	if err := r.sup.Teardown(ctx, id); err != nil {
		return fmt.Errorf("teardown: %w", err)
	}
	if err := r.sup.PurgeState(ctx, id); err != nil {
		return fmt.Errorf("purge state: %w", err)
	}
	if k, ok := r.resetKeys[id]; ok {
		if pid, ok := r.roster.LookupName(ident.Project, k.project); ok {
			if cur, ok := r.roster.StandingSessionID(pid, k.role, k.name); ok && cur == id {
				if err := r.roster.RemoveStandingSession(pid, k.role, k.name); err != nil {
					return fmt.Errorf("ending the session: %w", err)
				}
			}
		}
	}
	r.stMu.Lock()
	delete(r.resetting, id)
	delete(r.resetKeys, id)
	r.stMu.Unlock()
	r.log.Info("standing: session reset (ended)", "id", id)
	return nil
}

// ensure makes sure the declared name s of (project, role) has a cove: grant,
// then raise, releasing the grant and backing off if the raise fails. It
// returns nil when the name has a cove (live already, or raised now), else why
// not (Tick only logs it; an upgrade logs its re-raise's).
func (r *Reconciler) ensure(ctx context.Context, id, project, role string, s jam.StandingSession, byID map[string]jam.Instance) error {
	if r.resetting[id] {
		return fmt.Errorf("reset pending") // its old state isn't purged yet: raising would re-attach it
	}
	if inst, ok := byID[id]; ok {
		if inst.SessionKind != jam.SessionKindStanding || inst.Project != project || inst.Role != role || inst.Name != s.Name {
			r.log.Warn("standing: actor id held by another cove; not raising", "id", id, "project", project, "role", role, "name", s.Name)
			return fmt.Errorf("actor id %s held by another cove", id)
		}
		delete(r.backoff, id) // live: nothing to do
		return nil
	}
	now := r.now()
	if b, ok := r.backoff[id]; ok && now.Before(b.next) {
		return fmt.Errorf("backing off until %s", b.next.Format(time.RFC3339))
	}
	// No live cove holds this Jam-owned id, so an actor with it can only be
	// left over from a raise that crashed before recording its instance.
	if r.actors != nil {
		for _, a := range r.actors.ListActors() {
			if a.ID != id {
				continue
			}
			if err := r.actors.RemoveActor(id); err != nil {
				r.log.Warn("standing: removing leftover identity failed", "id", id, "err", err.Error())
				return fmt.Errorf("removing leftover identity: %w", err)
			}
			r.log.Warn("standing: removed leftover identity from an interrupted raise", "id", id)
			break
		}
	}
	granted, err := r.granter.Grant(ctx, allocator.Request{
		Project: project, Role: role, ReservationID: id,
		Kind: allocator.SessionStanding, Name: s.Name,
	})
	if err != nil {
		r.log.Warn("standing: grant failed", "id", id, "err", err.Error())
		return fmt.Errorf("grant: %w", err)
	}
	if !granted {
		r.log.Warn("standing: grant denied", "id", id, "project", project, "role", role, "name", s.Name)
		return fmt.Errorf("grant denied")
	}
	_, _, _, err = r.sup.Raise(ctx, jam.RaiseSpec{
		ActorID: id, Project: project, Role: role, Name: s.Name,
		Prompt: s.Prompt, SessionKind: jam.SessionKindStanding,
	})
	if err != nil {
		// Grant, then raise, then compensate: free the reserved slot.
		if rerr := r.granter.RecordRelease(context.WithoutCancel(ctx), project, role, id); rerr != nil {
			r.log.Warn("standing: compensating release failed", "id", id, "err", rerr.Error())
		}
		b := r.backoff[id]
		b.fails++
		delay := min(BackoffInitial<<min(b.fails-1, 16), BackoffMax)
		b.next = now.Add(delay)
		r.backoff[id] = b
		r.log.Warn("standing: raise failed; backing off", "id", id, "fails", b.fails, "retry_in", delay, "err", err.Error())
		return fmt.Errorf("raise: %w", err)
	}
	delete(r.backoff, id)
	r.log.Info("standing: session raised", "id", id, "project", project, "role", role, "name", s.Name)
	return nil
}
