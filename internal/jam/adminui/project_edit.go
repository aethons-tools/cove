package adminui

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// memberRow is a project member: their user, their tracker handle,
// and their delivery here in the edit form's line syntax.
type memberRow struct {
	UserID       ident.ID
	Name         string
	Handle       string
	Delivery     []jam.DeliveryProfile
	DeliverySpec string
}

// targetView is one escalation target; Unknown marks one that names nobody on
// the project's roster (flagged, never blocked).
type targetView struct {
	Text    string
	Unknown bool
}

type tierView struct {
	Targets []targetView
	Timeout string
}

// chainView is one escalation chain with its tiers in the editor's syntax.
type chainView struct {
	Category string // "" = the default chain
	Tiers    []tierView
	Spec     string // one targets@timeout per line
}

func lines[T any](xs []T, f func(T) string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = f(x)
	}
	return strings.Join(out, "\n")
}

// knownTargets is the set of escalation targets that name someone in a
// project: user:<name|id> for each member (human: is the pre-registry alias),
// channel:<name> for each room.
func knownTargets(members []jam.Member, rooms []jam.RoomView) map[string]bool {
	known := map[string]bool{}
	for _, m := range members {
		for _, k := range []string{"user:", "human:"} {
			known[k+m.User.Name], known[k+string(m.User.ID)] = true, true
		}
	}
	for _, r := range rooms {
		known["channel:"+r.Name] = true
	}
	return known
}

func chain(category string, tiers []jam.EscalationTier, known map[string]bool) chainView {
	c := chainView{Category: category, Spec: lines(tiers, jam.FormatEscalationTierSpec)}
	for _, t := range tiers {
		tv := tierView{Timeout: fmtDur(t.Timeout)}
		for _, x := range t.Targets {
			tv.Targets = append(tv.Targets, targetView{Text: x, Unknown: !known[x]})
		}
		c.Tiers = append(c.Tiers, tv)
	}
	return c
}

