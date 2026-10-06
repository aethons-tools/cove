package jam

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
)

// The roster view (intercom slice 1a-3a): the registry — users, memberships,
// accounts — is the source of truth for the people on a project's roster, and
// the pre-registry Human API is served from it. A project doc's stored
// Roster.Humans is always empty once the humans migration has run; the view
// fills it on the way out. This file goes away when the last consumer moves
// to the registry (1a-3e).

// ErrAccountLinked refuses binding a service identity (a tracker handle, a
// Discord user id) that already belongs to another user.
var ErrAccountLinked = errors.New("account is linked to another user")

// viewProject returns a copy of p with its roster humans filled from the
// registry. Caller holds mu.
func (m *memState) viewProject(p Project) Project {
	p = copyProject(p)
	p.Roster.Humans = m.rosterHumans(p.ID)
	return p
}

// rosterHumans is the roster view of project's members, sorted by name.
// Caller holds mu.
func (m *memState) rosterHumans(project ident.ID) []Human {
	var out []Human
	for uid, ms := range m.members[project] {
		if u, ok := m.users[uid]; ok && u.Status == StatusLive {
			out = append(out, m.humanView(u, ms))
		}
	}
	slices.SortFunc(out, func(a, b Human) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// humanView renders one member as a roster Human: the user's name, first
// login and OIDC bindings; the handle of their linear account; the
// membership's delivery, with their discord account's uid on the discord
// profile. Caller holds mu.
func (m *memState) humanView(u User, ms Membership) Human {
	h := Human{UserID: u.ID, Name: u.Name, Identity: slices.Clone(u.OIDC), Delivery: slices.Clone(ms.Delivery)}
	if len(u.Logins) > 0 {
		h.Login = u.Logins[0]
	}
	if a, ok := m.linkedAccount("linear", u.ID, func(a Account) bool { return a.Handle != "" }); ok {
		h.Handle = a.Handle
	}
	if a, ok := m.linkedAccount("discord", u.ID, func(a Account) bool { return a.ServiceUID != "" }); ok {
		i := slices.IndexFunc(h.Delivery, func(d DeliveryProfile) bool { return d.Service == "discord" })
		if i < 0 {
			h.Delivery = append(h.Delivery, DeliveryProfile{Service: "discord"})
			i = len(h.Delivery) - 1
		}
		h.Delivery[i].UserID = a.ServiceUID
	}
	return h
}

// linkedAccount is the user's live account (lowest id, for determinism) on
// the implicit connection named conn that satisfies match. Caller holds mu.
func (m *memState) linkedAccount(conn string, user ident.ID, match func(Account) bool) (Account, bool) {
	c, ok := m.connectionOfKind(conn)
	if !ok {
		return Account{}, false
	}
	var best Account
	found := false
	for _, a := range m.accounts {
		if a.ConnectionID == c.ID && a.UserID == user && a.Status == StatusLive && match(a) && (!found || a.ID < best.ID) {
			best, found = a, true
		}
	}
	return best, found
}

// prepareAddHuman plans AddHuman as registry writes on the Jam-wide user named
// h.Name (created if new). It never strips identity: h's login and OIDC
// bindings are added to the user's; a given handle or Discord user id
// becomes the user's (replacing their previous one), an absent one leaves
// theirs as is. The user becomes a member of the project with h's delivery
// addresses (this project only). Caller holds mu.
func (m *memState) prepareAddHuman(project string, h Human) (humanPlan, error) {
	var plan humanPlan
	p, created, err := m.requireProject(project)
	if err != nil {
		return plan, err
	}
	if created {
		plan.projects = append(plan.projects, p)
	}
	if err := ValidateDelivery(h.Delivery); err != nil {
		return plan, err
	}
	u, ok := m.liveUserNamed(h.Name)
	if ok {
		u = copyUser(u)
	} else {
		if err := ValidateEntityName(h.Name); err != nil {
			return plan, err
		}
		u = User{ID: ident.New(ident.User), Name: h.Name, Status: StatusLive}
	}
	if h.Login != "" && !slices.Contains(u.Logins, h.Login) {
		u.Logins = append(u.Logins, h.Login)
	}
	for _, o := range h.Identity {
		if !slices.Contains(u.OIDC, o) {
			u.OIDC = append(u.OIDC, o)
		}
	}
	if err := ValidateIdentity(h.Identity); err != nil {
		return plan, err
	}
	if err := m.checkLogins(u.Logins, u.ID); err != nil {
		return plan, err
	}
	if err := m.checkOIDC(u.OIDC, u.ID); err != nil {
		return plan, err
	}
	plan.users = append(plan.users, u)

	ms := Membership{ProjectID: p.ID, UserID: u.ID}
	for _, d := range h.Delivery {
		if d.Address != "" {
			ms.Delivery = append(ms.Delivery, DeliveryProfile{Service: d.Service, Address: d.Address})
		}
	}
	plan.memberships = append(plan.memberships, ms)

	var discordUID string
	if ids := h.discordUserIDs(); len(ids) > 0 {
		discordUID = ids[0]
	}
	if h.Handle != "" {
		if err := m.planUserAccount(&plan, "linear", u.ID, func(a Account) bool { return a.Handle == h.Handle }, Account{Handle: h.Handle}); err != nil {
			return plan, err
		}
	}
	if discordUID != "" {
		if err := m.planUserAccount(&plan, "discord", u.ID, func(a Account) bool { return a.ServiceUID == discordUID }, Account{ServiceUID: discordUID}); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

// planUserAccount makes the identity match selects on the implicit
// connection named kind the user's account there, creating the account (from
// tmpl) or the connection if absent, and unlinks the user's other accounts on
// that connection. ErrAccountLinked when the identity belongs to another user.
func (m *memState) planUserAccount(plan *humanPlan, kind string, user ident.ID, match func(Account) bool, tmpl Account) error {
	c, ok := m.connectionOfKind(kind)
	if !ok {
		c = Connection{ID: ident.New(ident.Connection), Kind: kind, Name: kind, Status: StatusLive}
		plan.connections = append(plan.connections, c)
	}
	a, found := m.accountBy(c.ID, match)
	switch {
	case found && a.UserID != "" && a.UserID != user:
		return fmt.Errorf("%w: %s identity %q", ErrAccountLinked, kind, accountName(a))
	case !found:
		a = tmpl
		a.ID, a.ConnectionID, a.Status = ident.New(ident.Account), c.ID, StatusLive
	}
	a.UserID = user
	for _, other := range m.accounts {
		if other.ConnectionID == c.ID && other.UserID == user && other.ID != a.ID {
			other.UserID = ""
			plan.accounts = append(plan.accounts, other)
		}
	}
	plan.accounts = append(plan.accounts, a)
	return nil
}

// prepareRemoveHuman resolves RemoveHuman to the membership it ends (none when
// the name is no member: a no-op, as before). Caller holds mu.
func (m *memState) prepareRemoveHuman(project, name string) (Membership, bool, error) {
	p, ok := m.projects[project]
	if !ok {
		return Membership{}, false, fmt.Errorf("project %q not found", project)
	}
	u, ok := m.liveUserNamed(name)
	if !ok {
		return Membership{}, false, nil
	}
	ms, ok := m.members[p.ID][u.ID]
	return ms, ok, nil
}
