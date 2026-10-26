package kit

import "testing"

func TestIsReservedSecretName(t *testing.T) {
	tests := []struct {
		name     string
		wantTrue bool
	}{
		{"AT_TASK_GIT_TOKEN", true},
		{"AT_DISPATCH_TRACKER_TOKEN", true},
		{"AT_DISPATCH_WEBHOOK_SECRET", true},
		{"GOOGLE_APPLICATION_CREDENTIALS_JSON", true},
		{"FOO", false},
		{"MYSECRET", false},
		{"CUSTOM_TOKEN", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsReservedSecretName(tc.name)
			if got != tc.wantTrue {
				t.Errorf("IsReservedSecretName(%q) = %v, want %v", tc.name, got, tc.wantTrue)
			}
		})
	}
}
