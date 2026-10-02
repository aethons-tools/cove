package jam

import (
	"fmt"
	"slices"
	"strings"
)

// ParseDestinations parses the operator syntax "git=git-pat-cove,anthropic": a
// comma-separated list of destination names, each optionally mapped to the
// credential the broker injects for it. creds is nil when nothing is mapped.
func ParseDestinations(csv string) (dests []string, creds map[string]string, err error) {
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		name, cred, mapped := strings.Cut(p, "=")
		name, cred = strings.TrimSpace(name), strings.TrimSpace(cred)
		if name == "" || (mapped && cred == "") {
			return nil, nil, fmt.Errorf("bad destination entry %q (want name or name=credential)", p)
		}
		if slices.Contains(dests, name) {
			return nil, nil, fmt.Errorf("destination %q listed twice", name)
		}
		dests = append(dests, name)
		if mapped {
			if creds == nil {
				creds = map[string]string{}
			}
			creds[name] = cred
		}
	}
	return dests, creds, nil
}

// FormatDestinations renders dests/creds back into ParseDestinations syntax.
func FormatDestinations(dests []string, creds map[string]string) string {
	parts := make([]string, 0, len(dests))
	for _, d := range dests {
		if c := creds[d]; c != "" {
			d += "=" + c
		}
		parts = append(parts, d)
	}
	return strings.Join(parts, ",")
}

// ValidateCredentials checks a scope's destination→credential map at write
// time: every key must be an allowed destination and every non-empty value a
// configured credential, so a typo fails at the admin API, not mid-request. The
// error never echoes a credential name (as with destination cred-name checks).
func ValidateCredentials(s Scope, credExists func(string) bool) error {
	for d, c := range s.Credentials {
		if !slices.Contains(s.Destinations, d) {
			return fmt.Errorf("credentials maps %q, which is not one of the scope's destinations", d)
		}
		if c != "" && !credExists(c) {
			return fmt.Errorf("the credential mapped for %q does not resolve to a configured credential", d)
		}
	}
	return nil
}
