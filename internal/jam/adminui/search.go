package adminui

import (
	"html/template"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/studio"
)

const (
	searchMinLen   = 2  // shorter queries match too much to be useful
	searchGroupMax = 20 // hits shown per group; the count says how many matched
	searchSquawks  = 10 // newest matching squawks shown; Intercom has the rest
)

// searchHit is one result: Title is its name, Sub its context, both
// highlighted in the page. Exact marks a hit whose name is the whole query.
type searchHit struct {
	Title, Sub, URL string
	Exact           bool
}

// searchGroup is one entity kind's hits. Count is every match; Hits is capped.
type searchGroup struct {
	Key, Name string
	Hits      []searchHit
	Count     int
	MoreURL   string // where the rest are (squawks → Intercom)
}

type searchData struct {
	Title    string
	Q        string
	TooShort bool
	Groups   []searchGroup
	Total    int
}

// matcher reports whether any field contains q, case-insensitively.
type matcher string

func (m matcher) any(fields ...string) bool {
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), string(m)) {
			return true
		}
	}
	return false
}

func (m matcher) exact(names ...string) bool {
	for _, n := range names {
		if strings.ToLower(n) == string(m) {
			return true
		}
	}
	return false
}

func (g *searchGroup) add(h searchHit) {
	g.Count++
	if len(g.Hits) < searchGroupMax {
		g.Hits = append(g.Hits, h)
	}
}

// search runs q over every entity the admin UI shows, plus squawk bodies.
func search(store jam.Store, msgs SquawkReader, q string) searchData {
	d := searchData{Title: "Search", Q: q}
	if len([]rune(strings.TrimSpace(q))) < searchMinLen {
		d.TooShort = strings.TrimSpace(q) != ""
		return d
	}
	m := matcher(strings.ToLower(strings.TrimSpace(q)))

	projects := searchGroup{Key: "projects", Name: "Projects"}
	users := searchGroup{Key: "users", Name: "Users"}
	channels := searchGroup{Key: "channels", Name: "Rooms"}
	for _, u := range store.ListUsers() {
		v := jam.NewUserView(store, u)
		fields := append([]string{u.Name}, u.Logins...)
		for _, id := range u.OIDC {
			fields = append(fields, id.Subject)
		}
		for _, a := range v.Accounts {
			fields = append(fields, a.Handle, a.ServiceUID)
		}
		if m.any(fields...) {
			sub := strings.Join(v.Projects, ", ")
			for _, a := range v.Accounts {
				if a.Handle != "" {
					sub += " · @" + a.Handle
					break
				}
			}
			if len(u.Logins) > 0 {
				sub += " · login " + u.Logins[0]
			}
			users.add(searchHit{Title: u.Name, Sub: sub, URL: userURL(u.ID), Exact: m.exact(u.Name)})
		}
	}
	for _, name := range store.ListProjects() {
		p, _ := store.GetProject(name)
		if m.any(name) {
			projects.add(searchHit{Title: name, URL: projectURL(name), Exact: m.exact(name),
				Sub: pluralCount(len(store.ListRoles(name)), "role")})
		}
		for _, r := range jam.ListRooms(store, p) {
			if m.any(r.Name, r.Ref) {
				channels.add(searchHit{Title: "channel:" + r.Name, Sub: name + " · " + r.Connection + " " + r.Ref, URL: projectURL(name)})
			}
		}
	}

	roles := searchGroup{Key: "roles", Name: "Roles"}
	for _, r := range roleRows(store) {
		if m.any(append([]string{r.Project + "/" + r.Name, r.Kit}, r.Destinations...)...) {
			sub := "destinations: " + strings.Join(r.Destinations, ", ")
			if len(r.Destinations) == 0 {
				sub = "no destinations"
			}
			if r.Kit != "" {
				sub += " · kit " + r.Kit
			}
			roles.add(searchHit{Title: r.Project + "/" + r.Name, Sub: sub, URL: roleURL(r.Project, r.Name),
				Exact: m.exact(r.Project + "/" + r.Name)})
		}
	}

	studios := searchGroup{Key: "studios", Name: "Studios"}
	for _, i := range store.ListInstances() {
		i.Project = jam.ProjectName(store, i.Project)
		if m.any(i.ActorID, i.Unit, i.Owner, i.Name, i.Project+"/"+i.Role) {
			sub := i.Project + "/" + i.Role + " · " + string(i.Phase)
			if i.Unit != "" {
				sub += " · " + i.Unit
			}
			if i.Owner != "" {
				sub += " · owner " + i.Owner
			}
			studios.add(searchHit{Title: i.ActorID, Sub: sub, URL: agentURL(i.ActorID), Exact: m.exact(i.ActorID)})
		}
	}

	actors := searchGroup{Key: "actors", Name: "Actors"}
	for _, a := range store.ListActors() {
		grants := make([]string, len(a.Grants))
		for i, g := range a.Grants {
			grants[i] = jam.ProjectName(store, g.Project) + "/" + g.Role
		}
		if m.any(append([]string{a.ID}, grants...)...) {
			actors.add(searchHit{Title: a.ID, Sub: "grants: " + strings.Join(grants, ", "), URL: agentURL(a.ID), Exact: m.exact(a.ID)})
		}
	}

	kits := searchGroup{Key: "kits", Name: "Kits"}
	for _, k := range store.ListKits() {
		fields := []string{k.Name}
		sub := "current v" + itoa(k.Current)
		if sk, err := studio.ParseStudioKit([]byte(k.Versions[k.Current])); err == nil {
			fields = append(append(fields, sk.Prompt), sk.Egress...)
			if sk.Prompt != "" {
				sub += " · " + clip(sk.Prompt, 90)
			}
		}
		if m.any(fields...) {
			kits.add(searchHit{Title: k.Name, Sub: sub, URL: kitURL(k.Name), Exact: m.exact(k.Name)})
		}
	}

	dests := searchGroup{Key: "destinations", Name: "Destinations"}
	for _, x := range store.ListDestinations() {
		envKeys := slices.Sorted(maps.Keys(x.ClientEnv()))
		if m.any(append([]string{x.Name, x.Route, x.Upstream}, envKeys...)...) {
			dests.add(searchHit{Title: x.Name, Sub: x.Route + " → " + x.Upstream, URL: destURL(x.Name), Exact: m.exact(x.Name)})
		}
	}

	squawks := searchGroup{Key: "squawks", Name: "Squawks", MoreURL: "/ui/intercom?q=" + url.QueryEscape(q)}
	if msgs != nil {
		var found []Logged
		for _, legacy := range []bool{false, true} {
			for _, s := range msgs.Squawks(legacy) {
				if m.any(s.Body) {
					found = append(found, s)
				}
			}
		}
		sort.SliceStable(found, func(i, j int) bool { return found[i].At.After(found[j].At) })
		squawks.Count = len(found)
		for _, s := range found[:min(len(found), searchSquawks)] {
			squawks.Hits = append(squawks.Hits, searchHit{
				Title: clip(s.Body, 160),
				Sub:   s.At.Format("Jan 2 15:04") + " · " + s.From + " → " + squawkWhere(s),
				URL:   "/ui/intercom?q=" + url.QueryEscape(q),
			})
		}
	}

	for _, g := range []searchGroup{studios, roles, projects, kits, dests, actors, users, channels, squawks} {
		if g.Count > 0 {
			d.Groups = append(d.Groups, g)
			d.Total += g.Count
		}
	}
	return d
}

