package jam

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// ParseDeliverySpec parses `service:address[:user-id]` (the CLI's --delivery
// and the UI's delivery lines) and validates it.
func ParseDeliverySpec(s string) (DeliveryProfile, error) {
	svc, rest, ok := strings.Cut(s, ":")
	if !ok || svc == "" || rest == "" {
		return DeliveryProfile{}, fmt.Errorf("want service:address[:user-id]")
	}
	addr, uid, hasUID := strings.Cut(rest, ":")
	if addr == "" {
		return DeliveryProfile{}, fmt.Errorf("want service:address[:user-id]")
	}
	if hasUID && uid == "" {
		return DeliveryProfile{}, fmt.Errorf("empty user id")
	}
	p := DeliveryProfile{Service: svc, Address: addr, UserID: uid}
	if err := ValidateDelivery([]DeliveryProfile{p}); err != nil {
		return DeliveryProfile{}, err
	}
	return p, nil
}

// FormatDeliverySpec renders p back into ParseDeliverySpec syntax.
func FormatDeliverySpec(p DeliveryProfile) string {
	s := p.Service + ":" + p.Address
	if p.UserID != "" {
		s += ":" + p.UserID
	}
	return s
}

// ParseOIDCSpec parses `issuer:subject`. The issuer is commonly a URL that
// itself contains colons, so the split is on the final colon. Both must be
// non-empty.
func ParseOIDCSpec(s string) (OIDCIdentity, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return OIDCIdentity{}, fmt.Errorf("want issuer:subject")
	}
	id := OIDCIdentity{Issuer: s[:i], Subject: s[i+1:]}
	if err := ValidateIdentity([]OIDCIdentity{id}); err != nil {
		return OIDCIdentity{}, err
	}
	return id, nil
}

// FormatOIDCSpec renders id back into ParseOIDCSpec syntax.
func FormatOIDCSpec(id OIDCIdentity) string { return id.Issuer + ":" + id.Subject }

// ParseEscalationTierSpec parses one tier, `target,target@timeout` (the CLI's
// --tier and one line of the UI's chain editor): at least one target and a
// positive Go duration.
func ParseEscalationTierSpec(s string) (EscalationTier, error) {
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return EscalationTier{}, fmt.Errorf("tier %q missing '@timeout'", s)
	}
	d, err := time.ParseDuration(strings.TrimSpace(s[at+1:]))
	if err != nil {
		return EscalationTier{}, fmt.Errorf("tier %q: bad timeout: %w", s, err)
	}
	if d <= 0 {
		return EscalationTier{}, fmt.Errorf("tier %q: timeout must be positive", s)
	}
	var targets []string
	for t := range strings.SplitSeq(s[:at], ",") {
		if t = strings.TrimSpace(t); t != "" {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return EscalationTier{}, fmt.Errorf("tier %q has no targets", s)
	}
	return EscalationTier{Targets: targets, Timeout: d}, nil
}

// FormatEscalationTierSpec renders t back into ParseEscalationTierSpec syntax.
func FormatEscalationTierSpec(t EscalationTier) string {
	return strings.Join(t.Targets, ",") + "@" + FormatDuration(t.Timeout)
}

// FormatDuration renders d without trailing zero units: "1h", "1h30m", "45s".
func FormatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// rosterMu makes PutRosterHuman's uniqueness checks and its write one step, so
// two concurrent adds can't both claim one login or Discord user id.
var rosterMu sync.Mutex

// PutRosterHuman adds h to project's roster, or replaces the human of the same
// name — the one roster-human write for the JSON admin API and the UI. A login
// links at most one human per project (so ownership, e.g. of a personal
// session, is unambiguous) and a Discord user id binds at most one (so a
// Discord reply's author is unambiguous); delivery and identity must validate.
// Refusals are 400 WriteErrors; an unknown project is ErrProjectNotFound.
func PutRosterHuman(store Store, project string, h Human) error {
	if h.Name == "" {
		return writeErr(http.StatusBadRequest, "human name required")
	}
	if err := ValidateDelivery(h.Delivery); err != nil {
		return writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := ValidateIdentity(h.Identity); err != nil {
		return writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	rosterMu.Lock()
	defer rosterMu.Unlock()
	if other, ok := HumanByLogin(store, project, h.Login); ok && other.Name != h.Name {
		return writeErr(http.StatusBadRequest, "login is already linked to roster human %q in this project", other.Name)
	}
	if r, ok := store.GetRoster(project); ok {
		for _, id := range h.discordUserIDs() {
			for _, other := range r.Humans {
				if other.Name != h.Name && slices.Contains(other.discordUserIDs(), id) {
					return writeErr(http.StatusBadRequest, "discord user id is already bound to roster human %q in this project", other.Name)
				}
			}
		}
	}
	return store.AddHuman(project, h)
}
