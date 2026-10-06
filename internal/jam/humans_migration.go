package jam

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/ident"
)

// HumanMigration reports a humans → users migration: what it created or
// linked, and every decision a person should review (renames, sanitized
// names, skipped links). Notes carry names and ids only.
type HumanMigration struct {
	Users       int      `json:"users"`
	Memberships int      `json:"memberships"`
	Accounts    int      `json:"accounts"`
	Notes       []string `json:"notes,omitempty"`
}

// legacyAlias is one legacy_human_aliases row.
type legacyAlias struct {
	Project, Name string
	User          ident.ID
}

// roleWrite is a role rewritten by the migration (a renamed human's exact
// addressing entries).
type roleWrite struct {
	Project string
	Role    Role
}

// humanPlan is everything one humans → users migration writes. The planner
// computes it from the store's state; MemStore applies it, PostgresStore
// writes it in one transaction and then applies it.
type humanPlan struct {
	connections []Connection
	users       []User
	accounts    []Account
	memberships []Membership
	aliases     []legacyAlias
	projects    []Project // docs with Roster.Humans cleared (and renamed refs rewritten)
	roles       []roleWrite
	actors      []Actor
	instances   []Instance
	report      HumanMigration
}

// strongKeys are a human's strong identities: its login, OIDC bindings and
// discord user ids. Two humans sharing one are the same person.
func strongKeys(h Human) []string {
	var keys []string
	if h.Login != "" {
		keys = append(keys, "login\x00"+h.Login)
	}
	for _, o := range h.Identity {
		keys = append(keys, "oidc\x00"+o.Issuer+"\x00"+o.Subject)
	}
	for _, id := range h.discordUserIDs() {
		keys = append(keys, "discord\x00"+id)
	}
	return keys
}

// unionFind groups the migration's humans.
type unionFind []int

func (u unionFind) find(i int) int {
	for u[i] != i {
		u[i] = u[u[i]]
		i = u[i]
	}
	return i
}

func (u unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if rb < ra {
		ra, rb = rb, ra
	}
	u[rb] = ra // the earlier human stays the root
}

// sanitizeEntityName makes name a valid entity name: forbidden characters
// become "-", it is cut to 64 bytes, and an empty or id-shaped result is "user".
func sanitizeEntityName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if strings.ContainsRune(":,*?[]\\/", r) || r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			b.WriteByte('-')
		} else {
			b.WriteRune(r)
		}
	}
	out := b.String()
	for len(out) > 64 {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	if ValidateEntityName(out) != nil {
		return "user"
	}
	return out
}

// withSuffix appends suffix to a valid name, trimming the name (never the
// suffix) to stay within 64 bytes, so distinct suffixes stay distinct.
func withSuffix(name, suffix string) string {
	suffix = sanitizeEntityName("x" + suffix)[1:]
	if len(suffix) > 63 {
		suffix = suffix[len(suffix)-63:]
	}
	for len(name)+len(suffix) > 64 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return sanitizeEntityName(name + suffix)
}

