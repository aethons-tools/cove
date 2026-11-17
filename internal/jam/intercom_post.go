package jam

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/ticketkey"
)

// The intercom's posting rules (intercom slice 2a-2): which channel an
// address names, whether the poster may post there, and who hears it. Each
// channel kind is a source with its own policy; the intercom enforces the
// invariants every source shares — the role's addressing is the ceiling a
// session can't exceed, authorization is checked before existence, and the
// audience is decided here, at post time. The log that records a post (and
// its audience) arrives with the cutover (2b); until then Plan is the whole
// decision, unused by the wire. See
// docs/superpowers/specs/2026-10-06-intercom-slice2-channels-design.md.

// Poster is who posts: a participant id, and for a session its instance (its
// project, ticket and starter) and actor (its grants: the addressing ceiling).
type Poster struct {
	ID      ident.ID
	Session *Instance
	Actor   *Actor
}

// Planned is a post's decision: its channel and who hears it (the channel's
// current members but the author, ended sessions skipped), sorted.
type Planned struct {
	Channel  Channel
	Audience []ident.ID
}

// Source is one channel kind's policy.
type Source interface {
	Kind() SourceKind
	// CanPost reports whether p may post in ch. allowed says the poster's
	// addressing already allows this channel (a session addressing it), which
	// a source may honour for channels the poster isn't a member of.
	CanPost(p Poster, ch Channel, allowed bool) bool
	// CanSee reports whether participant p may read ch's history.
	CanSee(p ident.ID, ch Channel) bool
}

// Intercom is the posting side of the channel model.
type Intercom struct {
	store   Store
	tracker func() (ident.ID, bool) // the connection ticket keys belong to
	lg      intercom.Store          // the channel log posts append to
	tail    func() int64            // the log's tail seq: where new members join
	log     *slog.Logger
	sources map[SourceKind]Source
	onAsks  func(session ident.ID) // SetOnAsksChanged
	now     func() time.Time       // when worker swaps are recorded (work_units.go)
}

// NewIntercom returns the intercom over store, appending to lg. tracker
// names the tracker connection ticket channels are keyed on (ok=false: none,
// so no ticket channels); tail reads the log's tail seq (nil: lg's).
func NewIntercom(store Store, tracker func() (ident.ID, bool), lg intercom.Store, tail func() int64, log *slog.Logger) *Intercom {
	if tail == nil {
		tail = func() int64 { seq, _ := lg.TailSeq(); return seq }
	}
	ic := &Intercom{store: store, tracker: tracker, lg: lg, tail: tail, log: log, now: time.Now}
	ic.sources = map[SourceKind]Source{}
	for _, s := range []Source{chatSource{ic, SourceDM}, chatSource{ic, SourceGroup}, ticketSource{ic}, roomSource{ic}, sessionSource{ic}} {
		ic.sources[s.Kind()] = s
	}
	return ic
}

// ---- writing ----

// Post appends m to the planned channel with the planned audience (m's
// Channel is set from the plan, and its mentions part from its own and its
// body's: withMentions). A person posting in a work log or a room joins it,
// from this post on.
func (ic *Intercom) Post(pl Planned, m intercom.Squawk) (intercom.Squawk, error) {
	m.Channel = pl.Channel.ID
	m.Parts = ic.withMentions(pl.Channel, m)
	m, err := ic.lg.Append(m, pl.Audience)
	if err != nil {
		return intercom.Squawk{}, err
	}
	if joinsByPosting(pl.Channel) && !isSessionID(m.From) && !ic.isMemberOf(pl.Channel, m.From) {
		if err := ic.store.JoinChannel(pl.Channel.ID, m.From, m.Seq); err != nil && ic.log != nil {
			ic.log.Warn("intercom: joining the poster to the channel failed", "channel", string(pl.Channel.ID), "err", err.Error())
		}
	}
	return m, nil
}

// joinsByPosting reports whether a person posting in ch becomes a member: a
// work log or a room.
func joinsByPosting(ch Channel) bool { return ch.Kind == SourceWork || ch.Kind == SourceRoom }

// PostTrusted posts m into ch without asking the source whether its author
// may: relay ingress (already resolved by its binding) and Jam's own notices.
// The audience is still the channel's; an archived channel takes nothing.
func (ic *Intercom) PostTrusted(ch Channel, m intercom.Squawk) (intercom.Squawk, error) {
	if ch.Status != StatusLive {
		return intercom.Squawk{}, fmt.Errorf("%w: channel %s", ErrRemoved, ch.ID)
	}
	return ic.Post(Planned{Channel: ch, Audience: ic.audience(ch, m.From)}, m)
}

// Notify posts a notice from Jam as the session, trusted: into the dm with
// the person who started it (a personal session), else into its unit's log.
// It works from the instance alone, after teardown too, so the starter
// learns why it ended. A session with neither has no one to tell: nothing is
// posted (a zero squawk, no error), and a gone session's notice makes no
// channel it didn't have. id "" gets a "notice:" id (IsNotice).
func (ic *Intercom) Notify(inst Instance, id, body string) (intercom.Squawk, error) {
	if id == "" {
		id = fmt.Sprintf("notice:%s:%d", inst.ActorID, time.Now().UnixNano())
	}
	m := intercom.Squawk{ID: id, From: ident.ID(inst.ActorID), Body: body}
	if ch, ok, err := ic.starterDM(inst); err != nil || ok {
		if err != nil {
			return intercom.Squawk{}, err
		}
		return ic.PostTrusted(ch, m)
	}
	ch, ok, err := ic.ticketChannel(inst, ic.sessionLive(ident.ID(inst.ActorID)))
	if err != nil || !ok {
		return intercom.Squawk{}, err
	}
	return ic.PostTrusted(ch, m)
}

