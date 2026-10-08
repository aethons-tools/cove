package jam

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// MaxPrincipalHeaderRules bounds a model-spec's principal.headers.
const MaxPrincipalHeaderRules = 16

// ModelHeaderRule is a model-spec principal header rule (modelspec.HeaderRule).
type ModelHeaderRule = modelspec.HeaderRule

// checkPrincipalHeaderRule holds the static rules for one principal header
// rule, checked at write time and again by the broker before applying it: a
// header name the proxy can carry, never a credential/session/proxy header,
// exactly one action, and a single-line value. Errors never echo the value.
func checkPrincipalHeaderRule(r ModelHeaderRule) error {
	if r.Name == "" {
		return errors.New("name is required")
	}
	if err := validateHeaderName(r.Name); err != nil {
		return err
	}
	if strings.HasSuffix(r.Name, "+") {
		// Keeps the admin UI's `NAME += ITEM` syntax unambiguous.
		return errors.New("header name may not end in '+'")
	}
	switch c := http.CanonicalHeaderKey(r.Name); {
	case c == "Authorization" || c == "X-Api-Key" || c == "Cookie" || strings.HasPrefix(c, "Proxy-"):
		return fmt.Errorf("header %s carries credentials or belongs to the proxy; a header rule cannot set it", c)
	}
	if (r.Set == "") == (r.EnsureListItem == "") {
		return errors.New("needs exactly one of set or ensure-list-item")
	}
	v := r.Set + r.EnsureListItem // exactly one is set
	if !validHeaderValuePart(v) {
		return errors.New("value must be single-line")
	}
	if strings.TrimSpace(v) != v {
		return errors.New("value has leading or trailing whitespace")
	}
	if strings.Contains(r.EnsureListItem, ",") {
		return errors.New("ensure-list-item must be one list item (no comma)")
	}
	return nil
}

// validatePrincipalHeaders checks principal.headers at write time. The
// destination-specific check (a rule may not touch a header the destination's
// identity or apply spec uses) is the broker's, at apply time.
func validatePrincipalHeaders(rules []ModelHeaderRule, bad func(string, ...any) error) error {
	if len(rules) > MaxPrincipalHeaderRules {
		return bad("principal.headers has %d rules; at most %d", len(rules), MaxPrincipalHeaderRules)
	}
	for i, r := range rules {
		if err := checkPrincipalHeaderRule(r); err != nil {
			return bad("principal.headers[%d]: %s", i, err.Error())
		}
	}
	return nil
}

// servedProvider names the claude provider whose API d serves, by its client
// env (what the connector hands the cove): "anthropic" when it sets
// ANTHROPIC_BASE_URL (the legacy /anthropic/ route, or any destination whose
// env sets it), "vertex" when it sets ANTHROPIC_VERTEX_BASE_URL; "" for any
// other destination. The broker checks the destination that matched the
// request, so a more specific sub-route (or any other route) is never mistaken
// for it.
func servedProvider(d Destination) string {
	env := d.ClientEnv()
	if _, ok := env["ANTHROPIC_BASE_URL"]; ok {
		return "anthropic"
	}
	if _, ok := env["ANTHROPIC_VERTEX_BASE_URL"]; ok {
		return "vertex"
	}
	return ""
}

