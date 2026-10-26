package jam

import "testing"

func TestValidManagerName(t *testing.T) {
	if err := ValidManagerName("mac-1"); err != nil {
		t.Fatalf("mac-1: %v", err)
	}
	if err := ValidManagerName("local"); err == nil || err.Error() != `manager name "local" is reserved` {
		t.Fatalf("local: %v", err)
	}
	if err := ValidManagerName("Bad"); err == nil || err.Error() != `invalid manager name "Bad"` {
		t.Fatalf("Bad: %v", err)
	}
}
