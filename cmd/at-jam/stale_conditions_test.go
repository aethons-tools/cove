package main

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

func TestClearStaleCredConditions(t *testing.T) {
	tr := condition.New(condition.Options{})
	for _, k := range []string{
		condition.Key("cred.unavailable", "kept"),
		condition.Key("cred.unavailable", "removed"),
		condition.Key("pool.account.refresh", "removed"),
	} {
		tr.Raise(condition.Condition{Key: k, Severity: condition.Critical, Summary: "s"})
	}
	clearStaleCredConditions(tr, []string{"kept"})
	if !tr.IsOpen(condition.Key("cred.unavailable", "kept")) {
		t.Error("configured credential's condition was cleared")
	}
	if tr.IsOpen(condition.Key("cred.unavailable", "removed")) {
		t.Error("removed credential's condition still open")
	}
	if !tr.IsOpen(condition.Key("pool.account.refresh", "removed")) {
		t.Error("other kinds must be untouched")
	}
	clearStaleCredConditions(nil, []string{"x"}) // nil-safe
}
