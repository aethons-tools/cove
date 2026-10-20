package collabhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
)

// Sharing: POST {prefix}/studios/{id}/shares {user, rights, for} grants a
// user time-boxed rights on a studio; DELETE …/shares/{user} removes the
// grant (closing that user's SSH connections when it was their only way in);
// GET …/access shows who can reach the studio and why, and who is connected.
// An operator may do all of it. A participant must hold some right on the
// studio to use any of it, may grant only rights they hold (and, when they
// hold one only by a grant, for no longer than that grant runs), and may
// remove a grant whose rights they all hold, or their own.

// GrantView is a share grant as the API and UI show it.
type GrantView struct {
	User          string    `json:"user"` // the grantee's name
	UserID        ident.ID  `json:"user_id"`
	Rights        []string  `json:"rights"`
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedAt     time.Time `json:"created_at"`
	GrantedBy     string    `json:"granted_by"`      // op:<operator-id> or usr_…
	GrantedByName string    `json:"granted_by_name"` // the grantor's user name when a user, else GrantedBy
	Expired       bool      `json:"expired"`
}

// UserRef names a roster user.
type UserRef struct {
	ID   ident.ID `json:"id"`
	Name string   `json:"name"`
}

// RoleAccess is what the studio's role gives the project's members, per
// right: "members" or "none" (defaults spelled out).
type RoleAccess struct {
	Project string `json:"project"`
	Role    string `json:"role"`
	Preview string `json:"preview"`
	SSH     string `json:"ssh"`
}

// AccessView is who can reach a studio and why: operators always; the
// personal session's requester; project members as the role says; each share
// grant. Live lists the open SSH connections.
type AccessView struct {
	Session   string      `json:"session"`
	Requester *UserRef    `json:"requester,omitempty"`
	Role      RoleAccess  `json:"role"`
	Grants    []GrantView `json:"grants"`
	Live      []LiveConn  `json:"live"`
}

// AccessOf builds inst's AccessView.
func AccessOf(store jam.Store, live *Live, now time.Time, inst jam.Instance) AccessView {
	v := AccessView{Session: inst.ActorID, Grants: []GrantView{}, Live: live.List(inst.ActorID)}
	if v.Live == nil {
		v.Live = []LiveConn{}
	}
	if inst.SessionKind == jam.SessionKindPersonal && inst.OwnerID != "" {
		v.Requester = &UserRef{ID: inst.OwnerID, Name: userName(store, inst.OwnerID, inst.Owner)}
	}
	role, _ := store.GetRole(inst.Project, inst.Role)
	v.Role = RoleAccess{Project: jam.ProjectName(store, inst.Project), Role: inst.Role, Preview: "none", SSH: "none"}
	if role.Collaborate.Allows(string(collab.Preview)) {
		v.Role.Preview = "members"
	}
	if role.Collaborate.Allows(string(collab.SSH)) {
		v.Role.SSH = "members"
	}
	for _, g := range store.ShareGrants(inst.ActorID) {
		v.Grants = append(v.Grants, grantView(store, now, g))
	}
	return v
}

func grantView(store jam.Store, now time.Time, g jam.ShareGrant) GrantView {
	by := g.GrantedBy
	if id, err := ident.Parse(g.GrantedBy); err == nil && id.Kind() == ident.User {
		by = userName(store, id, g.GrantedBy)
	}
	return GrantView{
		User: userName(store, g.UserID, string(g.UserID)), UserID: g.UserID, Rights: g.Rights,
		ExpiresAt: g.ExpiresAt, CreatedAt: g.CreatedAt, GrantedBy: g.GrantedBy, GrantedByName: by,
		Expired: !now.Before(g.ExpiresAt),
	}
}

func userName(store jam.Store, id ident.ID, fallback string) string {
	if u, ok := store.GetUser(id); ok && u.Name != "" {
		return u.Name
	}
	return fallback
}

// Unshare removes user's grant on session as grantor (op:<id> or usr_…, for
// the audit line) and, unless the user still holds ssh by another rule,
// closes their open SSH connections on it. It reports whether there was a
// grant.
func Unshare(store jam.Store, live *Live, log *slog.Logger, now time.Time, grantor, session string, user ident.ID) (bool, error) {
	var g jam.ShareGrant
	found := false
	for _, x := range store.ShareGrants(session) {
		if x.UserID == user {
			g, found = x, true
		}
	}
	if !found {
		return false, nil
	}
	if err := store.RemoveShareGrant(session, user); err != nil {
		return true, err
	}
	closed := 0
	inst, ok := store.GetInstance(session)
	if _, still := collab.Decide(store, now, collab.Principal{UserID: user}, inst, collab.SSH); !ok || !still {
		closed = live.CloseUser(session, user)
	}
	log.Info("studio unshared", "grantor", grantor, "user", string(user), "session", session, "rights", strings.Join(g.Rights, ","), "expires", g.ExpiresAt.UTC().Format(time.RFC3339), "closed", closed)
	return true, nil
}

var daysRe = regexp.MustCompile(`^([0-9]{1,4})d$`)

// ParseGrantFor parses a grant lifetime: a Go duration (4h, 90m) or whole
// days (7d); "" is collab.DefaultGrant. Range is collab.ValidateGrant's.
func ParseGrantFor(s string) (time.Duration, error) {
	if s == "" {
		return collab.DefaultGrant, nil
	}
	if m := daysRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (e.g. 4h, 90m, 7d)", s)
	}
	return d, nil
}