// principalHeaderRules returns the header rules to apply to a request the
// actor made on dest, which presents identities per in: the rules of the
// actor's model-spec (resolved as the connector resolves it) when it is claude
// on the provider dest serves (servedProvider: anthropic or vertex). Rules failing
// the static checks or naming a header the destination's identity or apply
// spec uses are dropped. Warnings name the header only — rule values are not
// secrets but are never logged — and are deduplicated (b.warns).
func (b *Broker) principalHeaderRules(actor Actor, dest Destination, in InboundSpec) []ModelHeaderRule {
	served := servedProvider(dest)
	if served == "" {
		return nil
	}
	resolveKey := "resolve\x00" + actor.ID
	name, explicit, by, err := modelSpecNameFor(b.store, actor)
	var provider string
	var rules []ModelHeaderRule
	if err == nil && name != "" {
		var ok bool
		if provider, rules, ok = b.store.PrincipalHeaderRules(name); !ok && explicit {
			err = missingModelSpecErr(name, by)
		}
	}
	if err != nil {
		if b.warns.first(resolveKey, err.Error()) {
			b.log.Warn("principal header rules not applied", "actor", actor.ID, "destination", dest.Name, "reason", err.Error())
		}
		return nil
	}
	b.warns.forget(resolveKey)
	if provider != served || len(rules) == 0 {
		return nil
	}
	owned := []string{http.CanonicalHeaderKey(in.Header)}
	if out, ok := dest.OutboundSpec(); ok {
		owned = append(owned, http.CanonicalHeaderKey(out.Header))
	}
	apply := rules[:0] // rules is the store's fresh copy
	for _, r := range rules {
		h := http.CanonicalHeaderKey(r.Name)
		reason := ""
		switch {
		case checkPrincipalHeaderRule(r) != nil:
			reason = "rule fails validation"
		case slices.Contains(owned, h):
			reason = "header is used by the destination's identity or apply spec"
		default:
			apply = append(apply, r)
			continue
		}
		if b.warns.first("skip\x00"+actor.ID+"\x00"+dest.Name+"\x00"+h, reason) {
			b.log.Warn("principal header rule skipped", "actor", actor.ID, "destination", dest.Name, "header", h, "reason", reason)
		}
	}
	return apply
}

// applyHeaderRules applies already-filtered principal header rules to h.
func applyHeaderRules(h http.Header, rules []ModelHeaderRule) {
	for _, r := range rules {
		if r.EnsureListItem != "" {
			ensureListItem(h, r.Name, r.EnsureListItem)
		} else {
			h.Set(r.Name, r.Set)
		}
	}
}

// ensureListItem adds item to h's comma-separated list header name,
// preserving the items already present and never duplicating (items compare
// with surrounding whitespace trimmed). Repeated header lines are one list:
// membership is checked across all of them, and they are rewritten as one
// comma-joined line keeping every item in order.
func ensureListItem(h http.Header, name, item string) {
	var vals []string
	for _, v := range h.Values(name) {
		if strings.TrimSpace(v) != "" {
			vals = append(vals, v)
		}
	}
	joined := strings.Join(vals, ",")
	present := false
	for _, it := range strings.Split(joined, ",") {
		if strings.TrimSpace(it) == item {
			present = true
			break
		}
	}
	switch {
	case joined == "":
		h.Set(name, item)
	case !present:
		h.Set(name, joined+","+item)
	case len(h.Values(name)) > 1:
		h.Set(name, joined)
	}
}

// warnDedupeTTL is how long a deduplicated warning stays quiet; after it the
// same (key, reason) warns again. warnDedupeMax bounds the memory: past it the
// table is reset (at worst a burst of repeat warnings).
const (
	warnDedupeTTL = time.Hour
	warnDedupeMax = 4096
)

// warnDedupe rate-limits a per-request warning: first reports true once per
// (key, reason) until the reason changes, the key is forgotten, or the TTL
// passes. Safe for concurrent use.
type warnDedupe struct {
	mu   sync.Mutex
	now  func() time.Time
	seen map[string]warnSeen
}

type warnSeen struct {
	reason string
	at     time.Time
}

func newWarnDedupe(now func() time.Time) *warnDedupe {
	return &warnDedupe{now: now, seen: map[string]warnSeen{}}
}

func (w *warnDedupe) first(key, reason string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if s, ok := w.seen[key]; ok && s.reason == reason && now.Sub(s.at) < warnDedupeTTL {
		return false
	}
	if len(w.seen) >= warnDedupeMax {
		clear(w.seen)
	}
	w.seen[key] = warnSeen{reason: reason, at: now}
	return true
}

func (w *warnDedupe) forget(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.seen, key)
}
