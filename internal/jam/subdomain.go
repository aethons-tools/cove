package jam

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync/atomic"
)

// destinationsDomain is this Jam's destinations domain (serve config
// destinations.domain; "" = none), which the client connector's endpoint
// placeholders and git routing resolve on. It is process configuration, set
// once at serve start (SetDestinationsDomain) like the broker's, so every
// path assembling a connector — delivery, status, setup views — agrees on
// one without threading it through each.
var destinationsDomain atomic.Pointer[string]

// SetDestinationsDomain sets the destinations domain connectors resolve on.
func SetDestinationsDomain(domain string) { destinationsDomain.Store(&domain) }

// DestinationsDomain is the domain SetDestinationsDomain set ("" = none).
func DestinationsDomain() string {
	if d := destinationsDomain.Load(); d != nil {
		return *d
	}
	return ""
}

// subdomainRE is one lowercase DNS label (LDH, no leading/trailing '-').
var subdomainRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidSubdomain checks an endpoint's subdomain: one lowercase DNS label with
// no "--" (the "xn--" punycode prefix, as preview.ValidName refuses it).
func ValidSubdomain(sub string) error {
	if !subdomainRE.MatchString(sub) || strings.Contains(sub, "--") {
		return fmt.Errorf("subdomain %q: want one lowercase DNS label of [a-z0-9-] (at most 63, not starting or ending with '-', no '--')", sub)
	}
	return nil
}

// SubdomainOf places host (a :port ignored, case and a trailing dot folded)
// against the destinations domain: under is whether host is a name below
// domain (domain itself is not), and sub is its subdomain when host is exactly
// one valid label below domain ("" otherwise, and always when domain is "").
func SubdomainOf(host, domain string) (sub string, under bool) {
	if domain == "" {
		return "", false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	label, ok := strings.CutSuffix(host, "."+strings.ToLower(domain))
	if !ok || label == "" {
		return "", false
	}
	if ValidSubdomain(label) != nil {
		return "", true
	}
	return label, true
}
