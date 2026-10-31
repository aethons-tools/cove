package adminui

import "testing"

func TestOnBranchPath(t *testing.T) {
	for p, want := range map[string]bool{
		apiPrefix + "users":                                         true,
		apiPrefix + "projects/app/roles/ic":                         true,
		apiPrefix + "users/u/accounts":                              false,
		apiPrefix + "projects/app/roles/ic/request":                 false,
		apiPrefix + "projects/app/roles/ic/standing/spider/reset":   false,
		apiPrefix + "projects/app/roles/ic/standing/spider/upgrade": false,
		apiPrefix + "projects/app/roles/ic/standing":                true,
		apiPrefix + "dashboard":                                     false,
	} {
		if got := onBranchPath(p); got != want {
			t.Errorf("%s = %v, want %v", p, got, want)
		}
	}
}
