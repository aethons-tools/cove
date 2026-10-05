package jam

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

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
	switch c := http.CanonicalHeaderKey(r.Name); {
	case c == "Authorization" || c == "X-Api-Key" || c == "Cookie" || strings.HasPrefix(c, "Proxy-"):
		return fmt.Errorf("header %s carries credentials or belongs to the proxy; a header rule cannot set it", c)
	}
	if (r.Set == "") == (r.EnsureListItem == "") {
		return errors.New("needs exactly one of set or ensure-list-item")
	}
	if !validHeaderValuePart(r.Set + r.EnsureListItem) {
		return errors.New("value must be single-line")
	}
	if item := r.EnsureListItem; item != "" {
		if strings.Contains(item, ",") {
			return errors.New("ensure-list-item must be one list item (no comma)")
		}
		if strings.TrimSpace(item) != item {
			return errors.New("ensure-list-item has leading or trailing whitespace")
		}
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

// anthropicDestinationMatchers identify the destination serving the claude
// anthropic provider, in precedence order: the one named "anthropic", else
// the one routed at /anthropic/. Shared by the claude-default seed
// (DefaultModelSpecFor) and the broker's principal header rules.
var anthropicDestinationMatchers = []func(Destination) bool{
	func(d Destination) bool { return d.Name == "anthropic" },
	func(d Destination) bool { return d.Route == "/anthropic/" },
}

// servesProvider reports whether dest is the destination serving spec's
// provider route. Only claude on the anthropic provider has one; vertex and
// bedrock are not brokered by a known destination. The cheap name check comes
// first; the store is listed only for a route-matched, differently named
// destination.
func servesProvider(store Store, spec ModelSpec, dest Destination) bool {
	if spec.Type != HarnessClaude || spec.Claude == nil || spec.Claude.Provider != "anthropic" {
		return false
	}
	switch {
	case anthropicDestinationMatchers[0](dest):
		return true
	case !anthropicDestinationMatchers[1](dest):
		return false
	}
	return !slices.ContainsFunc(store.ListDestinations(), anthropicDestinationMatchers[0])
}

// mayServeProvider is servesProvider's store-free pre-check: false means dest
// serves no provider route, so the broker need not resolve a model-spec.
func mayServeProvider(dest Destination) bool {
	return slices.ContainsFunc(anthropicDestinationMatchers, func(m func(Destination) bool) bool { return m(dest) })
}

// principalHeaderRules returns the header rules of actor's model-spec when
// dest serves that spec's provider route (nil otherwise, and for a spec
// without rules). The spec is resolved as the connector resolves it
// (ModelSpecFor); an unresolvable one applies no rules.
func (b *Broker) principalHeaderRules(actor Actor, dest Destination) []ModelHeaderRule {
	if !mayServeProvider(dest) {
		return nil
	}
	spec, err := ModelSpecFor(b.store, actor)
	if err != nil {
		b.log.Warn("principal header rules not applied", "actor", actor.ID, "destination", dest.Name, "reason", err.Error())
		return nil
	}
	if spec == nil || len(spec.Principal.Headers) == 0 || !servesProvider(b.store, *spec, dest) {
		return nil
	}
	return spec.Principal.Headers
}

// applyPrincipalHeaders applies rules to h after the credential. A rule that
// fails the static checks, or names a header the destination's identity or
// apply spec uses, is skipped with a warning naming the header only — rule
// values are not secrets but are never logged.
func (b *Broker) applyPrincipalHeaders(h http.Header, rules []ModelHeaderRule, actor Actor, dest Destination) {
	var owned []string
	if in, ok := dest.InboundSpec(); ok {
		owned = append(owned, http.CanonicalHeaderKey(in.Header))
	}
	if out, ok := dest.OutboundSpec(); ok {
		owned = append(owned, http.CanonicalHeaderKey(out.Header))
	}
	for _, r := range rules {
		name := http.CanonicalHeaderKey(r.Name)
		if err := checkPrincipalHeaderRule(r); err != nil || slices.Contains(owned, name) {
			b.log.Warn("principal header rule skipped", "actor", actor.ID, "destination", dest.Name, "header", name)
			continue
		}
		if r.EnsureListItem != "" {
			ensureListItem(h, name, r.EnsureListItem)
		} else {
			h.Set(name, r.Set)
		}
	}
}

// ensureListItem adds item to h's comma-separated list header name,
// preserving the items already present and never duplicating (items compare
// with surrounding whitespace trimmed).
func ensureListItem(h http.Header, name, item string) {
	existing := h.Get(name)
	if existing == "" {
		h.Set(name, item)
		return
	}
	for _, it := range strings.Split(existing, ",") {
		if strings.TrimSpace(it) == item {
			return
		}
	}
	h.Set(name, existing+","+item)
}