// splitSpecLines returns the non-blank, trimmed lines of a textarea.
func splitSpecLines(s string) []string {
	var out []string
	for l := range strings.Lines(s) {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// memberDeliveryFromForm parses a member's delivery lines (service:address;
// a Discord user id is an account, bound on the user's page).
func memberDeliveryFromForm(r *http.Request) ([]jam.DeliveryProfile, error) {
	var out []jam.DeliveryProfile
	for _, l := range splitSpecLines(r.FormValue("delivery")) {
		p, err := jam.ParseDeliverySpec(l)
		if err != nil {
			return nil, badRequest("delivery " + `"` + l + `": ` + err.Error())
		}
		if p.UserID != "" {
			return nil, badRequest("delivery " + `"` + l + `": a Discord user id is an account — add it on the user's page`)
		}
		out = append(out, p)
	}
	return out, nil
}

// registerProjectEdits mounts the project page's section writes. Each answers
// with the re-rendered section body plus the tree, out of band (a write can
// change what the tree lists, e.g. its rooms).
func registerProjectEdits(mux *http.ServeMux, store jam.Store, img jam.ImageResolver, msgs SquawkReader, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	// edit applies one project write and answers with the section it belongs
	// to re-rendered, for the page's #project swap.
	edit := func(what string, section projectSection, apply func(r *http.Request, project string) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !guardWrite(w, r) {
				return
			}
			if err := r.ParseForm(); err != nil {
				renderError(w, http.StatusBadRequest, "invalid form")
				return
			}
			project := r.PathValue("project")
			if _, ok := store.GetProject(project); !ok {
				renderError(w, http.StatusNotFound, "project "+`"`+project+`"`+" not found")
				return
			}
			if err := apply(r, project); err != nil {
				renderError(w, jam.WriteStatus(err, http.StatusBadRequest), err.Error())
				return
			}
			log.Info("ui project "+what, "operator", jam.OperatorID(r), "project", project)
			d, ok := buildProjectDetail(store, img, msgs, project, section)
			if !ok {
				renderError(w, http.StatusNotFound, "project no longer exists")
				return
			}
			d.Tree.OOB = true
			renderFragments(w, "project", namedFragment{"project-" + string(section), d}, namedFragment{"project-tree-nav", d.Tree})
		}
	}

	mux.HandleFunc("POST /ui/projects/{project}/context", edit("context set", sectionOverview, func(r *http.Request, project string) error {
		b, err := parseContextForm(r)
		if err != nil {
			return err
		}
		return jam.SetProjectContextChecked(store, project, b)
	}))
	mux.HandleFunc("DELETE /ui/projects/{project}/context", edit("context cleared", sectionOverview, func(r *http.Request, project string) error {
		return jam.SetProjectContextChecked(store, project, jam.ContextBody{})
	}))

	mux.HandleFunc("POST /ui/projects/{project}/members", edit("member put", sectionMembers, func(r *http.Request, project string) error {
		uid, err := jam.ResolveRegistryRef(store, ident.User, strings.TrimSpace(r.FormValue("user")))
		if err != nil {
			return registryErr(err)
		}
		delivery, err := memberDeliveryFromForm(r)
		if err != nil {
			return err
		}
		p, _ := store.GetProject(project)
		// "Add member" for someone already a member, with no delivery given,
		// keeps their delivery (the per-member Edit form replaces it).
		if r.FormValue("add") != "" && delivery == nil {
			if ms, ok := store.GetMembership(p.ID, uid); ok {
				delivery = ms.Delivery
			}
		}
		return registryErr(store.PutMembership(jam.Membership{ProjectID: p.ID, UserID: uid, Delivery: delivery}))
	}))
	mux.HandleFunc("DELETE /ui/projects/{project}/members/{user}", edit("member removed", sectionMembers, func(r *http.Request, project string) error {
		uid, err := jam.ResolveRegistryRef(store, ident.User, r.PathValue("user"))
		if err != nil {
			return registryErr(err)
		}
		p, _ := store.GetProject(project)
		return registryErr(store.RemoveMember(p.ID, uid))
	}))

	mux.HandleFunc("POST /ui/projects/{project}/channels", edit("room put", sectionIntercom, func(r *http.Request, project string) error {
		b := jam.RoomBody{
			Name:       strings.TrimSpace(r.FormValue("name")),
			Connection: strings.TrimSpace(r.FormValue("service")),
			Ref:        strings.TrimSpace(r.FormValue("ref")),
		}
		if b.Name == "" || b.Connection == "" || b.Ref == "" {
			return badRequest("room name, connection and ref are required")
		}
		_, _, err := jam.PutRoom(store, project, b)
		return registryErr(err)
	}))
	mux.HandleFunc("DELETE /ui/projects/{project}/channels/{name}", edit("room removed", sectionIntercom, func(r *http.Request, project string) error {
		return registryErr(jam.RemoveRoom(store, project, r.PathValue("name")))
	}))

	mux.HandleFunc("POST /ui/projects/{project}/escalation", edit("escalation set", sectionEscalation, func(r *http.Request, project string) error {
		var tiers []jam.EscalationTier
		for _, l := range splitSpecLines(r.FormValue("tiers")) {
			t, err := jam.ParseEscalationTierSpec(l)
			if err != nil {
				return badRequest(err.Error())
			}
			tiers = append(tiers, t)
		}
		if len(tiers) == 0 {
			return badRequest("a chain needs at least one tier (targets@timeout); use Clear to remove it")
		}
		return store.SetEscalationPolicy(project, strings.TrimSpace(r.FormValue("category")), tiers)
	}))
	mux.HandleFunc("DELETE /ui/projects/{project}/escalation", edit("escalation cleared", sectionEscalation, func(r *http.Request, project string) error {
		return store.SetEscalationPolicy(project, r.URL.Query().Get("category"), nil)
	}))

	mux.HandleFunc("POST /ui/projects/{project}/chat-service", edit("chat service set", sectionOverview, func(r *http.Request, project string) error {
		return store.SetChatService(project, strings.TrimSpace(r.FormValue("service")))
	}))
}
