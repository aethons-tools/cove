// Package allocator is harbor's capacity authority (the "Allocator" role from the
// orchestration design): it rations session existence per (project, role) against
// a budget. Slice 1 is an in-memory admission check that moves the concurrency cap
// out of the dispatcher; the event-sourced reservation ledger arrives in a later
// slice, behind this same interface.
package allocator

// Key identifies a role within a project — the allocation aggregate's key.
type Key struct{ Project, Role string }

// Counter reports how many sessions currently exist (are live) for a
// (project, role). Slice 1's implementation counts globally, preserving the
// dispatcher's prior max-concurrent semantics.
type Counter interface {
	LiveCount(project, role string) int
}

// Budget returns the per-(project, role) capacity; ok=false means no budget is
// configured for that pair, and admission fails closed.
type Budget interface {
	For(project, role string) (limit int, ok bool)
}

// StaticBudget is a fixed budget table. Slice 1 seeds it from the dispatcher's
// max-concurrent; a later slice replaces it with a roster-observed budget.
type StaticBudget map[Key]int

func (b StaticBudget) For(project, role string) (int, bool) {
	limit, ok := b[Key{Project: project, Role: role}]
	return limit, ok
}

// Allocator decides admission: may another session exist for (project, role)?
type Allocator struct {
	counter Counter
	budget  Budget
}

func New(counter Counter, budget Budget) *Allocator {
	return &Allocator{counter: counter, budget: budget}
}

// Admit reports whether a new session may be created for (project, role): the live
// count is strictly below the configured budget. Fail-closed when no budget.
func (a *Allocator) Admit(project, role string) bool {
	limit, ok := a.budget.For(project, role)
	if !ok {
		return false
	}
	return a.counter.LiveCount(project, role) < limit
}