// Reconcile moves serve's conversations over at startup
// (conversations-sessions.adoc#migration): it archives every live session
// channel an older Jam made (members and history stay), gives each work log
// its issue's binding from a room that holds it (moveRoomBindings), and sets
// up every live session (SetUp), so a reply has somewhere to land.
func (ic *Intercom) Reconcile() error {
	var errs []error
	for _, name := range ic.store.ListProjects() {
		p, ok := ic.store.GetProject(name)
		if !ok {
			continue
		}
		for _, ch := range ic.store.ListChannels(p.ID, SourceSession) {
			if err := ic.store.ArchiveChannel(ch.ID); err != nil && !errors.Is(err, ErrRemoved) {
				errs = append(errs, fmt.Errorf("archive %s: %w", ch.ID, err))
			}
		}
		for _, ch := range ic.store.ListChannels(p.ID, SourceWork) {
			if err := ic.takeRoomBinding(ch); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", ch.ID, err))
			}
		}
	}
	for _, inst := range ic.store.ListInstances() {
		if inst.Phase != PhaseGone {
			if err := ic.SetUp(inst); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", inst.ActorID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// ---- sessions' conversations ----

// SetUp gives a session its conversations: on a ticket, it becomes the
// worker of the unit's work log (ownTicketChannel), so the issue's comments
// reach it; a personal session gets its dm with the person who started it;
// and it joins the rooms its role lists (joinRoleRooms). A session has no
// channel of its own.
func (ic *Intercom) SetUp(inst Instance) error {
	if _, _, err := ic.ownTicketChannel(inst); err != nil {
		return err
	}
	if _, _, err := ic.starterDM(inst); err != nil {
		return err
	}
	return ic.joinRoleRooms(inst)
}

// StarterDM is the dm between a personal session and the person who started
// it, created when there is none (ok=false: no starter, or they are no
// longer a member of its project).
func (ic *Intercom) StarterDM(inst Instance) (Channel, bool, error) { return ic.starterDM(inst) }

func (ic *Intercom) starterDM(inst Instance) (Channel, bool, error) {
	starter, ok := ic.starter(inst)
	if !ok {
		return Channel{}, false, nil
	}
	p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok || !ic.projectUser(p.ID, starter) {
		return Channel{}, false, nil
	}
	ch, err := ic.chat(p, []ident.ID{starter, ident.ID(inst.ActorID)})
	return ch, err == nil, err
}

// joinRoleRooms joins inst to each live room its role's scope.rooms lists,
// from the tail, with the listed wake (set again when it changed). A listed
// room that no longer exists is skipped: the config planner refuses one, but
// a room can be archived after its sessions were raised.
func (ic *Intercom) joinRoleRooms(inst Instance) error {
	p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok {
		return nil
	}
	self := ident.ID(inst.ActorID)
	for _, rm := range ic.roleRooms(inst) {
		ch, ok := ic.store.ChannelByKey(p.ID, SourceRoom, rm.Name)
		if !ok || ch.Status != StatusLive {
			continue
		}
		if err := ic.store.JoinChannel(ch.ID, self, ic.tail()); err != nil {
			return err
		}
		if w, _ := ic.wakeOf(ch.ID, self); w != rm.Wake {
			if err := ic.store.SetChannelWake(ch.ID, self, rm.Wake); err != nil {
				return err
			}
		}
	}
	return nil
}

// roleRooms is the scope.rooms of inst's role.
func (ic *Intercom) roleRooms(inst Instance) []RoomMembership {
	r, ok := ic.store.GetRole(orDefaultProject(inst.Project), inst.Role)
	if !ok {
		return nil
	}
	return r.Scope.Rooms
}

// wakeOf is p's current membership's wake mode in ch; ok=false when p is
// not a member.
func (ic *Intercom) wakeOf(ch, p ident.ID) (WakeMode, bool) {
	for _, m := range ic.store.ChannelMembers(ch) {
		if m.ParticipantID == p && !m.Left {
			return m.Wake, true
		}
	}
	return "", false
}

// Ended withdraws an ended session's open asks and takes it out of its
// unit's work log (the unit has no worker until it is dispatched again).
// Its dms stay: Notify still reaches the person who started it.
func (ic *Intercom) Ended(inst Instance) error {
	ic.withdrawAsksOf(ident.ID(inst.ActorID))
	if inst.Unit == "" {
		return nil
	}
	ch, ok, err := ic.ticketChannel(inst, false)
	if err != nil || !ok {
		return err
	}
	if err := ic.store.LeaveChannel(ch.ID, ident.ID(inst.ActorID), ic.tail()); err != nil {
		return err
	}
	if err := ic.store.StopUnitWorker(ch.ID, ident.ID(inst.ActorID), ic.now()); err != nil {
		return err
	}
	if ch.Worker == ident.ID(inst.ActorID) {
		if err := ic.store.SetChannelWorker(ch.ID, ""); err != nil && !errors.Is(err, ErrRemoved) {
			return err
		}
	}
	return nil
}

// ownTicketChannel is the work log of inst's unit, created on demand, with
// inst its worker: a new worker takes the old one's place in the members
// and in the unit's record (a no-op while inst is the worker). A live worker
// raised after inst keeps the unit, and inst is no member. ok=false when
// inst has no ticket on the tracker.
func (ic *Intercom) ownTicketChannel(inst Instance) (Channel, bool, error) {
	if inst.Unit == "" {
		return Channel{}, false, nil
	}
	ch, ok, err := ic.ticketChannel(inst, true)
	if err != nil || !ok {
		return Channel{}, false, err
	}
	self := ident.ID(inst.ActorID)
	if ch.Worker != self && ch.Worker != "" {
		if cur, ok := ic.store.GetInstance(string(ch.Worker)); ok && cur.Phase != PhaseGone && cur.RaisedAt.After(inst.RaisedAt) {
			return ch, true, ic.store.LeaveChannel(ch.ID, self, ic.tail()) // a no-op unless it was a member
		}
		if err := ic.store.LeaveChannel(ch.ID, ch.Worker, ic.tail()); err != nil {
			return Channel{}, false, err
		}
	}
	if ch.Worker != self {
		if err := ic.store.SetChannelWorker(ch.ID, self); err != nil {
			return Channel{}, false, err
		}
		ch.Worker = self
	}
	if err := ic.store.JoinChannel(ch.ID, self, ic.tail()); err != nil {
		return Channel{}, false, err
	}
	w := UnitWorker{Session: self, Label: sessionLabel(inst), Started: ic.now()}
	if err := ic.store.StartUnitWorker(ch.ID, w, UnitIssue{Title: inst.UnitTitle, URL: inst.UnitURL}); err != nil {
		return Channel{}, false, err
	}
	return ch, true, nil
}

// Reported records a session's accepted report on its unit: its state and
// its PR. A session with no unit's log records nothing.
func (ic *Intercom) Reported(inst Instance, r TicketReport) error {
	if inst.Unit == "" {
		return nil
	}
	ch, ok, err := ic.ticketChannel(inst, false)
	if err != nil || !ok {
		return err
	}
	return ic.store.ReportUnit(ch.ID, r.State, r.PR, r.At)
}

// starter is the user who started a personal session (by id, or by name for
// an instance from before owners were recorded by id).
func (ic *Intercom) starter(inst Instance) (ident.ID, bool) {
	if inst.OwnerID != "" {
		return inst.OwnerID, true
	}
	if inst.Owner != "" {
		return ic.store.LookupName(ident.User, inst.Owner)
	}
	return "", false
}

// TicketChannelOf is the existing channel of a session's ticket, without
// creating one (for read-only views).
func (ic *Intercom) TicketChannelOf(inst Instance) (Channel, bool) {
	ch, ok, err := ic.ticketChannel(inst, false)
	return ch, ok && err == nil
}

// ticketChannel finds (or, with create, creates) the channel of inst's
// ticket: keyed "<tracker connection>/<issue key>" in its project, bound to
// the issue for ingress — taking the binding from a room that holds it
// (takeRoomBinding). ok=false when there is no tracker connection, or inst's
// unit is no ticket key on it (a github tracker's unit must be
// owner/repo#<n>; any other, say a manual raise's free text, leaves the
// session with no log).
func (ic *Intercom) ticketChannel(inst Instance, create bool) (Channel, bool, error) {
	conn, ok := ic.tracker()
	if !ok {
		return Channel{}, false, nil
	}
	unit, ok := TicketUnit(ic.store, conn, inst.Unit)
	if !ok {
		return Channel{}, false, nil
	}
	p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok {
		return Channel{}, false, fmt.Errorf("%w: %q", ErrProjectNotFound, inst.Project)
	}
	key := string(conn) + "/" + unit
	if ch, ok := ic.store.ChannelByKey(p.ID, SourceWork, key); ok || !create {
		return ch, ok, nil
	}
	created, err := ic.store.CreateChannel(Channel{ProjectID: p.ID, Kind: SourceWork, Key: key, Label: unit})
	if errors.Is(err, ErrChannelExists) { // created meanwhile
		created, ok = ic.store.ChannelByKey(p.ID, SourceWork, key)
		return created, ok, nil
	}
	if err != nil {
		return Channel{}, false, err
	}
	if err := ic.takeRoomBinding(created); err != nil {
		return Channel{}, false, err
	}
	created, _ = ic.store.GetChannel(created.ID)
	return created, true, nil
}

// takeRoomBinding binds work log ch to its issue for ingress
// (conversations-sessions.adoc#migration): a log with no binding takes it,
// and a room that holds it keeps posting to the issue (BindEgress, as a
// post-only room) while the log gets the issue's replies. The room keeps its
// history. A log that already has a binding, or one keyed on no issue, is
// left as it is.
func (ic *Intercom) takeRoomBinding(ch Channel) error {
	if len(ch.Bindings) > 0 {
		return nil
	}
	c, unit, ok := strings.Cut(ch.Key, "/")
	if !ok || unit == "" {
		return nil
	}
	conn := ident.ID(c)
	b := Binding{ConnectionID: conn, Ref: unit, Mode: BindBoth}
	if holder, taken := ic.store.ChannelByBinding(conn, unit); taken {
		if holder.Kind != SourceRoom {
			if ic.log != nil {
				ic.log.Warn("intercom: work log left unbound; another channel receives the issue's replies", "ticket", unit, "holder", string(holder.ID))
			}
			return nil
		}
		bs := slices.Clone(holder.Bindings)
		for i := range bs {
			if bs[i] == b {
				bs[i].Mode = BindEgress
			}
		}
		if err := ic.store.SetChannelBindings(holder.ID, bs); err != nil {
			return err
		}
		if ic.log != nil {
			ic.log.Info("intercom: the work log takes its issue's replies from a room, which still posts to it", "ticket", unit, "room", holder.Key)
		}
	}
	return ic.store.SetChannelBindings(ch.ID, []Binding{b})
}

// Tracker is the tracker connection ticket channels are keyed on (the one
// serve resolved; ok=false: none).
func (ic *Intercom) Tracker() (ident.ID, bool) { return ic.tracker() }

// TicketUnit is unit as a ticket key on tracker: a github tracker's in its
// canonical spelling, ok=false when unit is no owner/repo#<n> or is on a
// repo other than the tracker's (SetTrackerRepo); any other tracker's as
// given (ok=false only when empty).
func TicketUnit(store Store, tracker ident.ID, unit string) (string, bool) {
	if unit == "" {
		return "", false
	}
	if connKind(store, tracker) != "github" {
		return unit, true
	}
	key, err := ticketkey.Canonical(unit)
	return key, err == nil && onTrackerRepo(tracker, key)
}

// connKind is connection id's kind ("" when unknown).
func connKind(store Store, id ident.ID) string {
	c, _ := store.GetConnection(id)
	return c.Kind
}

// ---- planning a post ----

// Plan decides a session's send to addr ("" = its default conversation,
// defaultConversation): user:<name|id> (human: alias),
// session:<label|id>, and chat:<member>[,<member>…] (user: or session:
// members) → the dm or group with them; channel:<name> →
// a room; work:<key> (ticket:<key> an alias), or
// with <connection>/<key> → a unit of work's log; worker:<key> → the dm with
// the unit's current worker. ErrSendDenied when the session's addressing
// doesn't allow the address (checked first: a 403 never reveals existence),
// ErrSendUnresolved when it does but nothing answers to it (404), ErrNoWorker
// when a worker: unit has no current worker (409). A reply needs no
// addressing: a session may always send to a conversation it was delivered
// a message in (replyChannel).
func (ic *Intercom) Plan(p Poster, addr string, now time.Time) (Planned, error) {
	if p.Session == nil || p.Actor == nil {
		return Planned{}, fmt.Errorf("intercom: Plan needs a session poster")
	}
	if !p.Actor.Expiry.IsZero() && now.After(p.Actor.Expiry) {
		return Planned{}, ErrSendDenied
	}
	if addr == "" {
		ch, err := ic.defaultConversation(p)
		if err != nil {
			return Planned{}, err
		}
		return ic.planIn(p, ch, true)
	}
	project, ok := ic.store.GetProject(orDefaultProject(p.Session.Project))
	if !ok {
		return Planned{}, ErrSendUnresolved
	}
	kind, rest, _ := strings.Cut(addr, ":")
	ch, err := ic.resolve(p, project, kind, rest, ic.ceiling(p, project.Name))
	if errors.Is(err, ErrSendDenied) {
		if reply, ok := ic.replyChannel(p, project, kind, rest); ok {
			ch, err = reply, nil
		}
	}
	if err != nil {
		return Planned{}, err
	}
	return ic.planIn(p, ch, true)
}

// defaultConversation is where a post with no address from a session on an
// older turn protocol goes: its turn end (protocol 2 posts its final text
// with no `to`), an ask, or a whiteboard. It guesses from where messages
// arrived, which can race; a session on the turn-end rule is never given it
// (PlanSend). A session has no channel of its own, so it is
// the live conversation the session was last delivered a message in (what
// it is most likely answering) that it may still post in; else its unit's
// log; else, for a personal session, the dm with the person who started
// it. A session with none of these must name its conversation
// (ErrSendNeedsTo).
func (ic *Intercom) defaultConversation(p Poster) (Channel, error) {
	delivered := ic.lg.InboxChannels(p.ID)
	slices.SortFunc(delivered, func(a, b intercom.ChannelStat) int { return cmp.Compare(b.LastSeq, a.LastSeq) })
	for _, st := range delivered {
		ch, ok := ic.store.GetChannel(st.Channel)
		if !ok || ch.Status != StatusLive {
			continue
		}
		if src, ok := ic.sources[ch.Kind]; ok && src.CanPost(p, ch, false) {
			return ch, nil
		}
	}
	if ch, ok, err := ic.ownTicketChannel(*p.Session); err != nil || ok {
		return ch, err
	}
	if ch, ok, err := ic.starterDM(*p.Session); err != nil || ok {
		return ch, err
	}
	return Channel{}, ErrSendNeedsTo
}

// PlanSend is Plan for a session's send (POST /squawks), ask, or
// whiteboard. Under the turn-end rule (Instance.TurnProtocol 3) a post names
// its conversation — the cove fills a turn's later posts in from its first
// send — so one with no addr is ErrSendNeedsTo. A session on an older turn
// protocol keeps Plan's default conversation (defaultConversation), until
// its studio is upgraded.
func (ic *Intercom) PlanSend(p Poster, addr string, now time.Time) (Planned, error) {
	if addr == "" && p.Session != nil && p.Session.TurnProtocol == sessionctx.TurnProtocolTurnRule {
		return Planned{}, ErrSendNeedsTo
	}
	return ic.Plan(p, addr, now)
}

// resolve finds (creating a dm or group on demand) the channel the address
// kind:rest names in project, as globs allow.
func (ic *Intercom) resolve(p Poster, project Project, kind, rest string, globs []string) (Channel, error) {
	switch kind {
	case "user", "human":
		return ic.resolveChat(p, project, []string{"user:" + rest}, globs)
	case "chat":
		return ic.resolveChat(p, project, strings.Split(rest, ","), globs)
	case "channel":
		return ic.resolveRoom(project, rest, globs)
	case "ticket", "work":
		return ic.resolveTicket(p, project, rest, globs)
	case "worker":
		return ic.resolveWorker(p, project, rest, globs)
	case "session":
		return ic.resolveSession(p, project, rest, globs)
	}
	return Channel{}, ErrSendDenied
}

// anyGlobs allow every address: resolving one regardless of addressing.
var anyGlobs = []string{"*", "session:*"}

// replyChannel is the existing conversation the address kind:rest names,
// when the session was delivered a message in it (ok=false otherwise, or
// when it names none yet): a reply needs no addressing. It creates nothing.
func (ic *Intercom) replyChannel(p Poster, project Project, kind, rest string) (Channel, bool) {
	var ch Channel
	var ok bool
	switch kind {
	case "user", "human", "session", "chat":
		refs := strings.Split(rest, ",")
		if kind != "chat" {
			refs = []string{kind + ":" + rest}
			if kind == "human" {
				refs = []string{"user:" + rest}
			}
		}
		if members, err := ic.chatMembers(p, project, refs, anyGlobs); err == nil {
			ch, ok = ic.existingChat(project, members)
		}
	case "channel":
		ch, ok = ic.store.ChannelByKey(project.ID, SourceRoom, rest)
	case "ticket", "work":
		if !ic.ownUnit(p, rest) {
			ch, ok = ic.lookupTicket(p, project, rest)
		}
	case "worker":
		if log, found := ic.lookupTicket(p, project, rest); found && log.Worker != "" {
			ch, ok = ic.existingChat(project, []ident.ID{p.ID, log.Worker})
		}
	}
	return ch, ok && ic.deliveredIn(p.ID, ch.ID)
}

// ReplyTo is the send address that replies in channel chID for the session
// self ("" when none answers to it): a dm or group chat: its other members,
// a room channel:<name>, a work log work:<key>. An archived session channel
// an older Jam made takes no replies. A reply needs no addressing
// (replyChannel), so the session can always use it.
func (ic *Intercom) ReplyTo(self Instance, chID ident.ID) string {
	ch, ok := ic.store.GetChannel(chID)
	if !ok {
		return ""
	}
	switch ch.Kind {
	case SourceRoom:
		return "channel:" + ch.Key
	case SourceWork:
		conn, unit, _ := strings.Cut(ch.Key, "/")
		if tracker, ok := ic.tracker(); ok && conn == string(tracker) {
			return "work:" + unit
		}
		return "work:" + ch.Key
	case SourceDM, SourceGroup:
		var refs []string
		for _, m := range strings.Split(ch.Key, ",") {
			switch {
			case m == self.ActorID || m == "":
			case participantKind(ident.ID(m)) == string(ident.User):
				refs = append(refs, "user:"+m)
			default:
				refs = append(refs, "session:"+m)
			}
		}
		if len(refs) == 0 {
			return ""
		}
		return "chat:" + strings.Join(refs, ",")
	}
	return ""
}

// lookupTicket is the existing work log ref names, regardless of addressing
// (never one's own unit, which resolveTicket may create).
func (ic *Intercom) lookupTicket(p Poster, project Project, ref string) (Channel, bool) {
	if ic.ownUnit(p, ref) {
		return Channel{}, false
	}
	ch, err := ic.resolveTicket(p, project, ref, anyGlobs)
	return ch, err == nil
}

// ownUnit reports whether ref names the poster's own unit's key on the
// tracker (as written: a scoped or respelled ref is looked up instead).
func (ic *Intercom) ownUnit(p Poster, ref string) bool {
	tracker, ok := ic.tracker()
	if !ok {
		return false
	}
	own, onTicket := TicketUnit(ic.store, tracker, p.Session.Unit)
	key, isKey := TicketUnit(ic.store, tracker, ref)
	return onTicket && isKey && own == key
}

// deliveredIn reports whether participant p was delivered a message in ch.
func (ic *Intercom) deliveredIn(p, ch ident.ID) bool {
	return slices.ContainsFunc(ic.lg.InboxChannels(p), func(st intercom.ChannelStat) bool { return st.Channel == ch })
}

// PlanChannel decides a person's post into an existing channel (replying in
// a conversation): the source's CanPost. A channel that doesn't exist or that
// they may not post in is ErrSendDenied alike, so a refusal never tells
// whether a channel exists. Sessions post by address (Plan), where their
// addressing applies.
func (ic *Intercom) PlanChannel(p Poster, chID ident.ID) (Planned, error) {
	if p.Session != nil || isSessionID(p.ID) {
		return Planned{}, fmt.Errorf("intercom: a session posts by address, not into a channel id")
	}
	ch, ok := ic.store.GetChannel(chID)
	if !ok {
		return Planned{}, ErrSendDenied
	}
	return ic.planIn(p, ch, false)
}

// PlanPersonChat decides a person's new conversation from /me with other
// users: the dm (one other) or the group (several) in a project all of them
// are live members of (the first by name). ErrSendUnresolved when there is
// none.
func (ic *Intercom) PlanPersonChat(from ident.ID, with ...ident.ID) (Planned, error) {
	members := []ident.ID{from}
	for _, w := range with {
		if w.Kind() != ident.User {
			return Planned{}, ErrSendUnresolved
		}
		if !slices.Contains(members, w) {
			members = append(members, w)
		}
	}
	if from.Kind() != ident.User || len(members) < 2 {
		return Planned{}, ErrSendUnresolved
	}
	for _, name := range ic.store.ListProjects() {
		p, ok := ic.store.GetProject(name)
		if !ok || slices.ContainsFunc(members, func(m ident.ID) bool { return !ic.projectUser(p.ID, m) }) {
			continue
		}
		ch, err := ic.chat(p, members)
		if err != nil {
			return Planned{}, err
		}
		return ic.planIn(Poster{ID: from}, ch, false)
	}
	return Planned{}, ErrSendUnresolved
}

// PlanSessionDM decides a person's post to a live session from /me
// (session:<id|label>): the dm between them (conversations.adoc#targets), as
// MayReach allows. A session that doesn't exist or that they may not reach
// is ErrSendDenied alike.
func (ic *Intercom) PlanSessionDM(from ident.ID, ref string) (Planned, error) {
	inst, ok := ic.personSession(from, ref)
	if !ok || !ic.MayReach(from, inst) {
		return Planned{}, ErrSendDenied
	}
	return ic.planSessionDM(from, inst)
}

// planSessionDM is the dm between person from and live session inst, who
// may reach it.
func (ic *Intercom) planSessionDM(from ident.ID, inst Instance) (Planned, error) {
	p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok {
		return Planned{}, ErrSendDenied
	}
	ch, err := ic.chat(p, []ident.ID{from, ident.ID(inst.ActorID)})
	if err != nil {
		return Planned{}, err
	}
	return ic.planIn(Poster{ID: from}, ch, false)
}

// PlanWorkerDM decides a person's post to a unit of work's current worker
// from /me (worker:<key>): the dm between them (conversations.adoc#targets).
// key is the issue key on the tracker, in a project the person is a member
// of. ErrNoWorker when the unit has no live worker (or no log yet: nothing
// is held for a later session); ErrSendDenied when they may not reach it.
func (ic *Intercom) PlanWorkerDM(from ident.ID, key string) (Planned, error) {
	tracker, ok := ic.tracker()
	if !ok || from.Kind() != ident.User {
		return Planned{}, ErrSendDenied
	}
	unit, ok := TicketUnit(ic.store, tracker, key)
	if !ok {
		return Planned{}, ErrSendUnresolved
	}
	member := false
	for _, name := range ic.store.ListProjects() {
		p, ok := ic.store.GetProject(name)
		if !ok || !ic.projectUser(p.ID, from) {
			continue
		}
		member = true
		log, ok := ic.store.ChannelByKey(p.ID, SourceWork, string(tracker)+"/"+unit)
		if !ok {
			continue
		}
		inst, ok := ic.store.GetInstance(string(log.Worker))
		if log.Worker == "" || !ok || inst.Phase == PhaseGone {
			return Planned{}, ErrNoWorker
		}
		if !ic.MayReach(from, inst) {
			return Planned{}, ErrSendDenied
		}
		return ic.planSessionDM(from, inst)
	}
	if !member {
		return Planned{}, ErrSendDenied
	}
	return Planned{}, ErrNoWorker
}

// personSession is the live session ref names for person from: by id, else
// the only one labelled ref in the projects they're a member of.
func (ic *Intercom) personSession(from ident.ID, ref string) (Instance, bool) {
	if from.Kind() != ident.User || ref == "" {
		return Instance{}, false
	}
	if inst, ok := ic.store.GetInstance(ref); ok {
		return inst, inst.Phase != PhaseGone
	}
	var found []Instance
	for _, name := range ic.store.ListProjects() {
		if p, ok := ic.store.GetProject(name); ok && ic.projectUser(p.ID, from) {
			if inst, ok := ic.sessionNamed(p, ref); ok {
				found = append(found, inst)
			}
		}
	}
	if len(found) != 1 {
		return Instance{}, false
	}
	return found[0], true
}

// MayReach reports, without creating anything, whether user u may post to
// live session inst from /me (PlanSessionDM would allow it): a member of its
// project, and for a personal session the person who started it, or one
// already in a dm with it.
func (ic *Intercom) MayReach(u ident.ID, inst Instance) bool {
	p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok || inst.Phase == PhaseGone || !ic.projectUser(p.ID, u) {
		return false
	}
	if !isPersonal(inst) {
		return true
	}
	if starter, ok := ic.starter(inst); ok && starter == u {
		return true
	}
	_, ok = ic.existingChat(p, []ident.ID{u, ident.ID(inst.ActorID)})
	return ok
}

// isPersonal reports whether a session is someone's own (a personal session,
// or one recorded with who started it).
func isPersonal(inst Instance) bool {
	return inst.SessionKind == SessionKindPersonal || inst.OwnerID != "" || inst.Owner != ""
}

// CanSee reports whether participant p may read ch (evaluated live, at read
// time, so revocation is immediate).
func (ic *Intercom) CanSee(p ident.ID, ch Channel) bool {
	src, ok := ic.sources[ch.Kind]
	return ok && src.CanSee(p, ch)
}

func (ic *Intercom) planIn(p Poster, ch Channel, allowed bool) (Planned, error) {
	src, ok := ic.sources[ch.Kind]
	if !ok || !src.CanPost(p, ch, allowed) {
		return Planned{}, ErrSendDenied
	}
	if ch.Status != StatusLive {
		return Planned{}, fmt.Errorf("%w: channel %s", ErrRemoved, ch.ID)
	}
	return Planned{Channel: ch, Audience: ic.audience(ch, p.ID)}, nil
}

// audience is ch's current members but from, skipping sessions that ended
// and users who are no longer live members of the channel's project.
func (ic *Intercom) audience(ch Channel, from ident.ID) []ident.ID {
	var out []ident.ID
	for _, m := range ic.store.ChannelMembers(ch.ID) {
		if m.ParticipantID == from || !ic.reachable(ch, m.ParticipantID) {
			continue
		}
		out = append(out, m.ParticipantID)
	}
	slices.Sort(out)
	return out
}

// reachable reports whether a member still takes part: a session while it
// lives, a user while they are a live member of ch's project (checked live,
// so leaving a project ends their access at once); anyone else (an account)
// as long as they are listed.
func (ic *Intercom) reachable(ch Channel, p ident.ID) bool {
	switch {
	case isSessionID(p):
		return ic.sessionLive(p)
	case p.Kind() == ident.User:
		return ic.projectUser(ch.ProjectID, p)
	}
	return true
}

// projectUser reports whether user u is live and a member of project.
func (ic *Intercom) projectUser(project, u ident.ID) bool {
	usr, ok := ic.store.GetUser(u)
	return ok && usr.Status == StatusLive && ic.store.IsMember(project, u)
}

func (ic *Intercom) sessionLive(id ident.ID) bool {
	inst, ok := ic.store.GetInstance(string(id))
	return ok && inst.Phase != PhaseGone
}

// isSessionID reports whether a participant is a session: a session id, or
// a grandfathered pre-registry one (the only ids that don't parse).
func isSessionID(id ident.ID) bool { return participantKind(id) == string(ident.Session) }

// isMemberOf reports whether p is a current member of ch who still takes
// part (reachable).
func (ic *Intercom) isMemberOf(ch Channel, p ident.ID) bool {
	listed := slices.ContainsFunc(ic.store.ChannelMembers(ch.ID), func(m ChannelMember) bool { return m.ParticipantID == p })
	return listed && ic.reachable(ch, p)
}

// ceiling is the session's addressing in project: the effective addressing
// of each of its grants there (additive).
func (ic *Intercom) ceiling(p Poster, project string) []string {
	var globs []string
	for _, g := range p.Actor.Grants {
		if !SameProject(ic.store, g.Project, project) {
			continue
		}
		if role, ok := ic.store.GetRole(g.Project, g.Role); ok {
			globs = append(globs, EffectiveScope(g, role).Addressing...)
		}
	}
	return globs
}

// resolveChat resolves member targets (user:<name|id>, session:<label|id>)
// the session may address, then the dm or group of them and the session.
func (ic *Intercom) resolveChat(p Poster, project Project, targets []string, globs []string) (Channel, error) {
	members, err := ic.chatMembers(p, project, targets, globs)
	if err != nil {
		return Channel{}, err
	}
	return ic.chat(project, members)
}

// chatMembers resolves member targets to the poster and the members of
// project they name: people who are members, live sessions. Every member is
// checked before any is looked up beyond its address (403 before 404).
func (ic *Intercom) chatMembers(p Poster, project Project, targets []string, globs []string) ([]ident.ID, error) {
	members := []ident.ID{p.ID}
	var missing bool
	for _, t := range targets {
		kind, ref, _ := strings.Cut(t, ":")
		if ref == "" {
			return nil, ErrSendDenied
		}
		var id ident.ID
		var found, allowed bool
		switch kind {
		case "user":
			var u User
			u, found = ic.memberUser(project, ref)
			allowed = found && ic.userAllowed(p, u, globs)
			id = u.ID
		case "session":
			var inst Instance
			inst, found = ic.sessionNamed(project, ref)
			allowed = found && (inst.ActorID == p.Session.ActorID || anyAllowed([]string{"session:" + sessionLabel(inst), "session:" + inst.ActorID}, globs))
			id = ident.ID(inst.ActorID)
		default:
			return nil, ErrSendDenied
		}
		switch {
		case allowed:
			members = append(members, id)
		case !found && anyAllowed([]string{kind + ":" + ref}, globs):
			missing = true
		default:
			return nil, ErrSendDenied
		}
	}
	if missing {
		return nil, ErrSendUnresolved
	}
	return members, nil
}

// userAllowed reports whether the poster may address person u: its
// addressing allows them, or they started the poster's personal session.
func (ic *Intercom) userAllowed(p Poster, u User, globs []string) bool {
	if starter, ok := ic.starter(*p.Session); ok && starter == u.ID {
		return true
	}
	return anyAllowed([]string{"user:" + u.Name, "user:" + string(u.ID)}, globs)
}

// memberUser is the live user ref names (a name or a usr_ id) who is a
// member of project.
func (ic *Intercom) memberUser(project Project, ref string) (User, bool) {
	id, err := ident.Parse(ref)
	if err != nil || id.Kind() != ident.User {
		var ok bool
		if id, ok = ic.store.LookupName(ident.User, ref); !ok {
			return User{}, false
		}
	}
	u, ok := ic.store.GetUser(id)
	if !ok || u.Status != StatusLive || !ic.store.IsMember(project.ID, u.ID) {
		return User{}, false
	}
	return u, true
}

// chatKey is the key and kind of the dm or group of exactly members, and
// the members sorted and without repeats.
func chatKey(members []ident.ID) (string, SourceKind, []ident.ID) {
	members = slices.Clone(members)
	slices.Sort(members)
	members = slices.Compact(members)
	parts := make([]string, len(members))
	for i, m := range members {
		parts[i] = string(m)
	}
	return strings.Join(parts, ","), chatKind(len(members)), members
}

// existingChat is the live dm or group of exactly members in project, if
// there is one (creating none).
func (ic *Intercom) existingChat(project Project, members []ident.ID) (Channel, bool) {
	key, kind, _ := chatKey(members)
	return ic.store.ChannelByKey(project.ID, kind, key)
}

// chat is the live dm (two members) or group (more) of exactly members in
// project, created (members joined from the log tail) when there is none.
func (ic *Intercom) chat(project Project, members []ident.ID) (Channel, error) {
	key, kind, members := chatKey(members)
	labels := make([]string, len(members))
	for i, m := range members {
		labels[i] = ic.label(m)
	}
	ch, ok := ic.store.ChannelByKey(project.ID, kind, key)
	if !ok {
		var err error
		ch, err = ic.store.CreateChannel(Channel{ProjectID: project.ID, Kind: kind, Key: key, Label: strings.Join(labels, ", ")})
		if errors.Is(err, ErrChannelExists) { // created meanwhile
			ch, ok = ic.store.ChannelByKey(project.ID, kind, key)
			if !ok {
				return Channel{}, err
			}
		} else if err != nil {
			return Channel{}, err
		}
	}
	// Every member is in (joining is a no-op for one who is), so a chat whose
	// creation was cut short or raced is completed here, not left half-joined.
	seq := ic.tail()
	for _, m := range members {
		if err := ic.store.JoinChannel(ch.ID, m, seq); err != nil {
			return Channel{}, err
		}
	}
	return ch, nil
}

// label renders a participant for a channel label: a registry name, else a
// session's name, else the id.
func (ic *Intercom) label(id ident.ID) string {
	if e, ok := ic.store.Resolve(id); ok {
		return e.Label()
	}
	if inst, ok := ic.store.GetInstance(string(id)); ok {
		return sessionLabel(inst)
	}
	return string(id)
}

func (ic *Intercom) resolveRoom(project Project, name string, globs []string) (Channel, error) {
	if name == "" {
		return Channel{}, ErrSendDenied
	}
	if !anyAllowed([]string{"channel:" + name}, globs) {
		return Channel{}, ErrSendDenied
	}
	ch, ok := ic.store.ChannelByKey(project.ID, SourceRoom, name)
	if !ok {
		return Channel{}, ErrSendUnresolved
	}
	return ch, nil
}

// resolveTicket finds a unit of work's log: "<key>" on the tracker connection,
// or "<connection name|id>/<key>". A GitHub key (owner/repo#<n>) is itself
// split by '/', so it is matched whole first, and is authorized and looked
// up in its canonical spelling only. A session's own ticket needs no addressing; any other
// needs ticket:<glob> — checked before a malformed GitHub key is refused
// (403 before 404), and the refusal never echoes the key.
func (ic *Intercom) resolveTicket(p Poster, project Project, ref string, globs []string) (Channel, error) {
	tracker, hasTracker := ic.tracker()
	conn, key, scoped := strings.Cut(ref, "/")
	if ticketkey.IsGitHub(ref) {
		conn, key, scoped = "", ref, false
	}
	connID, connOK := tracker, hasTracker
	if !scoped {
		key = ref
	} else if id, err := ResolveRegistryRef(ic.store, ident.Connection, conn); err == nil {
		connID, connOK = id, true
	} else if hasTracker && connKind(ic.store, tracker) == "github" {
		conn, key = "", ref // owner/repo#… on the tracker, malformed: refused below
	} else {
		connOK = false
	}
	if key == "" {
		return Channel{}, ErrSendDenied
	}
	forms := []string{"ticket:" + key, "ticket:" + ref}
	canon, isKey := key, true
	if connOK {
		canon, isKey = TicketUnit(ic.store, connID, key)
		if c, err := ticketkey.Canonical(key); err == nil && connKind(ic.store, connID) == "github" {
			// A GitHub key is authorized by its canonical spelling only (as
			// session: is): how it is spelled can't change the answer.
			forms = []string{"ticket:" + c}
			if conn != "" {
				forms = append(forms, "ticket:"+conn+"/"+c)
			}
		}
	}
	ownUnit, onTicket := "", false
	if hasTracker {
		ownUnit, onTicket = TicketUnit(ic.store, tracker, p.Session.Unit)
	}
	own := isKey && onTicket && ownUnit == canon && connOK && connID == tracker
	if !own && !anyAllowed(forms, globs) {
		return Channel{}, ErrSendDenied
	}
	switch {
	case own:
		ch, _, err := ic.ownTicketChannel(*p.Session)
		return ch, err
	case !connOK:
		return Channel{}, ErrSendUnresolved
	case !isKey && ticketkey.IsGitHub(key):
		return Channel{}, ErrSendUnresolved // another repo's: no ticket on the tracker
	case !isKey:
		return Channel{}, fmt.Errorf("%w: %w", ErrSendUnresolved, ticketkey.ErrGitHub)
	}
	ch, ok := ic.store.ChannelByKey(project.ID, SourceWork, string(connID)+"/"+canon)
	if !ok {
		return Channel{}, errNoLog
	}
	return ch, nil
}

// errNoLog is resolveTicket's unit with no work log yet.
var errNoLog = fmt.Errorf("%w: no work log", ErrSendUnresolved)

// resolveWorker finds the dm between the poster and the current worker of
// the unit ref names (authorized as its log is, by ticket: addressing).
// ErrNoWorker when the unit has no live worker (or no log yet);
// ErrSendUnresolved when the poster is the worker.
func (ic *Intercom) resolveWorker(p Poster, project Project, ref string, globs []string) (Channel, error) {
	log, err := ic.resolveTicket(p, project, ref, globs)
	switch {
	case errors.Is(err, errNoLog):
		return Channel{}, ErrNoWorker
	case err != nil:
		return Channel{}, err
	case log.Worker == "" || !ic.sessionLive(log.Worker):
		return Channel{}, ErrNoWorker
	case log.Worker == p.ID:
		return Channel{}, ErrSendUnresolved
	}
	return ic.chat(project, []ident.ID{p.ID, log.Worker})
}

// resolveSession finds the live session of project that ref names (its id,
// else its unique label) and returns the dm between the poster and it. It
// needs session:<glob>, checked before the session is looked up beyond its
// address (403 before 404). One's own session is no conversation
// (ErrSendUnresolved).
func (ic *Intercom) resolveSession(p Poster, project Project, ref string, globs []string) (Channel, error) {
	if ref == "" {
		return Channel{}, ErrSendDenied
	}
	target, found := ic.sessionNamed(project, ref)
	switch {
	case found && target.ActorID == p.Session.ActorID:
		return Channel{}, ErrSendUnresolved
	case found && anyAllowed([]string{"session:" + sessionLabel(target), "session:" + target.ActorID}, globs):
		return ic.chat(project, []ident.ID{p.ID, ident.ID(target.ActorID)})
	case anyAllowed([]string{"session:" + ref}, globs):
		return Channel{}, ErrSendUnresolved
	}
	return Channel{}, ErrSendDenied
}

// sessionNamed is the live session of project with id ref, else the only
// one labelled ref (two sharing a label: neither).
func (ic *Intercom) sessionNamed(project Project, ref string) (Instance, bool) {
	var byLabel []Instance
	for _, inst := range ic.store.ListInstances() {
		if inst.Phase == PhaseGone || !SameProject(ic.store, inst.Project, string(project.ID)) {
			continue
		}
		if inst.ActorID == ref {
			return inst, true
		}
		if sessionLabel(inst) == ref {
			byLabel = append(byLabel, inst)
		}
	}
	if len(byLabel) == 1 {
		return byLabel[0], true
	}
	return Instance{}, false
}

// ---- the sources ----

// chatSource: a dm or a group is its fixed member set; only they post and see.
type chatSource struct {
	ic   *Intercom
	kind SourceKind
}

func (s chatSource) Kind() SourceKind { return s.kind }
func (s chatSource) CanPost(p Poster, ch Channel, _ bool) bool {
	return s.ic.isMemberOf(ch, p.ID)
}
func (s chatSource) CanSee(p ident.ID, ch Channel) bool { return s.ic.isMemberOf(ch, p) }

// ticketSource: a ticket's conversation belongs to its project. Members (the
// sessions working it, people who joined) and the project's members post and
// see; a session whose addressing allows the ticket may post without joining.
type ticketSource struct{ ic *Intercom }

func (ticketSource) Kind() SourceKind { return SourceWork }
func (s ticketSource) CanPost(p Poster, ch Channel, allowed bool) bool {
	return allowed || s.CanSee(p.ID, ch)
}
func (s ticketSource) CanSee(p ident.ID, ch Channel) bool {
	return s.ic.isMemberOf(ch, p) || s.ic.projectUser(ch.ProjectID, p)
}

// roomSource: a room is open to its project's members and its own (sessions
// its role lists or that were called in); any other session posts to it when
// its addressing allows.
type roomSource struct{ ic *Intercom }

func (roomSource) Kind() SourceKind { return SourceRoom }
func (s roomSource) CanPost(p Poster, ch Channel, allowed bool) bool {
	return allowed || s.CanSee(p.ID, ch)
}
func (s roomSource) CanSee(p ident.ID, ch Channel) bool {
	return s.ic.isMemberOf(ch, p) || s.ic.projectUser(ch.ProjectID, p)
}

// sessionSource: a session channel an older Jam made, archived at serve
// start (Reconcile). Its members still read its history; nobody posts in it.
type sessionSource struct{ ic *Intercom }

func (sessionSource) Kind() SourceKind                   { return SourceSession }
func (sessionSource) CanPost(Poster, Channel, bool) bool { return false }
func (s sessionSource) CanSee(p ident.ID, ch Channel) bool {
	return s.ic.isMemberOf(ch, p)
}
