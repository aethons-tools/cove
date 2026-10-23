package userui

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

const testVAPIDKey = "BOtestPublicKey"

var (
	goodP256dh = base64.RawURLEncoding.EncodeToString(append([]byte{4}, make([]byte, 64)...))
	goodAuth   = base64.RawURLEncoding.EncodeToString(make([]byte, 16))
)

func subBody(endpoint, p256dh, auth string) string {
	b, _ := json.Marshal(map[string]any{
		"endpoint":       endpoint,
		"expirationTime": nil,
		"keys":           map[string]string{"p256dh": p256dh, "auth": auth},
	})
	return string(b)
}

func pushDo(h http.Handler, p jam.Participant, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TestBrowser/1.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jam.WithParticipant(req, p))
	return rec
}

func TestPushKeyReturnsConfiguredKey(t *testing.T) {
	e, p := fixture()
	rec := pushDo(Handler(e.Deps, nil, WithPush(testVAPIDKey)), p, "GET", "/app/user/push/key", "")
	if rec.Code != 200 {
		t.Fatalf("GET key = %d", rec.Code)
	}
	var got struct{ Key string }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Key != testVAPIDKey {
		t.Fatalf("key body = %q (%v)", rec.Body.String(), err)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content type = %q", ct)
	}
}

func TestPushSubscribeStoresSubscription(t *testing.T) {
	e, p := fixture()
	h := Handler(e.Deps, nil, WithPush(testVAPIDKey))
	ep := "https://fcm.googleapis.com/fcm/send/abc"
	rec := pushDo(h, p, "POST", "/app/user/push/subscriptions", subBody(ep, goodP256dh, goodAuth))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("subscribe = %d %s", rec.Code, rec.Body.String())
	}
	subs := e.st.PushSubscriptions(p.UserID)
	if len(subs) != 1 {
		t.Fatalf("subs = %+v", subs)
	}
	s := subs[0]
	if s.Endpoint != ep || s.UserID != p.UserID || s.P256dh != goodP256dh || s.Auth != goodAuth || s.UserAgent != "TestBrowser/1.0" || s.CreatedAt.IsZero() {
		t.Errorf("stored = %+v", s)
	}
}

func TestPushSubscribeRejectsBadInput(t *testing.T) {
	e, p := fixture()
	h := Handler(e.Deps, nil, WithPush(testVAPIDKey))
	long := "https://push.example/" + strings.Repeat("a", 2048)
	short := base64.RawURLEncoding.EncodeToString(make([]byte, 64))
	cases := map[string]string{
		"http endpoint":  subBody("http://push.example/x", goodP256dh, goodAuth),
		"long endpoint":  subBody(long, goodP256dh, goodAuth),
		"not a url":      subBody("not a url", goodP256dh, goodAuth),
		"empty endpoint": subBody("", goodP256dh, goodAuth),
		"no host":        subBody("https:///x", goodP256dh, goodAuth),
		"short p256dh":   subBody("https://push.example/x", short, goodAuth),
		"bad auth":       subBody("https://push.example/x", goodP256dh, base64.RawURLEncoding.EncodeToString(make([]byte, 15))),
		"auth not b64":   subBody("https://push.example/x", goodP256dh, "!!!!"),
		"malformed json": `{"endpoint":`,
		"oversize body":  `{"endpoint":"https://push.example/x","pad":"` + strings.Repeat("a", 9000) + `"}`,
	}
	for name, body := range cases {
		if rec := pushDo(h, p, "POST", "/app/user/push/subscriptions", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: = %d, want 400", name, rec.Code)
		}
	}
	if subs := e.st.PushSubscriptions(p.UserID); len(subs) != 0 {
		t.Errorf("bad input stored %+v", subs)
	}
}

func TestPushUnsubscribeDeletesOnlyOwnRow(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bobID, _ := e.st.LookupName(ident.User, "bob")
	bobEP := "https://push.example/bob"
	if err := e.st.PutPushSubscription(jam.PushSubscription{Endpoint: bobEP, UserID: bobID, P256dh: goodP256dh, Auth: goodAuth}); err != nil {
		t.Fatal(err)
	}
	h := Handler(e.Deps, nil, WithPush(testVAPIDKey))
	mine := "https://push.example/alice"
	if rec := pushDo(h, p, "POST", "/app/user/push/subscriptions", subBody(mine, goodP256dh, goodAuth)); rec.Code != 204 {
		t.Fatalf("subscribe = %d", rec.Code)
	}
	if rec := pushDo(h, p, "DELETE", "/app/user/push/subscriptions", `{"endpoint":"`+mine+`"}`); rec.Code != 204 {
		t.Fatalf("delete own = %d", rec.Code)
	}
	if subs := e.st.PushSubscriptions(p.UserID); len(subs) != 0 {
		t.Errorf("own row survived: %+v", subs)
	}
	if rec := pushDo(h, p, "DELETE", "/app/user/push/subscriptions", `{"endpoint":"`+bobEP+`"}`); rec.Code != 204 {
		t.Fatalf("delete other's = %d", rec.Code)
	}
	if subs := e.st.PushSubscriptions(bobID); len(subs) != 1 {
		t.Errorf("another user's row was deleted: %+v", subs)
	}
	if rec := pushDo(h, p, "DELETE", "/app/user/push/subscriptions", `{`); rec.Code != 400 {
		t.Errorf("malformed delete = %d, want 400", rec.Code)
	}
}

func TestPushRoutesOffWithoutKey(t *testing.T) {
	e, p := fixture()
	for _, h := range []http.Handler{Handler(e.Deps, nil), Handler(e.Deps, nil, WithPush(""))} {
		for _, c := range []struct{ method, path, body string }{
			{"GET", "/app/user/push/key", ""},
			{"POST", "/app/user/push/subscriptions", subBody("https://push.example/x", goodP256dh, goodAuth)},
			{"DELETE", "/app/user/push/subscriptions", `{"endpoint":"https://push.example/x"}`},
		} {
			if rec := pushDo(h, p, c.method, c.path, c.body); rec.Code != http.StatusNotFound {
				t.Errorf("%s %s without push = %d, want 404", c.method, c.path, rec.Code)
			}
		}
	}
}
