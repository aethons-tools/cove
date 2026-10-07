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
// the poster a member by posting (addressing a session's home channel).
type Planned struct {
	Channel  Channel
	Audience []ident.ID
	Join     bool
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
	for _, s := range []Source{chatSource{ic}, ticketSource{ic}, roomSource{ic}, sessionSource{ic}} {
		ic.sources[s.Kind()] = s
	}
	return ic
}

// ---- writing ----

// Post appends m to the planned channel with the planned audience (m's
// Channel is set from the plan). A person posting in a ticket, a room or an
// open session channel (not a personal session's: that one is by invitation)
// joins it, from this post on; so does any poster the plan says joins (one
// addressing a session, whose access the plan already checked).
func (ic *Intercom) Post(pl Planned, m intercom.Squawk) (intercom.Squawk, error) {
	m.Channel = pl.Channel.ID
	m, err := ic.lg.Append(m, pl.Audience)
	if err != nil {
		return intercom.Squawk{}, err
	}
	if (pl.Join || ic.joinsByPosting(pl.Channel) && !isSessionID(m.From)) && !ic.isMemberOf(pl.Channel, m.From) {
		if err := ic.store.JoinChannel(pl.Channel.ID, m.From, m.Seq); err != nil && ic.log != nil {
			ic.log.Warn("intercom: joining the poster to the channel failed", "channel", string(pl.Channel.ID), "err", err.Error())
		}
	}
	return m, nil
}