// planHumanMigration plans the humans → users migration over every stored
// project doc's Roster.Humans (spec §6 and plan 1a-3a). Caller holds mu. It
// reads but never mutates memState.
func (m *memState) planHumanMigration() humanPlan {
	var plan humanPlan
	note := func(format string, args ...any) {
		plan.report.Notes = append(plan.report.Notes, fmt.Sprintf(format, args...))
	}

	type item struct {
		project Project
		h       Human
	}
	var items []item
	var projectsWithHumans []Project
	for _, name := range slices.Sorted(mapsKeys(m.projects)) {
		p := m.projects[name]
		if len(p.Roster.Humans) == 0 {
			continue
		}
		projectsWithHumans = append(projectsWithHumans, p)
		for _, h := range p.Roster.Humans {
			items = append(items, item{p, h})
		}
	}
	if len(items) == 0 {
		return plan
	}

	// 1. Group: union by strong identity, then weak humans by name.
	uf := make(unionFind, len(items))
	for i := range uf {
		uf[i] = i
	}
	byKey := map[string]int{}
	for i, it := range items {
		for _, k := range strongKeys(it.h) {
			if j, ok := byKey[k]; ok {
				uf.union(i, j)
			} else {
				byKey[k] = i
			}
		}
	}
	strongRootsByName := map[string][]int{}
	for i, it := range items {
		if len(strongKeys(it.h)) > 0 {
			r := uf.find(i)
			if !slices.Contains(strongRootsByName[it.h.Name], r) {
				strongRootsByName[it.h.Name] = append(strongRootsByName[it.h.Name], r)
			}
		}
	}
	weakByName := map[string]int{}
	for i, it := range items {
		if len(strongKeys(it.h)) > 0 {
			continue
		}
		if roots := strongRootsByName[it.h.Name]; len(roots) == 1 {
			uf.union(i, roots[0])
			continue
		}
		if j, ok := weakByName[it.h.Name]; ok {
			uf.union(i, j)
		} else {
			weakByName[it.h.Name] = i
		}
	}
	groups := map[int][]int{}
	var roots []int
	for i := range items {
		r := uf.find(i)
		if _, ok := groups[r]; !ok {
			roots = append(roots, r)
		}
		groups[r] = append(groups[r], i)
	}
	slices.Sort(roots)

	// 2. Users. Names taken by live users or by earlier groups.
	taken := map[string]ident.ID{}
	for _, u := range m.users {
		if u.Status == StatusLive {
			taken[u.Name] = u.ID
		}
	}
	userOf := map[int]User{} // root → user
	for _, r := range roots {
		members := groups[r]
		first := items[members[0]]
		var keys []string
		for _, i := range members {
			keys = append(keys, strongKeys(items[i].h)...)
		}
		u, found := m.existingUserFor(keys, first.h.Name)
		if found {
			if prior, dup := userOfByID(userOf, u.ID); dup {
				u = prior
			}
		} else {
			name := sanitizeEntityName(first.h.Name)
			if name != first.h.Name {
				note("human %q in project %q: name %q is not a valid user name; using %q", first.h.Name, first.project.Name, first.h.Name, name)
			}
			if _, clash := taken[name]; clash {
				base := name
				name = withSuffix(base, "-"+first.project.Name)
				for n := 2; ; n++ {
					if _, clash := taken[name]; !clash {
						break
					}
					name = withSuffix(base, fmt.Sprintf("-%s-%d", first.project.Name, n))
				}
				note("human %q in project %q is a different person from an existing user of that name; renamed to %q", first.h.Name, first.project.Name, name)
			}
			u = User{ID: ident.New(ident.User), Name: name, Status: StatusLive}
			plan.report.Users++
		}
		taken[u.Name] = u.ID
		for _, i := range members {
			h := items[i].h
			if h.Login != "" && !slices.Contains(u.Logins, h.Login) {
				if other, held := m.loginHolder(h.Login); held && other != u.ID {
					note("login %q of human %q in project %q is held by user %s; not added", h.Login, h.Name, items[i].project.Name, other)
				} else {
					u.Logins = append(u.Logins, h.Login)
				}
			}
			for _, o := range h.Identity {
				if slices.Contains(u.OIDC, o) {
					continue
				}
				if other, held := m.oidcHolder(o); held && other != u.ID {
					note("oidc identity %s:%s of human %q is held by user %s; not added", o.Issuer, o.Subject, h.Name, other)
					continue
				}
				u.OIDC = append(u.OIDC, o)
			}
		}
		userOf[r] = u
	}
	for _, r := range roots {
		plan.users = append(plan.users, userOf[r])
	}

	// 3. Memberships, accounts, aliases, renames.
	conns := map[string]Connection{}
	connFor := func(kind string) Connection {
		if c, ok := conns[kind]; ok {
			return c
		}
		c, ok := m.liveConnectionNamed(kind)
		if !ok {
			c = Connection{ID: ident.New(ident.Connection), Kind: kind, Name: kind, Status: StatusLive}
			plan.connections = append(plan.connections, c)
		}
		conns[kind] = c
		return c
	}
	planned := map[string]Account{} // conn id + uid/handle key → account in this plan
	link := func(kind, uid, handle string, u User, h Human, project string) {
		c := connFor(kind)
		key := string(c.ID) + "\x00" + uid + "\x00" + handle
		a, ok := planned[key]
		if !ok {
			if uid != "" {
				a, ok = m.accountBy(c.ID, func(a Account) bool { return a.ServiceUID == uid })
			} else {
				a, ok = m.accountBy(c.ID, func(a Account) bool { return a.Handle == handle })
			}
		}
		if ok && a.UserID != "" && a.UserID != u.ID {
			note("%s account %q of human %q in project %q is linked to user %s; left as is", kind, cmp.Or(uid, handle), h.Name, project, a.UserID)
			return
		}
		if !ok {
			a = Account{ID: ident.New(ident.Account), ConnectionID: c.ID, ServiceUID: uid, Handle: handle, Status: StatusLive}
			plan.report.Accounts++
		}
		a.UserID = u.ID
		planned[key] = a
	}
	memberships := map[[2]ident.ID]Membership{}
	var memberOrder [][2]ident.ID
	renames := map[string]map[string]string{} // project → old human name → user name
	for _, r := range roots {
		u := userOf[r]
		for _, i := range groups[r] {
			it := items[i]
			key := [2]ident.ID{it.project.ID, u.ID}
			ms, ok := memberships[key]
			if !ok {
				ms = Membership{ProjectID: it.project.ID, UserID: u.ID}
				memberOrder = append(memberOrder, key)
			}
			for _, d := range it.h.Delivery {
				if d.Address == "" || slices.ContainsFunc(ms.Delivery, func(e DeliveryProfile) bool { return e.Service == d.Service }) {
					continue
				}
				ms.Delivery = append(ms.Delivery, DeliveryProfile{Service: d.Service, Address: d.Address})
			}
			memberships[key] = ms
			if it.h.Handle != "" {
				link("linear", "", it.h.Handle, u, it.h, it.project.Name)
			}
			for _, id := range it.h.discordUserIDs() {
				link("discord", id, "", u, it.h, it.project.Name)
			}
			plan.aliases = append(plan.aliases, legacyAlias{Project: it.project.Name, Name: it.h.Name, User: u.ID})
			if u.Name != it.h.Name {
				if renames[it.project.Name] == nil {
					renames[it.project.Name] = map[string]string{}
				}
				renames[it.project.Name][it.h.Name] = u.Name
			}
		}
	}
	for _, k := range memberOrder {
		plan.memberships = append(plan.memberships, memberships[k])
	}
	plan.report.Memberships = len(plan.memberships)
	perUser := map[[2]ident.ID]int{} // (connection, user) → linked accounts
	for _, k := range slices.Sorted(mapsKeys(planned)) {
		a := planned[k]
		plan.accounts = append(plan.accounts, a)
		perUser[[2]ident.ID{a.ConnectionID, a.UserID}]++
	}
	for _, r := range roots {
		u := userOf[r]
		for _, kind := range []string{"linear", "discord"} {
			if c, ok := conns[kind]; ok && perUser[[2]ident.ID{c.ID, u.ID}] > 1 {
				note("user %q has several %s handles or ids from different projects; the roster shows one of them", u.Name, kind)
			}
		}
	}

	// 4. Project docs lose their humans; renamed humans' exact references in
	// the same project follow the new name.
	for _, p := range projectsWithHumans {
		p = copyProject(p)
		p.Roster.Humans = nil
		rn := renames[p.Name]
		p.Escalation = renameTiers(p.Escalation, rn)
		for cat, tiers := range p.EscalationByCategory {
			p.EscalationByCategory[cat] = renameTiers(tiers, rn)
		}
		plan.projects = append(plan.projects, p)
	}
	for _, project := range slices.Sorted(mapsKeys(renames)) {
		rn := renames[project]
		for _, name := range slices.Sorted(mapsKeys(m.roles[project])) {
			r := m.roles[project][name]
			if addr, changed := renameTargets(r.Scope.Addressing, rn, func(glob string) {
				note("role %s/%s addressing glob %q matched a renamed human; not rewritten", project, name, glob)
			}); changed {
				r.Scope.Addressing = addr
				plan.roles = append(plan.roles, roleWrite{Project: project, Role: r})
			}
		}
		for _, h := range slices.Sorted(mapsKeys(m.actors)) {
			a := m.actors[h]
			changed := false
			grants := slices.Clone(a.Grants)
			for gi, g := range grants {
				if orDefaultProject(g.Project) != project || g.Overrides == nil {
					continue
				}
				if addr, ch := renameTargets(g.Overrides.Addressing, rn, func(glob string) {
					note("actor %q grant addressing glob %q matched a renamed human; not rewritten", a.ID, glob)
				}); ch {
					o := *g.Overrides
					o.Addressing = addr
					grants[gi].Overrides = &o
					changed = true
				}
			}
			if changed {
				a.Grants = grants
				plan.actors = append(plan.actors, a)
			}
		}
		for _, id := range slices.Sorted(mapsKeys(m.instances)) {
			inst := m.instances[id]
			if orDefaultProject(inst.Project) != project {
				continue
			}
			if to, ok := rn[inst.Owner]; ok && inst.Owner != "" {
				inst.Owner = to
				plan.instances = append(plan.instances, inst)
			}
		}
	}
	return plan
}

