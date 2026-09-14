package harbor

import (
	"context"
	"fmt"
	"net"
	"net/http"
)

// Operator is the identity behind an admin-API request. Operator identity (humans
// managing harbor) is a separate plane from actor identity (enrollment tokens).
type Operator struct{ ID string }

type operatorCtxKey struct{}

// WithOperator returns r carrying op, so handlers can attribute a change to the
// authenticated operator in the audit log.
func WithOperator(r *http.Request, op Operator) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), operatorCtxKey{}, op))
}

// OperatorID returns the authenticated operator's id from the request context,
// or "" if the request was not authenticated.
func OperatorID(r *http.Request) string {
	op, _ := r.Context().Value(operatorCtxKey{}).(Operator)
	return op.ID
}

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
	if !IsLoopbackRequest(r) {
		return Operator{}, fmt.Errorf("admin request from non-loopback address %q", r.RemoteAddr)
	}
	return Operator{ID: "local"}, nil
}

// IsLoopbackRequest reports whether r originates from a loopback IP.
func IsLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
