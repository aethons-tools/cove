package modelspec

import (
	"strings"
	"testing"
)

func TestCheckPermissionMode(t *testing.T) {
	for _, mode := range append(PermissionModes(), "") {
		if err := CheckPermissionMode(mode); err != nil {
			t.Errorf("mode %q refused: %v", mode, err)
		}
	}
	for mode, want := range map[string]string{"yolo": `"yolo"`, ModePlan: "never leave plan mode", "Default": `"Default"`} {
		if err := CheckPermissionMode(mode); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("mode %q: err = %v, want one mentioning %q", mode, err, want)
		}
	}
}

func TestPermissionModesIsACopy(t *testing.T) {
	m := PermissionModes()
	m[0] = "mutated"
	if PermissionModes()[0] == "mutated" {
		t.Fatal("PermissionModes shares its backing array")
	}
}
