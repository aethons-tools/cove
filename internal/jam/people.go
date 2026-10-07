package jam

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/aethons-tools/cove/internal/ident"
)

// Project members as Jam's surfaces see them, read from the identity registry
// (users, memberships, accounts). This replaces the per-project roster view
// of "humans" (intercom 1a-3e).

// Member is a live user who is a member of a project: their per-project
// delivery addresses, and the @-handle and Discord user id of the accounts
// linked to them (on the linear and discord connections).
type Member struct {
	User       User
	Delivery   []DeliveryProfile
	Handle     string // their linear account's @-handle; "" = none
	DiscordUID string // their discord account's user id; "" = unbound
}

// Inbox is the member's address on service (for "discord", the inbox channel
// Jam posts their chats to).
func (m Member) Inbox(service string) (string, bool) {
	for _, d := range m.Delivery {
		if d.Service == service && d.Address != "" {
			return d.Address, true
		}
	}
	return "", false
}

// linkedAccount is user's live account on the connection of kind that match
// selects (lowest id, for determinism).
func linkedAccount(store Store, kind string, user ident.ID, match func(Account) bool) (Account, bool) {
	c, ok := store.ConnectionOfKind(kind)
	if !ok {
		return Account{}, false
	}
	var best Account
	found := false
	for _, a := range store.ListAccounts(c.ID) {
		if a.UserID == user && a.Status == StatusLive && match(a) && (!found || a.ID < best.ID) {
			best, found = a, true
		}
	}
	return best, found
}

// MemberOf is user as a member of project (ok=false: no live member).
func MemberOf(store Store, project, user ident.ID) (Member, bool) {
	u, ok := store.GetUser(user)
	if !ok || u.Status != StatusLive || !store.IsMember(project, user) {
		return Member{}, false
	}
	ms, _ := store.GetMembership(project, user)
	m := Member{User: u, Delivery: slices.Clone(ms.Delivery)}
	if a, ok := linkedAccount(store, "linear", user, func(a Account) bool { return a.Handle != "" }); ok {
		m.Handle = a.Handle
	}
	if a, ok := linkedAccount(store, "discord", user, func(a Account) bool { return a.ServiceUID != "" }); ok {
		m.DiscordUID = a.ServiceUID
	}
	return m, true
}

// MembersOf lists project's live members, sorted by name.
func MembersOf(store Store, project ident.ID) []Member {
	var out []Member
	for _, uid := range store.ListMembers(project) {
		if m, ok := MemberOf(store, project, uid); ok {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b Member) int { return cmp.Compare(a.User.Name, b.User.Name) })
	return out
}

// MemberByLogin is the member of the project named project who holds login
// (any of their logins). The empty login never matches.
func MemberByLogin(store Store, project, login string) (Member, bool) {
	if login == "" {
		return Member{}, false
	}
	u, ok := store.UserByLogin(login)
	if !ok {
		return Member{}, false
	}
	p, ok := store.GetProject(orDefaultProject(project))
	if !ok {
		return Member{}, false
	}
	return MemberOf(store, p.ID, u.ID)
}

// DiscordAuthorOf names the member a Discord message in project is from —
// by its immutable author id, never the display name (which anyone can set):
//
//  1. an author id bound to exactly one member (their discord account) is
//     that member, in any channel, shared inboxes included;
//  2. a channel that is exactly one member's inbox (and no room's channel) is
//     that member — only while they are unbound: once bound, only their own
//     Discord account counts as them;
//  3. otherwise nobody (the caller records the author's account).
//
// by is "id" or "channel". Every doubt fails toward nobody.
func DiscordAuthorOf(store Store, project ident.ID, channel, authorID string) (m Member, by string, ok bool) {
	members := MembersOf(store, project)
	if authorID != "" {
		var hits []Member
		for _, x := range members {
			if x.DiscordUID == authorID {
				hits = append(hits, x)
			}
		}
		if len(hits) == 1 {
			return hits[0], "id", true
		}
	}
	if channel == "" {
		return Member{}, "", false
	}
	if isDiscordRoomChannel(store, project, channel) {
		return Member{}, "", false // a room's channel is a shared conduit, not an inbox
	}
	var owners []Member
	for _, x := range members {
		if inbox, ok := x.Inbox("discord"); ok && inbox == channel {
			owners = append(owners, x)
		}
	}
	if len(owners) != 1 || owners[0].DiscordUID != "" {
		return Member{}, "", false
	}
	return owners[0], "channel", true
}

