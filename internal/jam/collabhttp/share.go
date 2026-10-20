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
	"github.com/aethons-tools/cove/internal/jam/collabwire"
)

// Sharing: POST {prefix}/studios/{id}/shares {user, rights, for} grants a
// user time-boxed rights on a studio; DELETE …/shares/{user} removes the
// grant (closing that user's SSH connections when it was their only way in);
// GET …/access shows who can reach the studio and why, and who is connected.
// An operator may do all of it. A participant must hold some right on the
// studio to use any of it, may grant only rights they hold (and, when they
// hold one only by a grant, for no longer than that grant runs), and may
// remove a grant whose rights they all hold, or their own.

// AccessOf builds inst's collabwire.AccessView.
func AccessOf(store jam.Store, live *Live, now time.Time, inst jam.Instance) collabwire.AccessView {
	v := collabwire.AccessView{Session: inst.ActorID, Grants: []collabwire.GrantView{}, Live: live.List(inst.ActorID)}
	if v.Live == nil {
		v.Live = []collabwire.LiveConn{}
	}
	if inst.SessionKind == jam.SessionKindPersonal && inst.OwnerID != "" {
		v.Requester = &collabwire.UserRef{ID: inst.OwnerID, Name: userName(store, inst.OwnerID, inst.Owner)}
	}
	role, _ := store.GetRole(inst.Project, inst.Role)
	v.Role = collabwire.RoleAccess{Project: jam.ProjectName(store, inst.Project), Role: inst.Role, Preview: "none", SSH: "none"}
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

func grantView(store jam.Store, now time.Time, g jam.ShareGrant) collabwire.GrantView {
	by := g.GrantedBy
	if id, err := ident.Parse(g.GrantedBy); err == nil && id.Kind() == ident.User {
		by = userName(store, id, g.GrantedBy)
	}
	return collabwire.GrantView{
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

// GrantOf returns user's grant on session, if any (expired ones included).
func GrantOf(store jam.Store, session string, user ident.ID) (jam.ShareGrant, bool) {
	for _, g := range store.ShareGrants(session) {
		if g.UserID == user {
			return g, true
		}
	}
	return jam.ShareGrant{}, false
}

// Unshare removes grant g as grantor (op:<id> or usr_…, for the audit line)
// — only if it is still exactly g, so a caller's checks on g hold for what is
// removed — and closes the user's open SSH connections on the studio unless
// they still hold ssh another way. It reports whether it removed g; false
// means the grant is gone or changed since it was read.
func Unshare(store jam.Store, live *Live, log *slog.Logger, now time.Time, grantor string, g jam.ShareGrant) (bool, error) {
	removed, err := store.RemoveShareGrantIf(g)
	if err != nil || !removed {
		return false, err
	}
	closed := closeIfSSHLost(store, live, now, g.SessionID, g.UserID)
	log.Info("studio unshared", "grantor", grantor, "user", string(g.UserID), "session", g.SessionID, "rights", strings.Join(g.Rights, ","), "expires", g.ExpiresAt.UTC().Format(time.RFC3339), "closed", closed)
	return true, nil
}

// closeIfSSHLost closes user's open SSH connections on session unless they
// still hold ssh on it, returning how many it closed.
func closeIfSSHLost(store jam.Store, live *Live, now time.Time, session string, user ident.ID) int {
	inst, ok := store.GetInstance(session)
	if ok {
		if _, still := collab.Decide(store, now, collab.Principal{UserID: user}, inst, collab.SSH); still {
			return 0
		}
	}
	return live.CloseUser(session, user)
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

// grantOfRef finds the session's grant for a user reference: a live user
// (as resolveGrantee), else a grant whose user has that id or name — so a
// removed user's leftover grant can still be named.
func grantOfRef(store jam.Store, session, ref string) (jam.ShareGrant, bool) {
	if id, ok := resolveGrantee(store, ref); ok {
		if g, ok := GrantOf(store, session, id); ok {
			return g, true
		}
	}
	ref = strings.TrimPrefix(ref, "user:")
	for _, g := range store.ShareGrants(session) {
		if string(g.UserID) == ref || userName(store, g.UserID, "") == ref {
			return g, true
		}
	}
	return jam.ShareGrant{}, false
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
	old, replacing := GrantOf(h.d.Store, inst.ActorID, user)
	if p.Operator == "" {
		if user == p.UserID {
			http.Error(w, "you can't share with yourself", http.StatusForbidden)
			return
		}
		for _, right := range rights {
			if msg := h.mayShare(p, inst, collab.Right(right), g.ExpiresAt, now); msg != "" {
				http.Error(w, msg, http.StatusForbidden)
				return
			}
		}
		// Replacing a grant removes it, so it takes what removing it takes.
		if replacing {
			if msg := h.mayRemove(p, inst, old, now); msg != "" {
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
	closed := 0
	if replacing && slices.Contains(old.Rights, string(collab.SSH)) && !slices.Contains(rights, string(collab.SSH)) {
		closed = closeIfSSHLost(h.d.Store, h.d.Live, now, inst.ActorID, user)
	}
	h.d.Log.Info("studio shared", "grantor", g.GrantedBy, "user", string(user), "session", inst.ActorID, "rights", strings.Join(rights, ","), "expires", g.ExpiresAt.UTC().Format(time.RFC3339), "replaced", replacing, "closed", closed)
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

// mayRemove says why participant p may not remove grant g, or "" when it
// may: g is p's own, or p holds every right in it.
func (h *handler) mayRemove(p collab.Principal, inst jam.Instance, g jam.ShareGrant, now time.Time) string {
	if g.UserID == p.UserID {
		return ""
	}
	for _, right := range g.Rights {
		if _, holds := collab.Decide(h.d.Store, now, p, inst, collab.Right(right)); !holds {
			return "you may not change or remove a grant of " + right + ": you don't hold it on this studio"
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
	g, found := grantOfRef(h.d.Store, inst.ActorID, ref)
	if !found {
		http.Error(w, fmt.Sprintf("no share for %q on this studio", ref), http.StatusNotFound)
		return
	}
	now := h.d.Now()
	if p.Operator == "" {
		if msg := h.mayRemove(p, inst, g, now); msg != "" {
			http.Error(w, msg, http.StatusForbidden)
			return
		}
	}
	removed, err := Unshare(h.d.Store, h.d.Live, h.d.Log, now, grantor(p), g)
	switch {
	case err != nil:
		h.d.Log.Error("collab: removing share grant failed", "session", inst.ActorID, "err", err.Error())
		http.Error(w, "could not remove the grant", http.StatusInternalServerError)
	case !removed:
		http.Error(w, "the grant changed while being removed; retry", http.StatusConflict)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *handler) access(w http.ResponseWriter, r *http.Request) {
	_, inst, ok := h.sharer(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(AccessOf(h.d.Store, h.d.Live, h.d.Now(), inst))
}