// squawkWhere is where a squawk went: its channel, or a legacy squawk's
// recipients.
func squawkWhere(s Logged) string {
	if s.Channel != "" {
		return s.Channel
	}
	out := make([]string, len(s.To))
	for i, t := range s.To {
		out[i] = t.Target
	}
	return strings.Join(out, ", ")
}

func pluralCount(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return itoa(n) + " " + noun + "s"
}

// exactJump is the URL of the single entity whose name is exactly the query,
// or "" when there are none or several.
func (d searchData) exactJump() string {
	jump := ""
	for _, g := range d.Groups {
		for _, h := range g.Hits {
			if h.Exact {
				if jump != "" {
					return ""
				}
				jump = h.URL
			}
		}
	}
	return jump
}

// highlight escapes s and wraps each case-insensitive occurrence of q in <mark>.
func highlight(s, q string) template.HTML {
	q = strings.TrimSpace(q)
	if q == "" {
		return template.HTML(template.HTMLEscapeString(s))
	}
	lower, lq := strings.ToLower(s), strings.ToLower(q)
	var b strings.Builder
	for {
		i := strings.Index(lower, lq)
		if i < 0 || len(lower) != len(s) { // a case fold changed byte lengths: don't risk misaligned marks
			b.WriteString(template.HTMLEscapeString(s))
			break
		}
		b.WriteString(template.HTMLEscapeString(s[:i]))
		b.WriteString("<mark>" + template.HTMLEscapeString(s[i:i+len(lq)]) + "</mark>")
		s, lower = s[i+len(lq):], lower[i+len(lq):]
	}
	return template.HTML(b.String())
}

func registerSearch(mux *http.ServeMux, store jam.Store, msgs SquawkReader) {
	mux.HandleFunc("GET /ui/search", func(w http.ResponseWriter, r *http.Request) {
		d := search(store, msgs, r.URL.Query().Get("q"))
		if r.URL.Query().Get("go") != "" { // Enter in the top-bar box: jump on a single exact match
			if u := d.exactJump(); u != "" {
				http.Redirect(w, r, u, http.StatusSeeOther)
				return
			}
		}
		if r.Header.Get("HX-Request") == "true" {
			renderFragment(w, "search", "search-results", d)
			return
		}
		render(w, "search", d)
	})
}

func itoa(n int) string { return strconv.Itoa(n) }
