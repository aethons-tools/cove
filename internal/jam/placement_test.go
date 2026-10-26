package jam

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestKitNotReadyErrorIsErrKitNotReady(t *testing.T) {
	err := fmt.Errorf("raise: %w", &KitNotReadyError{Manager: "mac-1"})
	if !errors.Is(err, ErrKitNotReady) {
		t.Fatal("want errors.Is ErrKitNotReady")
	}
	var k *KitNotReadyError
	if !errors.As(err, &k) || k.Manager != "mac-1" {
		t.Fatalf("got %v", k)
	}
}

func TestNoManagerErrorListsReasons(t *testing.T) {
	e := &NoManagerError{Reasons: []string{"mac-1: at capacity", "local: unavailable"}}
	want := "no manager could host it: mac-1: at capacity; local: unavailable"
	if e.Error() != want {
		t.Fatalf("got %q", e.Error())
	}
}

func TestInstanceManagerRoundTripsJSON(t *testing.T) {
	// Test that Instance.Manager field is properly marshaled and unmarshaled
	inst := Instance{
		ActorID: "test-actor",
		Manager: "local",
	}

	// Marshal to JSON
	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Verify that Manager field is in the JSON
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal to map failed: %v", err)
	}

	if _, ok := m["manager"]; !ok {
		t.Fatalf("manager field not in JSON: %v", string(data))
	}

	// Unmarshal back to Instance
	var inst2 Instance
	if err := json.Unmarshal(data, &inst2); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Verify Manager field survives round-trip
	if inst2.Manager != "local" {
		t.Fatalf("Manager field not preserved: got %q, want %q", inst2.Manager, "local")
	}

	// Test that empty Manager is omitted (due to omitempty tag)
	inst3 := Instance{
		ActorID: "test-actor",
		Manager: "",
	}

	data3, err := json.Marshal(inst3)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var m3 map[string]interface{}
	if err := json.Unmarshal(data3, &m3); err != nil {
		t.Fatalf("Unmarshal to map failed: %v", err)
	}

	if _, ok := m3["manager"]; ok {
		t.Fatalf("empty manager field should be omitted from JSON: %v", string(data3))
	}
}
