package harbor

import (
	"fmt"
	"net"
	"net/http"
)

// Operator is the identity behind an admin-API request. Operator identity (humans
// managing harbor) is a separate plane from actor identity (enrollment tokens).
type Operator struct{ ID string }

// OperatorAuthenticator resolves the operator behind an admin request, or rejects it.
// The MVP impl trusts loopback; the Auth0/OIDC impl (next slice) drops in here.
type OperatorAuthenticator interface {
	Authenticate(r *http.Request) (Operator, error)
}

// LoopbackAuthenticator accepts only requests from the loopback interface and
// attributes them to the single local operator. Defence in depth for the
// loopback-bound admin listener.
type LoopbackAuthenticator struct{}

func (LoopbackAuthenticator) Authenticate(r *http.Request) (Operator, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return Operator{}, fmt.Errorf("admin request from non-loopback address %q", r.RemoteAddr)
	}
	return Operator{ID: "local"}, nil
}
