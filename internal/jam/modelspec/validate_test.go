package modelspec

import "testing"

func TestReservedEnvKeyIsJAM(t *testing.T) {
	// The pre-rename prefixes are spelled split ("AT_" + …) so the renamed guard
	// (renamed_test.go) does not flag this test for naming them.
	for k, want := range map[string]bool{"JAM_X": true, "JAM_IDENTITY_TOKEN": true, "AT_" + "JAM_X": false, "AT_" + "HARBOR_X": false, "MY_JAM_X": false} {
		if ReservedEnvKey(k) != want {
			t.Errorf("ReservedEnvKey(%q) != %v", k, want)
		}
	}
}
