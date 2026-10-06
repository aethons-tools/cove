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

// ErrNoDefaultChannel is a send with no address from a session that has no
// default channel (a standing session: no ticket, no one who started it).
var ErrNoDefaultChannel = errors.New(`no default recipient: pass "to"`)

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
	for _, s := range []Source{chatSource{ic}, ticketSource{ic}, roomSource{ic}} {
		ic.sources[s.Kind()] = s
	}
	return ic
}

// ---- writing ----

// Post appends m to the planned channel with the planned audience (m's
// Channel is set from the plan). A person posting in a ticket or room joins
// it, from this post on.
func (ic *Intercom) Post(pl Planned, m intercom.Squawk) (intercom.Squawk, error) {
	m.Channel = pl.Channel.ID
	m, err := ic.lg.Append(m, pl.Audience)
	if err != nil {
		return intercom.Squawk{}, err
	}
	if (pl.Channel.Kind == SourceTicket || pl.Channel.Kind == SourceRoom) && !isSessionID(m.From) && !ic.isMemberOf(pl.Channel, m.From) {
		if err := ic.store.JoinChannel(pl.Channel.ID, m.From, m.Seq); err != nil && ic.log != nil {
			ic.log.Warn("intercom: joining the poster to the channel failed", "channel", string(pl.Channel.ID), "err", err.Error())
		}
	}
	return m, nil
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

// Notify posts a notice from Jam as the session into its default channel
// (for a personal session, the chat with its owner), trusted: it works from
// the instance alone, after teardown too. id "" lets the log assign one.
func (ic *Intercom) Notify(inst Instance, id, body string) (intercom.Squawk, error) {
	ch, err := ic.DefaultChannel(inst)
	if err != nil {
		return intercom.Squawk{}, err
	}
	return ic.PostTrusted(ch, intercom.Squawk{ID: id, From: ident.ID(inst.ActorID), Body: body})
}

// Reconcile gives every live session on a ticket its ticket channel — at
// startup, for sessions set up before ticket channels existed, so replies on
// their tickets have somewhere to land before they send.
func (ic *Intercom) Reconcile() error {
	var errs []error
	for _, inst := range ic.store.ListInstances() {
		if inst.Phase != PhaseGone && inst.Unit != "" {
			if err := ic.SetUp(inst); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", inst.ActorID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// ---- sessions' ticket channels ----

// SetUp joins a session set up on a ticket to the ticket's channel, creating
// it on first use. A session without a ticket has nothing to join.
func (ic *Intercom) SetUp(inst Instance) error {
	if inst.Unit == "" {
		return nil
	}
	ch, ok, err := ic.ticketChannel(inst, true)
	if err != nil || !ok {
		return err
	}
	return ic.store.JoinChannel(ch.ID, ident.ID(inst.ActorID), ic.tail())
}

// Ended takes an ended session out of its ticket's channel.
func (ic *Intercom) Ended(inst Instance) error {
	if inst.Unit == "" {
		return nil
	}
	ch, ok, err := ic.ticketChannel(inst, false)
	if err != nil || !ok {
		return err
	}
	return ic.store.LeaveChannel(ch.ID, ident.ID(inst.ActorID), ic.tail())
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
		ch, err := ic.DefaultChannel(*p.Session)
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

// DefaultChannel is a session's channel when it gives no address: its
// ticket's, else a chat with the user who started it (until slice 3 gives
// every session its own channel).
func (ic *Intercom) DefaultChannel(inst Instance) (Channel, error) {
	switch {
	case inst.Unit != "":
		ch, ok, err := ic.ticketChannel(inst, true)
		if err != nil {
			return Channel{}, err
		}
		if !ok {
			return Channel{}, ErrNoDefaultChannel
		}
		if err := ic.store.JoinChannel(ch.ID, ident.ID(inst.ActorID), ic.tail()); err != nil { // a no-op while it is one
			return Channel{}, err
		}
		return ch, nil
	case inst.OwnerID != "":
		p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
		if !ok || !ic.projectUser(p.ID, inst.OwnerID) {
			return Channel{}, ErrSendUnresolved
		}
		return ic.chat(p, []ident.ID{ident.ID(inst.ActorID), inst.OwnerID})
	}
	return Channel{}, ErrNoDefaultChannel
}

// ceiling is the session's addressing in project: the effective addressing
// of each of its grants there (additive).
func (ic *Intercom) ceiling(p Poster, project string) []string {
	var globs []string
	for _, g := range p.Actor.Grants {
		if orDefaultProject(g.Project) != project {
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
		return ic.DefaultChannel(*p.Session)
	case !connOK:
		return Channel{}, ErrSendUnresolved
	}
	ch, ok := ic.store.ChannelByKey(project.ID, SourceTicket, string(connID)+"/"+key)
	if !ok {
		return Channel{}, ErrSendUnresolved
	}
	return ch, nil
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
