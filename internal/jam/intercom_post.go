package jam

import (
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
// current members but the author, ended sessions skipped), sorted. Join makes
// the poster a member by posting (addressing a session's home channel);
// JoinHome joins them to that session's home channel besides (a person's dm
// with a session, until its turn-end text stops going home: COV-350).
type Planned struct {
	Channel  Channel
	Audience []ident.ID
	Join     bool
	JoinHome ident.ID
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
}

// NewIntercom returns the intercom over store, appending to lg. tracker
// names the tracker connection ticket channels are keyed on (ok=false: none,
// so no ticket channels); tail reads the log's tail seq (nil: lg's).
func NewIntercom(store Store, tracker func() (ident.ID, bool), lg intercom.Store, tail func() int64, log *slog.Logger) *Intercom {
	if tail == nil {
		tail = func() int64 { seq, _ := lg.TailSeq(); return seq }
	}
	ic := &Intercom{store: store, tracker: tracker, lg: lg, tail: tail, log: log}
	ic.sources = map[SourceKind]Source{}
	for _, s := range []Source{chatSource{ic, SourceDM}, chatSource{ic, SourceGroup}, ticketSource{ic}, roomSource{ic}, sessionSource{ic}} {
		ic.sources[s.Kind()] = s
	}
	return ic
}

// ---- writing ----

// Post appends m to the planned channel with the planned audience (m's
// Channel is set from the plan, and its mentions part from its own and its
// body's: withMentions). A person posting in a ticket, a room or an
// open session channel (not a personal session's: that one is by invitation)
// joins it, from this post on; so does any poster the plan says joins (one
// addressing a session, whose access the plan already checked).
func (ic *Intercom) Post(pl Planned, m intercom.Squawk) (intercom.Squawk, error) {
	m.Channel = pl.Channel.ID
	m.Parts = ic.withMentions(pl.Channel, m)
	m, err := ic.lg.Append(m, pl.Audience)
	if err != nil {
		return intercom.Squawk{}, err
	}
	if (pl.Join || ic.joinsByPosting(pl.Channel) && !isSessionID(m.From)) && !ic.isMemberOf(pl.Channel, m.From) {
		if err := ic.store.JoinChannel(pl.Channel.ID, m.From, m.Seq); err != nil && ic.log != nil {
			ic.log.Warn("intercom: joining the poster to the channel failed", "channel", string(pl.Channel.ID), "err", err.Error())
		}
	}
	if pl.JoinHome != "" {
		if home, ok := ic.store.GetChannel(pl.JoinHome); ok && !ic.isMemberOf(home, m.From) {
			if err := ic.store.JoinChannel(home.ID, m.From, m.Seq); err != nil && ic.log != nil {
				ic.log.Warn("intercom: joining the poster to the session's home channel failed", "channel", string(home.ID), "err", err.Error())
			}
		}
	}
	return m, nil
}

// joinsByPosting reports whether a person posting in ch becomes a member:
// a ticket, a room, or a session channel open to its project.
func (ic *Intercom) joinsByPosting(ch Channel) bool {
	switch ch.Kind {
	case SourceWork, SourceRoom:
		return true
	case SourceSession:
		inst, ok := ic.store.GetInstance(ch.Key)
		return ok && !isPersonal(inst)
	}
	return false
}

// PostTrusted posts m into ch without asking the source whether its author
// may: relay ingress (already resolved by its binding) and Jam's own notices.
// The audience is still the channel's; an archived channel takes nothing.
func (ic *Intercom) PostTrusted(ch Channel, m intercom.Squawk) (intercom.Squawk, error) {
	if ch.Status != StatusLive {
		return intercom.Squawk{}, fmt.Errorf("%w: channel %s", ErrRemoved, ch.ID)
	}
	return ic.Post(Planned{Channel: ch, Audience: ic.audience(ch, m.From)}, m)
}

// Notify posts a notice from Jam as the session into its home channel,
// trusted: it works from the instance alone, after teardown too — a session
// channel archived at the session's end still takes Jam's notices about it,
// so its members learn why it ended. id "" gets a "notice:" id (IsNotice).
func (ic *Intercom) Notify(inst Instance, id, body string) (intercom.Squawk, error) {
	if id == "" {
		id = fmt.Sprintf("notice:%s:%d", inst.ActorID, time.Now().UnixNano())
	}
	m := intercom.Squawk{ID: id, From: ident.ID(inst.ActorID), Body: body}
	if ch, ok := ic.ownSessionChannel(inst); ok && ch.Status != StatusLive {
		return ic.Post(Planned{Channel: ch, Audience: ic.audience(ch, m.From)}, m)
	}
	if !ic.sessionLive(ident.ID(inst.ActorID)) { // gone: post where it was, joining or creating nothing
		if ch, ok := ic.ownSessionChannel(inst); ok {
			return ic.Post(Planned{Channel: ch, Audience: ic.audience(ch, m.From)}, m)
		}
		if ch, ok, err := ic.ticketChannel(inst, false); err == nil && ok {
			return ic.PostTrusted(ch, m)
		}
		return ic.notifyGone(inst, m)
	}
	ch, err := ic.HomeChannel(inst)
	if err != nil {
		return intercom.Squawk{}, err
	}
	return ic.PostTrusted(ch, m)
}

// notifyGone delivers Jam's notice about a session that is gone and never
// had its own channel: a personal session's starter still learns why it
// ended, in a channel archived as soon as it carries the notice; anyone
// else's notice has no one to reach and no channel is made for it.
func (ic *Intercom) notifyGone(inst Instance, m intercom.Squawk) (intercom.Squawk, error) {
	if _, ok := ic.starter(inst); !ok {
		return intercom.Squawk{}, fmt.Errorf("%w: session %s ended with no channel", ErrRemoved, inst.ActorID)
	}
	live := inst
	live.Phase = PhaseLive
	ch, err := ic.sessionChannel(live)
	if err != nil {
		return intercom.Squawk{}, err
	}
	posted, err := ic.Post(Planned{Channel: ch, Audience: ic.audience(ch, m.From)}, m)
	if aerr := ic.store.ArchiveChannel(ch.ID); err == nil && aerr != nil {
		err = aerr
	}
	return posted, err
}

// Reconcile gives every live session its home channel — at startup, for
// sessions set up before ticket or session channels existed, so replies have
// somewhere to land before they send.
func (ic *Intercom) Reconcile() error {
	var errs []error
	for _, inst := range ic.store.ListInstances() {
		if inst.Phase != PhaseGone {
			if err := ic.SetUp(inst); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", inst.ActorID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// ---- sessions' home channels ----

// SetUp gives a session its home channel (HomeChannel, its own session
// channel) and, for a session on a ticket, makes it the worker of the unit's
// work log (ownTicketChannel), so the issue's comments reach it.
func (ic *Intercom) SetUp(inst Instance) error {
	if _, err := ic.HomeChannel(inst); err != nil {
		return err
	}
	_, _, err := ic.ownTicketChannel(inst)
	return err
}

// Ended withdraws an ended session's open asks, takes it out of its unit's
// work log (the unit has no worker until it is dispatched again), and
// archives its session channel (history stays readable; Notify still
// reaches it).
func (ic *Intercom) Ended(inst Instance) error {
	ic.withdrawAsksOf(ident.ID(inst.ActorID))
	if inst.Unit != "" {
		ch, ok, err := ic.ticketChannel(inst, false)
		if err != nil {
			return err
		}
		if ok {
			if err := ic.store.LeaveChannel(ch.ID, ident.ID(inst.ActorID), ic.tail()); err != nil {
				return err
			}
			if ch.Worker == ident.ID(inst.ActorID) {
				if err := ic.store.SetChannelWorker(ch.ID, ""); err != nil && !errors.Is(err, ErrRemoved) {
					return err
				}
			}
		}
	}
	ch, ok := ic.ownSessionChannel(inst)
	if !ok || ch.Status != StatusLive {
		return nil
	}
	return ic.store.ArchiveChannel(ch.ID)
}

// HomeChannel is the channel a session is always in, where it posts with no
// address: its own session channel, whatever started it. A session channel
// is created on first use with the session in it — and, for a personal
// session, the user who started it, called in once (one who later leaves
// stays out).
func (ic *Intercom) HomeChannel(inst Instance) (Channel, error) {
	return ic.sessionChannel(inst)
}

// ownTicketChannel is the work log of inst's unit, created on demand, with
// inst its worker: a new worker takes the old one's place in the members
// (a no-op while inst is the worker). A live worker raised after inst keeps
// the unit, and inst is no member. ok=false when inst has no ticket on the
// tracker.
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
	return ch, true, nil
}

// ownSessionChannel is the session channel inst is in, in its project: the
// live one, else the latest archived one.
func (ic *Intercom) ownSessionChannel(inst Instance) (Channel, bool) {
	p, exists := ic.store.GetProject(orDefaultProject(inst.Project))
	if !exists {
		return Channel{}, false
	}
	var found Channel
	ok := false
	for _, id := range ic.store.ChannelsOf(ident.ID(inst.ActorID)) {
		ch, exists := ic.store.GetChannel(id)
		if !exists || ch.Kind != SourceSession || ch.Key != inst.ActorID || ch.ProjectID != p.ID {
			continue
		}
		if ch.Status == StatusLive {
			return ch, true
		}
		if !ok || ch.ID > found.ID {
			found, ok = ch, true
		}
	}
	return found, ok
}

// sessionChannel finds or creates inst's live session channel.
func (ic *Intercom) sessionChannel(inst Instance) (Channel, error) {
	p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok {
		return Channel{}, fmt.Errorf("%w: %q", ErrProjectNotFound, inst.Project)
	}
	self := ident.ID(inst.ActorID)
	if ch, ok := ic.store.ChannelByKey(p.ID, SourceSession, inst.ActorID); ok {
		return ch, ic.store.JoinChannel(ch.ID, self, ic.tail()) // a no-op while it is one
	}
	if inst.Phase == PhaseGone {
		return Channel{}, fmt.Errorf("%w: session %s ended", ErrRemoved, inst.ActorID)
	}
	// Set up again under the same id (a restart, an upgrade): its channel
	// comes back, with whoever was in it.
	if old, ok := ic.ownSessionChannel(inst); ok {
		if err := ic.store.ReopenChannel(old.ID); err == nil {
			old.Status = StatusLive
			return old, ic.store.JoinChannel(old.ID, self, ic.tail())
		} else if !errors.Is(err, ErrChannelExists) {
			return Channel{}, err
		}
		if ch, ok := ic.store.ChannelByKey(p.ID, SourceSession, inst.ActorID); ok { // reopened or created meanwhile
			return ch, ic.store.JoinChannel(ch.ID, self, ic.tail())
		}
	}
	ch, err := ic.store.CreateChannel(Channel{ProjectID: p.ID, Kind: SourceSession, Key: inst.ActorID, Label: sessionLabel(inst)})
	if errors.Is(err, ErrChannelExists) { // created meanwhile: its creator called the starter in
		if ch, ok = ic.store.ChannelByKey(p.ID, SourceSession, inst.ActorID); !ok {
			return Channel{}, err
		}
		return ch, ic.store.JoinChannel(ch.ID, self, ic.tail())
	}
	if err != nil {
		return Channel{}, err
	}
	seq := ic.tail()
	if err := ic.store.JoinChannel(ch.ID, self, seq); err != nil {
		return Channel{}, err
	}
	if starter, ok := ic.starter(inst); ok && ic.projectUser(p.ID, starter) {
		if err := ic.store.JoinChannel(ch.ID, starter, seq); err != nil {
			return Channel{}, err
		}
	}
	return ch, nil
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
// the issue for ingress — unless a room already holds that binding, which
// keeps it. ok=false when there is no tracker connection, or inst's unit is
// no ticket key on it (a github tracker's unit must be owner/repo#<n>; any
// other, say a manual raise's free text, leaves the session on its own
// channel).
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
	ch := Channel{ProjectID: p.ID, Kind: SourceWork, Key: key, Label: unit}
	b := Binding{ConnectionID: conn, Ref: unit, Mode: BindBoth}
	if holder, taken := ic.store.ChannelByBinding(conn, unit); !taken {
		ch.Bindings = []Binding{b}
	} else if ic.log != nil {
		ic.log.Warn("intercom: ticket channel left unbound; another channel receives the issue's replies",
			"ticket", unit, "holder", string(holder.ID))
	}
	created, err := ic.store.CreateChannel(ch)
	if errors.Is(err, ErrBindingTaken) { // a room took the issue meanwhile: it keeps it
		ch.Bindings = nil
		created, err = ic.store.CreateChannel(ch)
	}
	if errors.Is(err, ErrChannelExists) { // created meanwhile
		created, ok = ic.store.ChannelByKey(p.ID, SourceWork, key)
		return created, ok, nil
	}
	return created, err == nil, err
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

// Plan decides a session's send to addr ("" = its default channel):
// user:<name|id> (human: alias) and chat:<member>[,<member>…] (user: or
// session: members) → the dm or group with them; session:<label|id> → that
// session's home channel, joining it so replies come back; channel:<name> →
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
		ch, err := ic.HomeChannel(*p.Session)
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
	pl, err := ic.planIn(p, ch, true)
	pl.Join = kind == "session" // addressing a session joins its conversation, so replies come back
	return pl, err
}

// PlanSend is Plan for a session's send (POST /squawks). Under the turn-end
// rule (Instance.TurnProtocol 3) a send names its conversation — the cove
// fills a turn's later sends in from its first — so one with no addr is
// ErrSendNeedsTo. A session on an older turn protocol keeps Plan's default,
// its home channel. Asks and whiteboards keep it too.
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
		if home, ok := ic.homeWithMember(p, project, rest, globs); ok {
			return home, nil // already in the session's own channel: keep one conversation
		}
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
// when it names none yet): a reply needs no addressing. A user: address
// names the session's own channel when that person is in it. It creates
// nothing.
func (ic *Intercom) replyChannel(p Poster, project Project, kind, rest string) (Channel, bool) {
	var ch Channel
	var ok bool
	switch kind {
	case "user", "human":
		if home, found := ic.homeWithMember(p, project, rest, anyGlobs); found {
			// They are in the session's own channel: reply there, as
			// resolve would with addressing.
			ch, ok = home, true
			break
		}
		fallthrough
	case "chat":
		refs := strings.Split(rest, ",")
		if kind != "chat" {
			refs = []string{"user:" + rest}
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
	case "session":
		if target, found := ic.sessionNamed(project, rest); found {
			ch, ok = ic.store.ChannelByKey(project.ID, SourceSession, target.ActorID)
		}
	}
	return ch, ok && ic.deliveredIn(p.ID, ch.ID)
}

// ReplyTo is the send address that replies in channel chID for the session
// self ("" when none answers to it): its own channel is session:<its label>, a
// dm or group chat: its other members, a room channel:<name>, a work log
// work:<key>, another session's channel session:<it>. A reply needs no
// addressing (replyChannel), so the session can always use it.
func (ic *Intercom) ReplyTo(self Instance, chID ident.ID) string {
	ch, ok := ic.store.GetChannel(chID)
	if !ok {
		return ""
	}
	switch ch.Kind {
	case SourceSession:
		// Its own channel too (own needs no addressing): by label when the
		// label names that session alone, else by id.
		if p, ok := ic.store.GetProject(orDefaultProject(self.Project)); ok {
			if inst, ok := ic.store.GetInstance(ch.Key); ok {
				if named, ok := ic.sessionNamed(p, sessionLabel(inst)); ok && named.ActorID == inst.ActorID {
					return "session:" + sessionLabel(inst)
				}
			}
		}
		return "session:" + ch.Key
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
// MayReach allows. The session's turn-end text still goes to its home
// channel until the turn-end rule lands (COV-350), so the person joins that
// too (JoinHome), as before, to get the reply. A session that doesn't exist
// or that they may not reach is ErrSendDenied alike.
func (ic *Intercom) PlanSessionDM(from ident.ID, ref string) (Planned, error) {
	inst, ok := ic.personSession(from, ref)
	if !ok {
		return Planned{}, ErrSendDenied
	}
	p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok || !ic.projectUser(p.ID, from) {
		return Planned{}, ErrSendDenied
	}
	home, err := ic.HomeChannel(inst) // set up with its starter, whom MayReach finds there
	if err != nil {
		return Planned{}, err
	}
	if !ic.MayReach(from, inst) {
		return Planned{}, ErrSendDenied
	}
	ch, err := ic.chat(p, []ident.ID{from, ident.ID(inst.ActorID)})
	if err != nil {
		return Planned{}, err
	}
	pl, err := ic.planIn(Poster{ID: from}, ch, false)
	if err != nil {
		return Planned{}, err
	}
	pl.JoinHome = home.ID
	return pl, nil
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
// project, and for a personal session one already in its channel.
func (ic *Intercom) MayReach(u ident.ID, inst Instance) bool {
	p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok || inst.Phase == PhaseGone || !ic.projectUser(p.ID, u) {
		return false
	}
	if !isPersonal(inst) {
		return true
	}
	ch, ok := ic.store.ChannelByKey(p.ID, SourceSession, inst.ActorID)
	return ok && ic.isMemberOf(ch, u)
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

// homeWithMember is the poster's own session channel when user ref (whom its
// addressing allows) is already a member of it — a user: send then posts
// there rather than opening a separate chat with them.
func (ic *Intercom) homeWithMember(p Poster, project Project, ref string, globs []string) (Channel, bool) {
	u, found := ic.memberUser(project, ref)
	if !found || !ic.userAllowed(p, u, globs) {
		return Channel{}, false
	}
	home, err := ic.HomeChannel(*p.Session)
	if err != nil || home.Kind != SourceSession || !ic.isMemberOf(home, u.ID) {
		return Channel{}, false
	}
	return home, true
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
// else its unique label) and returns its home channel. One's own session
// needs no addressing;
// any other needs session:<glob>, checked before the session is looked up
// beyond its address (403 before 404).
func (ic *Intercom) resolveSession(p Poster, project Project, ref string, globs []string) (Channel, error) {
	if ref == "" {
		return Channel{}, ErrSendDenied
	}
	target, found := ic.sessionNamed(project, ref)
	own := found && target.ActorID == p.Session.ActorID
	switch {
	case own:
		return ic.HomeChannel(target)
	case found && anyAllowed([]string{"session:" + sessionLabel(target), "session:" + target.ActorID}, globs):
		return ic.HomeChannel(target)
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

// roomSource: a room is open to its project's members; a session posts to it
// when its addressing allows (rooms are post-only for sessions).
type roomSource struct{ ic *Intercom }

func (roomSource) Kind() SourceKind { return SourceRoom }
func (s roomSource) CanPost(p Poster, ch Channel, allowed bool) bool {
	return allowed || s.CanSee(p.ID, ch)
}
func (s roomSource) CanSee(p ident.ID, ch Channel) bool {
	return s.ic.isMemberOf(ch, p) || s.ic.projectUser(ch.ProjectID, p)
}

// sessionSource: a session's own channel. The session and those called in or
// who joined post and see; a standing or manual session's channel is open to
// its project's members, a personal session's only to its members (people
// join it by invitation). A session whose addressing allows it may post.
type sessionSource struct{ ic *Intercom }

func (sessionSource) Kind() SourceKind { return SourceSession }
func (s sessionSource) CanPost(p Poster, ch Channel, allowed bool) bool {
	return allowed || s.CanSee(p.ID, ch)
}
func (s sessionSource) CanSee(p ident.ID, ch Channel) bool {
	if s.ic.isMemberOf(ch, p) {
		return true
	}
	inst, ok := s.ic.store.GetInstance(ch.Key)
	return ok && !isPersonal(inst) && s.ic.projectUser(ch.ProjectID, p)
}
