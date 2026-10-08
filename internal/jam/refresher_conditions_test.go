package jam

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

func jsonDecode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

type memPoolStore struct{ accts []PoolAccount }

func (m *memPoolStore) Accounts() ([]PoolAccount, error) { return m.accts, nil }
func (m *memPoolStore) SetAccount(a PoolAccount) error {
	for i := range m.accts {
		if m.accts[i].Name == a.Name {
			m.accts[i] = a
		}
	}
	return nil
}
func (m *memPoolStore) Bindings() (map[string]string, error)        { return map[string]string{}, nil }
func (m *memPoolStore) Bind(identityHash, accountName string) error { return nil }

func TestRefresherRaisesPoolConditions(t *testing.T) {
	failing := map[string]bool{"a": true, "b": true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = jsonDecode(r, &body)
		if failing[body.RefreshToken] {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"new","expires_in":3600}`))
	}))
	defer srv.Close()
	now := time.Unix(1_000_000, 0)
	st := &memPoolStore{accts: []PoolAccount{{Name: "a", RefreshToken: "a", ExpiresAt: now}, {Name: "b", RefreshToken: "b", ExpiresAt: now}}}
	tr := condition.New(condition.Options{})
	r := NewRefresher(st, RefresherOptions{TokenURL: srv.URL, Now: func() time.Time { return now }, Conditions: tr})
	ka, kb := condition.Key("pool.account.refresh", "a"), condition.Key("pool.account.refresh", "b")

	_ = r.RefreshDue(context.Background()) // 1st failing pass: below threshold
	if tr.IsOpen(ka) {
		t.Fatal("raised after one pass")
	}
	_ = r.RefreshDue(context.Background()) // 2nd: both open, and every account failing => critical
	ca, _ := tr.Get(ka)
	cb, _ := tr.Get(kb)
	if ca.Severity != condition.Critical || cb.Severity != condition.Critical {
		t.Fatalf("all failing should be critical: %s %s", ca.Severity, cb.Severity)
	}
	failing["b"] = false // b recovers => b clears, a drops back to warning
	_ = r.RefreshDue(context.Background())
	if tr.IsOpen(kb) {
		t.Fatal("recovered account not cleared")
	}
	if ca, _ = tr.Get(ka); ca.Severity != condition.Warning {
		t.Fatalf("a should be warning once not all fail: %s", ca.Severity)
	}
}