// joinsByPosting reports whether a person posting in ch becomes a member:
// a ticket, a room, or a session channel open to its project.
func (ic *Intercom) joinsByPosting(ch Channel) bool {
	switch ch.Kind {
	case SourceTicket, SourceRoom:
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

// SetUp gives a session its home channel (HomeChannel): it joins its
// ticket's channel, or its own session channel is created.
func (ic *Intercom) SetUp(inst Instance) error {
	_, err := ic.HomeChannel(inst)
	return err
}

// Ended takes an ended session out of its ticket's channel, or archives its
// session channel (history stays readable; Notify still reaches it).
func (ic *Intercom) Ended(inst Instance) error {
	if inst.Unit != "" {
		ch, ok, err := ic.ticketChannel(inst, false)
		if err != nil {
			return err
		}
		if ok {
			return ic.store.LeaveChannel(ch.ID, ident.ID(inst.ActorID), ic.tail())
		}
	}
	ch, ok := ic.ownSessionChannel(inst)
	if !ok || ch.Status != StatusLive {
		return nil
	}
	return ic.store.ArchiveChannel(ch.ID)
}

// HomeChannel is the channel a session is always in, where it posts with no
// address: its ticket's channel for a session on a ticket (joined, created
// on demand), else its own session channel. A session channel is created on
// first use with the session in it — and, for a personal session, the user
// who started it, called in once (one who later leaves stays out).
func (ic *Intercom) HomeChannel(inst Instance) (Channel, error) {
	if inst.Unit != "" {
		ch, ok, err := ic.ticketChannel(inst, true)
		if err != nil {
			return Channel{}, err
		}
		if ok {
			if err := ic.store.JoinChannel(ch.ID, ident.ID(inst.ActorID), ic.tail()); err != nil { // a no-op while it is one
				return Channel{}, err
			}
			return ch, nil
		}
		// No tracker: the session has its own channel instead.
	}
	return ic.sessionChannel(inst)
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
// keeps it. ok=false when there is no tracker connection.
func (ic *Intercom) ticketChannel(inst Instance, create bool) (Channel, bool, error) {
	conn, ok := ic.tracker()
	if !ok {
		return Channel{}, false, nil
	}
	p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok {
		return Channel{}, false, fmt.Errorf("%w: %q", ErrProjectNotFound, inst.Project)
	}
	key := string(conn) + "/" + inst.Unit
	if ch, ok := ic.store.ChannelByKey(p.ID, SourceTicket, key); ok || !create {
		return ch, ok, nil
	}
	ch := Channel{ProjectID: p.ID, Kind: SourceTicket, Key: key, Label: inst.Unit}
	b := Binding{ConnectionID: conn, Ref: inst.Unit, Mode: BindBoth}
	if holder, taken := ic.store.ChannelByBinding(conn, inst.Unit); !taken {
		ch.Bindings = []Binding{b}
	} else if ic.log != nil {
		ic.log.Warn("intercom: ticket channel left unbound; another channel receives the issue's replies",
			"ticket", inst.Unit, "holder", string(holder.ID))
	}
	created, err := ic.store.CreateChannel(ch)
	if errors.Is(err, ErrBindingTaken) { // a room took the issue meanwhile: it keeps it
		ch.Bindings = nil
		created, err = ic.store.CreateChannel(ch)
	}
	if errors.Is(err, ErrChannelExists) { // created meanwhile
		created, ok = ic.store.ChannelByKey(p.ID, SourceTicket, key)
		return created, ok, nil
	}
	return created, err == nil, err
}

// ---- planning a post ----

// Plan decides a session's send to addr ("" = its default channel):
// user:<name|id> (human: alias) and chat:user:<a>[,user:<b>…] → a chat with
// them; channel:<name> → a room; ticket:<key> or ticket:<connection>/<key> →
// a ticket's channel. ErrSendDenied when the session's addressing doesn't
// allow the address (checked first: a 403 never reveals existence),
// ErrSendUnresolved when it does but nothing answers to it (404).
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
	globs := ic.ceiling(p, project.Name)
	kind, rest, _ := strings.Cut(addr, ":")
	var ch Channel
	var err error
	switch kind {
	case "user", "human":
		if home, ok := ic.homeWithMember(p, project, rest, globs); ok {
			ch = home // already in the session's own channel: keep one conversation
			break
		}
		ch, err = ic.resolveChat(p, project, []string{rest}, globs)
	case "chat":
		var refs []string
		for _, m := range strings.Split(rest, ",") {
			ref, ok := strings.CutPrefix(m, "user:")
			if !ok {
				return Planned{}, ErrSendDenied
			}
			refs = append(refs, ref)
		}
		ch, err = ic.resolveChat(p, project, refs, globs)
	case "channel":
		ch, err = ic.resolveRoom(project, rest, globs)
	case "ticket":
		ch, err = ic.resolveTicket(p, project, rest, globs)
	case "session":
		if ch, err = ic.resolveSession(p, project, rest, globs); err != nil {
			return Planned{}, err
		}
		pl, err := ic.planIn(p, ch, true)
		pl.Join = true // addressing a session joins its conversation, so replies come back
		return pl, err
	default:
		return Planned{}, ErrSendDenied
	}
	if err != nil {
		return Planned{}, err
	}
	return ic.planIn(p, ch, true)
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

// PlanPersonChat decides a person's new conversation from /me with another
// user: a chat in a project both are live members of (the first by name).
// ErrSendUnresolved when there is none.
func (ic *Intercom) PlanPersonChat(from, with ident.ID) (Planned, error) {
	if from == with || from.Kind() != ident.User || with.Kind() != ident.User {
		return Planned{}, ErrSendUnresolved
	}
	for _, name := range ic.store.ListProjects() {
		if p, ok := ic.store.GetProject(name); ok && ic.projectUser(p.ID, from) && ic.projectUser(p.ID, with) {
			ch, err := ic.chat(p, []ident.ID{from, with})
			if err != nil {
				return Planned{}, err
			}
			return ic.planIn(Poster{ID: from}, ch, false)
		}
	}
	return Planned{}, ErrSendUnresolved
}

// PlanHome decides a person's post to a live session from /me: into the
// session's home channel, as its source allows them (a personal session's
// channel only to its members), joining it. A session that doesn't exist or
// that they may not reach is ErrSendDenied alike.
func (ic *Intercom) PlanHome(from, session ident.ID) (Planned, error) {
	inst, ok := ic.store.GetInstance(string(session))
	if !ok || inst.Phase == PhaseGone || from.Kind() != ident.User {
		return Planned{}, ErrSendDenied
	}
	p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok || !ic.projectUser(p.ID, from) {
		return Planned{}, ErrSendDenied
	}
	ch, err := ic.HomeChannel(inst)
	if err != nil {
		return Planned{}, err
	}
	pl, err := ic.planIn(Poster{ID: from}, ch, false)
	pl.Join = true
	return pl, err
}

// MayReach reports, without creating anything, whether user u may post to
// live session inst from /me (PlanHome would allow it): a member of its
// project, and for a personal session one already in its channel.
func (ic *Intercom) MayReach(u ident.ID, inst Instance) bool {
	p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok || inst.Phase == PhaseGone || !ic.projectUser(p.ID, u) {
		return false
	}
	if inst.Unit != "" || !isPersonal(inst) {
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

// resolveChat resolves user refs (names or ids) to members of project the
// session may address, then the chat of them and the session. Every member
// is checked before any is looked up beyond its address (403 before 404).
func (ic *Intercom) resolveChat(p Poster, project Project, refs []string, globs []string) (Channel, error) {
	members := []ident.ID{p.ID}
	var missing bool
	for _, ref := range refs {
		if ref == "" {
			return Channel{}, ErrSendDenied
		}
		written := []string{"user:" + ref}
		u, found := ic.memberUser(project, ref)
		switch {
		case found && anyAllowed([]string{"user:" + u.Name, "user:" + string(u.ID)}, globs):
			members = append(members, u.ID)
		case anyAllowed(written, globs):
			missing = true
		default:
			return Channel{}, ErrSendDenied
		}
	}
	if missing {
		return Channel{}, ErrSendUnresolved
	}
	return ic.chat(project, members)
}

// homeWithMember is the poster's own session channel when user ref (whom its
// addressing allows) is already a member of it — a user: send then posts
// there rather than opening a separate chat with them.
func (ic *Intercom) homeWithMember(p Poster, project Project, ref string, globs []string) (Channel, bool) {
	u, found := ic.memberUser(project, ref)
	if !found || !anyAllowed([]string{"user:" + u.Name, "user:" + string(u.ID)}, globs) {
		return Channel{}, false
	}
	if p.Session.Unit != "" {
		return Channel{}, false // a ticket session's home is its ticket: user: stays a chat
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

// chat is the live chat of exactly members in project, created (members
// joined from the log tail) when there is none.
func (ic *Intercom) chat(project Project, members []ident.ID) (Channel, error) {
	members = slices.Clone(members)
	slices.Sort(members)
	members = slices.Compact(members)
	parts := make([]string, len(members))
	labels := make([]string, len(members))
	for i, m := range members {
		parts[i] = string(m)
		labels[i] = ic.label(m)
	}
	key := strings.Join(parts, ",")
	ch, ok := ic.store.ChannelByKey(project.ID, SourceChat, key)
	if !ok {
		var err error
		ch, err = ic.store.CreateChannel(Channel{ProjectID: project.ID, Kind: SourceChat, Key: key, Label: strings.Join(labels, ", ")})
		if errors.Is(err, ErrChannelExists) { // created meanwhile
			ch, ok = ic.store.ChannelByKey(project.ID, SourceChat, key)
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

// resolveTicket finds a ticket's channel: "<key>" on the tracker connection,
// or "<connection name|id>/<key>". A session's own ticket needs no
// addressing; any other needs ticket:<glob>.
func (ic *Intercom) resolveTicket(p Poster, project Project, ref string, globs []string) (Channel, error) {
	tracker, hasTracker := ic.tracker()
	conn, key, scoped := strings.Cut(ref, "/")
	connID, connOK := tracker, hasTracker
	if !scoped {
		key = ref
	} else if id, err := ResolveRegistryRef(ic.store, ident.Connection, conn); err == nil {
		connID, connOK = id, true
	} else {
		connOK = false
	}
	if key == "" {
		return Channel{}, ErrSendDenied
	}
	own := p.Session.Unit == key && connOK && hasTracker && connID == tracker
	if !own && !anyAllowed([]string{"ticket:" + key, "ticket:" + ref}, globs) {
		return Channel{}, ErrSendDenied
	}
	switch {
	case own:
		return ic.HomeChannel(*p.Session)
	case !connOK:
		return Channel{}, ErrSendUnresolved
	}
	ch, ok := ic.store.ChannelByKey(project.ID, SourceTicket, string(connID)+"/"+key)
	if !ok {
		return Channel{}, ErrSendUnresolved
	}
	return ch, nil
}

// resolveSession finds the live session of project that ref names (its id,
// else its unique label) and returns its home channel. One's own session
// needs no addressing; any other needs session:<glob>, checked before the
// session is looked up beyond its address (403 before 404).
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
		// A ticket session's home is its ticket: reaching it needs what
		// addressing that ticket needs (session: is no way around ticket:).
		if tracker, ok := ic.tracker(); ok && !sessionTicketAllowed(ic.store, tracker, *p.Session, target, globs) {
			return Channel{}, ErrSendDenied
		}
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

// chatSource: a chat is its fixed member set; only they post and see.
type chatSource struct{ ic *Intercom }

func (chatSource) Kind() SourceKind { return SourceChat }
func (s chatSource) CanPost(p Poster, ch Channel, _ bool) bool {
	return s.ic.isMemberOf(ch, p.ID)
}
func (s chatSource) CanSee(p ident.ID, ch Channel) bool { return s.ic.isMemberOf(ch, p) }

// ticketSource: a ticket's conversation belongs to its project. Members (the
// sessions working it, people who joined) and the project's members post and
// see; a session whose addressing allows the ticket may post without joining.
type ticketSource struct{ ic *Intercom }

func (ticketSource) Kind() SourceKind { return SourceTicket }
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