// existingUserFor finds the live registry user a group of humans already is:
// one holding any of its strong identities, else (for a group with none) the
// live user of its name.
func (m *memState) existingUserFor(keys []string, name string) (User, bool) {
	for _, k := range keys {
		parts := strings.Split(k, "\x00")
		switch parts[0] {
		case "login":
			if id, ok := m.loginHolder(parts[1]); ok {
				return copyUser(m.users[id]), true
			}
		case "oidc":
			if id, ok := m.oidcHolder(OIDCIdentity{Issuer: parts[1], Subject: parts[2]}); ok {
				return copyUser(m.users[id]), true
			}
		case "discord":
			if c, ok := m.liveConnectionNamed("discord"); ok {
				if a, ok := m.accountBy(c.ID, func(a Account) bool { return a.ServiceUID == parts[1] }); ok && a.UserID != "" {
					if u, ok := m.users[a.UserID]; ok && u.Status == StatusLive {
						return copyUser(u), true
					}
				}
			}
		}
	}
	// By name: always for a group with no strong identity; for one with, only
	// a user holding none of its own (nothing to conflict with).
	if u, ok := m.liveUserNamed(name); ok && (len(keys) == 0 || !m.hasStrongIdentity(u)) {
		return copyUser(u), true
	}
	return User{}, false
}

