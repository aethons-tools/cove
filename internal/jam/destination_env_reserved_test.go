package jam

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

// A destination env key stored before JAM_* became Jam's reserved prefix
// does not fail every connector in its scope: it is dropped, the rest kept.
func TestConnectorForDropsAStoredReservedEnvKey(t *testing.T) {
	d := ghAPI
	d.Env = map[string]string{"GH_HOST": "{host}", "JAM_LEGACY": "{token}"}
	st := connectorStore(t, []Destination{d}, map[string]Scope{"w": {Destinations: []string{"github-api"}}})
	c, err := ConnectorFor(st, actorWith("w"))
	if err != nil || c.Env["GH_HOST"] != "{host}" {
		t.Fatalf("connector = %+v, %v", c, err)
	}
	if _, ok := c.Env["JAM_LEGACY"]; ok {
		t.Fatalf("a reserved key reached the connector: %+v", c.Env)
	}
}

func TestReservedDestinationEnvConditions(t *testing.T) {
	d := ghAPI
	d.Env = map[string]string{"GH_HOST": "{host}", "JAM_LEGACY": "{token}"}
	st := connectorStore(t, []Destination{d, legacyAnthropic}, nil)
	tr := condition.New(condition.Options{})
	ReservedDestinationEnvConditions(st, tr)
	c, ok := tr.Get(condition.Key(DestinationEnvKind, "github-api"))
	if !ok || c.Summary != "env key JAM_LEGACY is reserved (JAM_*); rename it" {
		t.Fatalf("condition = %+v, %v", c, ok)
	}
	if open := tr.OpenKeys(DestinationEnvKind); len(open) != 1 {
		t.Fatalf("open = %v", open)
	}
	// Renamed: the next check clears it.
	d.Env = map[string]string{"GH_HOST": "{host}", "LEGACY": "{token}"}
	if err := st.RemoveDestination(d.Name); err != nil {
		t.Fatal(err)
	}
	if err := st.AddDestination(d); err != nil {
		t.Fatal(err)
	}
	ReservedDestinationEnvConditions(st, tr)
	if tr.IsOpen(condition.Key(DestinationEnvKind, "github-api")) {
		t.Fatal("the condition must clear once the key is renamed")
	}
}

func TestReservedDestinationEnvConditionsNameEveryKey(t *testing.T) {
	d := ghAPI
	d.Env = map[string]string{"JAM_B": "{token}", "JAM_A": "{host}"}
	st := connectorStore(t, []Destination{d}, nil)
	tr := condition.New(condition.Options{})
	ReservedDestinationEnvConditions(st, tr)
	c, _ := tr.Get(condition.Key(DestinationEnvKind, "github-api"))
	if c.Summary != "env keys JAM_A, JAM_B are reserved (JAM_*); rename them" ||
		c.Detail != "destination github-api sets JAM_A, JAM_B; Jam drops them from every connector, as JAM_* names are Jam's own" {
		t.Fatalf("condition = %+v", c)
	}
	d.Env = map[string]string{"JAM_A": "{host}"}
	_ = st.RemoveDestination(d.Name)
	_ = st.AddDestination(d)
	ReservedDestinationEnvConditions(st, tr)
	if c, _ := tr.Get(condition.Key(DestinationEnvKind, "github-api")); c.Detail != "destination github-api sets JAM_A; Jam drops it from every connector, as JAM_* names are Jam's own" {
		t.Fatalf("one key: %+v", c)
	}
}