// isDiscordRoomChannel reports whether Discord channel ref is a room's: bound,
// in any mode, to a live room of project on any discord connection, or
// receiving any project's room's replies. It errs toward "a room".
func isDiscordRoomChannel(store Store, project ident.ID, ref string) bool {
	for _, c := range store.ListConnections() {
		if c.Kind != "discord" {
			continue
		}
		if _, ok := store.ChannelByBinding(c.ID, ref); ok {
			return true
		}
		for _, ch := range store.ListChannels(project, SourceRoom) {
			for _, b := range ch.Bindings {
				if b.ConnectionID == c.ID && b.Ref == ref {
					return true
				}
			}
		}
	}
	return false
}

// ProjectMembers adapts a Store to "a project's members by project name"
// (the escalation engine's view).
type ProjectMembers struct{ Store }

// Members lists the members of the project named project.
func (p ProjectMembers) Members(project string) []Member {
	pr, ok := p.Store.GetProject(orDefaultProject(project))
	if !ok {
		return nil
	}
	return MembersOf(p.Store, pr.ID)
}

// AddPerson makes the person h describes a member of project, in registry
// terms — a convenience for tests and tooling with a pre-registry shape:
// the user named h.Name (created if new) gains h's login and OIDC bindings,
// becomes a member with h's delivery addresses, and has h's tracker handle
// and Discord user id as their linked accounts (a service identity another
// user holds is ErrAccountLinked). It never strips identity the user already
// has.
func AddPerson(store Store, project string, h Human) error {
	p, ok := store.GetProject(orDefaultProject(project))
	if !ok {
		if orDefaultProject(project) != DefaultProject {
			return fmt.Errorf("%w: %q", ErrProjectNotFound, project)
		}
		if err := store.CreateProject(DefaultProject); err != nil {
			return err
		}
		p, _ = store.GetProject(DefaultProject)
	}
	if err := ValidateDelivery(h.Delivery); err != nil {
		return err
	}
	if err := ValidateIdentity(h.Identity); err != nil {
		return err
	}
	var u User
	if id, ok := store.LookupName(ident.User, h.Name); ok {
		u, _ = store.GetUser(id)
	} else {
		created, err := store.CreateUser(User{Name: h.Name})
		if err != nil {
			return err
		}
		u = created
	}
	if h.Login != "" && !slices.Contains(u.Logins, h.Login) {
		if err := store.SetUserLogins(u.ID, append(slices.Clone(u.Logins), h.Login)); err != nil {
			return err
		}
	}
	oidc := slices.Clone(u.OIDC)
	for _, o := range h.Identity {
		if !slices.Contains(oidc, o) {
			oidc = append(oidc, o)
		}
	}
	if len(oidc) != len(u.OIDC) {
		if err := store.SetUserOIDC(u.ID, oidc); err != nil {
			return err
		}
	}
	ms := Membership{ProjectID: p.ID, UserID: u.ID}
	for _, d := range h.Delivery {
		if d.Address != "" {
			ms.Delivery = append(ms.Delivery, DeliveryProfile{Service: d.Service, Address: d.Address})
		}
	}
	if err := store.PutMembership(ms); err != nil {
		return err
	}
	if h.Handle != "" {
		if err := linkPersonAccount(store, "linear", u.ID, Account{Handle: h.Handle}); err != nil {
			return err
		}
	}
	if ids := h.discordUserIDs(); len(ids) > 0 {
		if err := linkPersonAccount(store, "discord", u.ID, Account{ServiceUID: ids[0]}); err != nil {
			return err
		}
	}
	return nil
}

// linkPersonAccount makes the identity in tmpl, on the connection of kind
// (created if none), user's account there.
func linkPersonAccount(store Store, kind string, user ident.ID, tmpl Account) error {
	c, ok := store.ConnectionOfKind(kind)
	if !ok {
		var err error
		if c, err = store.CreateConnection(Connection{Kind: kind, Name: kind}); err != nil {
			return err
		}
	}
	existing, found := Account{}, false
	if tmpl.ServiceUID != "" {
		existing, found = store.AccountByUID(c.ID, tmpl.ServiceUID)
	} else {
		existing, found = store.AccountByHandle(c.ID, tmpl.Handle)
	}
	if found && existing.UserID != "" && existing.UserID != user {
		return fmt.Errorf("%w: %s identity %q", ErrAccountLinked, kind, accountName(existing))
	}
	tmpl.ConnectionID = c.ID
	a, err := store.UpsertAccount(tmpl)
	if err != nil {
		return err
	}
	return store.LinkAccount(a.ID, user)
}

// RemovePerson ends the membership in project of the user named name (a
// no-op when they're no member).
func RemovePerson(store Store, project, name string) error {
	p, ok := store.GetProject(project)
	if !ok {
		return fmt.Errorf("%w: %q", ErrProjectNotFound, project)
	}
	id, ok := store.LookupName(ident.User, name)
	if !ok || !store.IsMember(p.ID, id) {
		return nil
	}
	return store.RemoveMember(p.ID, id)
}