// grantor is how p appears as a grant's granted_by.
func grantor(p collab.Principal) string {
	return access{p: p}.auditUser()
}

// resolveGrantee resolves a user reference: a name, user:<name>, or a usr_ id
// of a live user.
func resolveGrantee(store jam.Store, ref string) (ident.ID, bool) {
	ref = strings.TrimPrefix(ref, "user:")
	if ref == "" {
		return "", false
	}
	id, err := jam.ResolveRegistryRef(store, ident.User, ref)
	if err != nil {
		return "", false
	}
	if u, ok := store.GetUser(id); !ok || u.Status != jam.StatusLive {
		return "", false
	}
	return id, true
}

// sharer resolves the caller and studio for a sharing route: an operator, or
// a participant holding at least one right on it.
func (h *handler) sharer(w http.ResponseWriter, r *http.Request) (collab.Principal, jam.Instance, bool) {
	p, _, inst, ok := h.resolve(w, r)
	if !ok {
		return p, inst, false
	}
	if p.Operator == "" {
		_, pv := collab.Decide(h.d.Store, h.d.Now(), p, inst, collab.Preview)
		_, sh := collab.Decide(h.d.Store, h.d.Now(), p, inst, collab.SSH)
		if !pv && !sh {
			http.Error(w, "you have no access to this studio", http.StatusForbidden)
			return p, inst, false
		}
	}
	return p, inst, true
}

func (h *handler) share(w http.ResponseWriter, r *http.Request) {
	p, inst, ok := h.sharer(w, r)
	if !ok {
		return
	}
	var in struct {
		User   string   `json:"user"`
		Rights []string `json:"rights"`
		For    string   `json:"for"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&in); err != nil {
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	dur, err := ParseGrantFor(in.For)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user, ok := resolveGrantee(h.d.Store, in.User)
	if !ok {
		http.Error(w, fmt.Sprintf("no such user %q", in.User), http.StatusNotFound)
		return
	}
	now := h.d.Now()
	rights := slices.Compact(slices.Sorted(slices.Values(in.Rights)))
	g := jam.ShareGrant{SessionID: inst.ActorID, UserID: user, Rights: rights, CreatedAt: now, ExpiresAt: now.Add(dur), GrantedBy: grantor(p)}
	if err := collab.ValidateGrant(now, g); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if p.Operator == "" {
		for _, right := range rights {
			if msg := h.mayShare(p, inst, collab.Right(right), g.ExpiresAt, now); msg != "" {
				http.Error(w, msg, http.StatusForbidden)
				return
			}
		}
	}
	if err := h.d.Store.PutShareGrant(g); err != nil {
		h.d.Log.Error("collab: storing share grant failed", "session", inst.ActorID, "err", err.Error())
		http.Error(w, "could not store the grant", http.StatusInternalServerError)
		return
	}
	h.d.Log.Info("studio shared", "grantor", g.GrantedBy, "user", string(user), "session", inst.ActorID, "rights", strings.Join(rights, ","), "expires", g.ExpiresAt.UTC().Format(time.RFC3339))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(grantView(h.d.Store, now, g))
}

// mayShare says why participant p may not grant right until expires, or ""
// when it may: p must hold the right, and a right p holds only by a grant
// may not be passed on (or renewed) past that grant's end.
func (h *handler) mayShare(p collab.Principal, inst jam.Instance, right collab.Right, expires, now time.Time) string {
	reason, ok := collab.Decide(h.d.Store, now, p, inst, right)
	if !ok {
		return "you may not share " + string(right) + ": you don't hold it on this studio"
	}
	if reason != "grant" {
		return ""
	}
	for _, own := range h.d.Store.ShareGrants(inst.ActorID) {
		if own.UserID == p.UserID && expires.After(own.ExpiresAt) {
			return "you may not share " + string(right) + " beyond your own access, which ends " + own.ExpiresAt.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func (h *handler) unshare(w http.ResponseWriter, r *http.Request) {
	p, inst, ok := h.sharer(w, r)
	if !ok {
		return
	}
	ref := r.PathValue("user")
	user, ok := resolveGrantee(h.d.Store, ref)
	var g jam.ShareGrant
	found := false
	if ok {
		for _, x := range h.d.Store.ShareGrants(inst.ActorID) {
			if x.UserID == user {
				g, found = x, true
			}
		}
	}
	if !found {
		http.Error(w, fmt.Sprintf("no share for %q on this studio", ref), http.StatusNotFound)
		return
	}
	now := h.d.Now()
	if p.Operator == "" && p.UserID != user {
		for _, right := range g.Rights {
			if _, holds := collab.Decide(h.d.Store, now, p, inst, collab.Right(right)); !holds {
				http.Error(w, "you may not remove a grant of "+right+": you don't hold it on this studio", http.StatusForbidden)
				return
			}
		}
	}
	if _, err := Unshare(h.d.Store, h.d.Live, h.d.Log, now, grantor(p), inst.ActorID, user); err != nil {
		h.d.Log.Error("collab: removing share grant failed", "session", inst.ActorID, "err", err.Error())
		http.Error(w, "could not remove the grant", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) access(w http.ResponseWriter, r *http.Request) {
	_, inst, ok := h.sharer(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(AccessOf(h.d.Store, h.d.Live, h.d.Now(), inst))
}
