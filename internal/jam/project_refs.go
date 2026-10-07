package jam

import "github.com/aethons-tools/cove/internal/ident"

// Project references (intercom 1b-2a). Stored references — Grant.Project,
// Instance.Project, a role's project — hold the project's id; the edges (CLI,
// admin API, UI, serve config) name projects, and every Store method that
// takes a project accepts either form ("" is DefaultProject). These helpers
// compare and label references.

// projectResolver is the slice of Store that resolves a reference.
type projectResolver interface {
	GetProject(ref string) (Project, bool)
}

// ProjectIDOf resolves a project reference to the project's id (ok=false: no
// such project).
func ProjectIDOf(store projectResolver, ref string) (ident.ID, bool) {
	p, ok := store.GetProject(ref)
	return p.ID, ok && p.ID != ""
}

// ProjectName is a project reference's name, for people (the reference
// itself when no project answers to it; "" is DefaultProject).
func ProjectName(store projectResolver, ref string) string {
	if p, ok := store.GetProject(ref); ok {
		return p.Name
	}
	return orDefaultProject(ref)
}

// SameProject reports whether two references name the same project.
func SameProject(store projectResolver, a, b string) bool {
	pa, oka := store.GetProject(a)
	pb, okb := store.GetProject(b)
	if oka && okb {
		return pa.ID == pb.ID && pa.Name == pb.Name
	}
	return orDefaultProject(a) == orDefaultProject(b)
}