// hasStrongIdentity reports whether u holds a login, an OIDC binding or a
// linked discord account. Caller holds mu.
func (m *memState) hasStrongIdentity(u User) bool {
	if len(u.Logins) > 0 || len(u.OIDC) > 0 {
		return true
	}
	if c, ok := m.liveConnectionNamed("discord"); ok {
		_, linked := m.accountBy(c.ID, func(a Account) bool { return a.UserID == u.ID && a.ServiceUID != "" })
		return linked
	}
	return false
}

func (m *memState) loginHolder(login string) (ident.ID, bool) {
	for _, u := range m.users {
		if u.Status == StatusLive && slices.Contains(u.Logins, login) {
			return u.ID, true
		}
	}
	return "", false
}

func (m *memState) oidcHolder(o OIDCIdentity) (ident.ID, bool) {
	for _, u := range m.users {
		if u.Status == StatusLive && slices.Contains(u.OIDC, o) {
			return u.ID, true
		}
	}
	return "", false
}

// userOfByID finds a user already assigned to an earlier group (two groups
// that both resolve to one existing user share its accumulated value).
func userOfByID(userOf map[int]User, id ident.ID) (User, bool) {
	for _, u := range userOf {
		if u.ID == id {
			return u, true
		}
	}
	return User{}, false
}

// renameTiers rewrites exact "human:<old>" tier targets.
func renameTiers(tiers []EscalationTier, rn map[string]string) []EscalationTier {
	if len(rn) == 0 {
		return tiers
	}
	for i := range tiers {
		tiers[i].Targets, _ = renameTargets(tiers[i].Targets, rn, nil)
	}
	return tiers
}

// renameTargets rewrites exact "human:<old>" entries to "human:<new>" and
// reports (via glob) any other pattern that matched an old name.
func renameTargets(targets []string, rn map[string]string, glob func(string)) ([]string, bool) {
	if len(rn) == 0 {
		return targets, false
	}
	out := slices.Clone(targets)
	changed := false
	for i, t := range out {
		name, isHuman := strings.CutPrefix(t, "human:")
		if !isHuman {
			continue
		}
		if to, ok := rn[name]; ok {
			out[i] = "human:" + to
			changed = true
			continue
		}
		if glob != nil {
			for old := range rn {
				if ok, _ := path.Match(t, "human:"+old); ok {
					glob(t)
					break
				}
			}
		}
	}
	return out, changed
}

// applyHumanPlan applies a planned migration to the cache. Caller holds mu.Lock.
func (m *memState) applyHumanPlan(p humanPlan) {
	for _, c := range p.connections {
		m.applyPutConnection(c)
	}
	for _, u := range p.users {
		m.applyPutUser(u)
	}
	for _, a := range p.accounts {
		m.applyPutAccount(a)
	}
	for _, ms := range p.memberships {
		m.applyPutMembership(ms)
	}
	for _, al := range p.aliases {
		if m.aliases[al.Project] == nil {
			m.aliases[al.Project] = map[string]ident.ID{}
		}
		m.aliases[al.Project][al.Name] = al.User
	}
	for _, pr := range p.projects {
		m.applyPutProject(pr)
	}
	for _, rw := range p.roles {
		m.applyPutRole(rw.Project, rw.Role)
	}
	for _, a := range p.actors {
		m.applyPutActor(a)
	}
	for _, inst := range p.instances {
		m.applyPutInstance(inst)
	}
}

// mapsKeys is maps.Keys for slices.Sorted call sites over string-keyed maps.
func mapsKeys[V any](mm map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range mm {
			if !yield(k) {
				return
			}
		}
	}
}
