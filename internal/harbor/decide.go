package harbor

import (
	"fmt"
	"slices"
	"time"
)

// Decision is the outcome of the three-question pipeline for one request.
type Decision struct {
	Dest     Destination
	NeedCred bool
	CredName string
	Apply    ApplyMethod
}

// Decide answers, for an authenticated identity and a matched destination:
//  1. may this identity reach the destination (and, if repo-scoped, this repo)?
//  2. does the call require a credential?
//  3. which credential, and how to apply it?
//
// It fails closed: any policy violation returns an error and the zero Decision.
// now is injected so expiry is testable.
func Decide(id Identity, dest Destination, ownerRepo string, now time.Time) (Decision, error) {
	if !id.Expiry.IsZero() && now.After(id.Expiry) {
		return Decision{}, fmt.Errorf("identity %q expired", id.ID)
	}
	if !slices.Contains(id.Destinations, dest.Name) {
		return Decision{}, fmt.Errorf("identity %q not allowed destination %q", id.ID, dest.Name)
	}
	if dest.RepoScoped {
		if ownerRepo == "" {
			return Decision{}, fmt.Errorf("destination %q requires owner/repo", dest.Name)
		}
		if !repoAllowed(ownerRepo, id.Repos) {
			return Decision{}, fmt.Errorf("identity %q not allowed repo %q", id.ID, ownerRepo)
		}
	}
	return Decision{Dest: dest, NeedCred: dest.CredName != "", CredName: dest.CredName, Apply: dest.Apply}, nil
}
