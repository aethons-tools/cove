package jam

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type fakeExposureSetter struct {
	exposed  []string // "actor|name|port"
	removed  []string // "actor|name"
	list     map[string][]Exposure
	exposeEr error
	unexpEr  error
}

func (f *fakeExposureSetter) Expose(a, n string, p int) (Exposure, error) {
	if f.exposeEr != nil {
		return Exposure{}, f.exposeEr
	}
	f.exposed = append(f.exposed, a+"|"+n+"|"+strconv.Itoa(p))
	return Exposure{Name: n, Port: p}, nil
}

func (f *fakeExposureSetter) Unexpose(a, n string) error {
	f.removed = append(f.removed, a+"|"+n)
	return f.unexpEr
}

func (f *fakeExposureSetter) Exposures(a string) []Exposure { return f.list[a] }

func exposureFixture(urlFor func(actorID, name string) (string, error)) (*ExposureHandler, *fakeExposureSetter) {
	st := newFakeEscStore()
	st.actors[HashToken("tok")] = Actor{ID: "cove-1"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1"}
	st.actors[HashToken("tok2")] = Actor{ID: "cove-2"}
	st.instances["cove-2"] = Instance{ActorID: "cove-2"}
	set := &fakeExposureSetter{}
	return NewExposureHandler(st, set, urlFor, testLogger()), set
}

func fakeURL(actorID, name string) (string, error) {
	return "https://" + name + "-" + actorID + ".example.test/", nil
}

func TestExposurePutDeleteGet(t *testing.T) {
	h, set := exposureFixture(fakeURL)
	rec := doTurnEnd(h, "PUT", "/exposures/web", "tok", `{"port":3000}`)
	if rec.Code != 200 {
		t.Fatalf("PUT %d %s", rec.Code, rec.Body)
	}
	for _, w := range []string{`"name":"web"`, `"port":3000`, `"url":"https://web-cove-1.example.test/"`} {
		if !strings.Contains(rec.Body.String(), w) {
			t.Errorf("PUT body missing %s: %s", w, rec.Body)
		}
	}
	if len(set.exposed) != 1 || set.exposed[0] != "cove-1|web|3000" {
		t.Fatalf("exposed = %v", set.exposed)
	}

	set.list = map[string][]Exposure{"cove-1": {{Name: "web", Port: 3000}, {Name: "api", Port: 8080}}}
	rec = doTurnEnd(h, "GET", "/exposures", "tok", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"exposures":[`) || !strings.Contains(rec.Body.String(), `"url":"https://api-cove-1.example.test/"`) {
		t.Fatalf("GET %d %s", rec.Code, rec.Body)
	}
	set.list = nil
	if rec = doTurnEnd(h, "GET", "/exposures", "tok", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"exposures":[]`) {
		t.Fatalf("empty GET %d %s", rec.Code, rec.Body)
	}

	if rec = doTurnEnd(h, "DELETE", "/exposures/web", "tok", ""); rec.Code != 204 {
		t.Fatalf("DELETE %d", rec.Code)
	}
	if len(set.removed) != 1 || set.removed[0] != "cove-1|web" {
		t.Fatalf("removed = %v", set.removed)
	}
	set.unexpEr = ErrNoExposure
	if rec = doTurnEnd(h, "DELETE", "/exposures/gone", "tok", ""); rec.Code != 404 {
		t.Fatalf("DELETE absent %d, want 404", rec.Code)
	}
}

func TestExposureRejects(t *testing.T) {
	h, set := exposureFixture(fakeURL)
	set.exposeEr = errors.New("name must be a DNS label")
	if rec := doTurnEnd(h, "PUT", "/exposures/Bad_Name", "tok", `{"port":3000}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "DNS label") {
		t.Fatalf("bad name → %d %s", rec.Code, rec.Body)
	}
	set.exposeEr = ErrExposureLimit
	if rec := doTurnEnd(h, "PUT", "/exposures/web", "tok", `{"port":3000}`); rec.Code != http.StatusConflict {
		t.Fatalf("limit → %d, want 409", rec.Code)
	}
	if rec := doTurnEnd(h, "PUT", "/exposures/web", "tok", `not json`); rec.Code != 400 {
		t.Fatalf("bad body → %d", rec.Code)
	}
	for _, c := range []struct {
		m, p, tok string
		want      int
	}{
		{"GET", "/exposures", "", 401},
		{"PUT", "/exposures/web", "nope", 401},
		{"POST", "/exposures", "tok", 405},
		{"GET", "/exposures/web", "tok", 405},
		{"PUT", "/exposures/a/b", "tok", 404},
	} {
		if rec := doTurnEnd(h, c.m, c.p, c.tok, `{"port":3000}`); rec.Code != c.want {
			t.Errorf("%s %s tok=%q → %d, want %d", c.m, c.p, c.tok, rec.Code, c.want)
		}
	}
}

func TestExposurePreviewsOff(t *testing.T) {
	h, set := exposureFixture(nil)
	for _, c := range []struct{ m, p string }{{"PUT", "/exposures/web"}, {"DELETE", "/exposures/web"}, {"GET", "/exposures"}} {
		rec := doTurnEnd(h, c.m, c.p, "tok", `{"port":3000}`)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "previews are not enabled on this Jam") {
			t.Errorf("%s %s → %d %s", c.m, c.p, rec.Code, rec.Body)
		}
	}
	if len(set.exposed)+len(set.removed) != 0 {
		t.Fatal("setter touched with previews off")
	}
	if rec := doTurnEnd(h, "GET", "/exposures", "", ""); rec.Code != 401 {
		t.Fatalf("unauthenticated with previews off → %d, want 401", rec.Code)
	}
}

// Each call acts on the caller's own session: two tokens, two actors.
func TestExposureActorScoped(t *testing.T) {
	h, set := exposureFixture(fakeURL)
	set.list = map[string][]Exposure{"cove-1": {{Name: "web", Port: 3000}}, "cove-2": {{Name: "api", Port: 8080}}}
	rec := doTurnEnd(h, "PUT", "/exposures/docs", "tok2", `{"port":4000}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"url":"https://docs-cove-2.example.test/"`) {
		t.Fatalf("PUT as cove-2 → %d %s", rec.Code, rec.Body)
	}
	if len(set.exposed) != 1 || set.exposed[0] != "cove-2|docs|4000" {
		t.Fatalf("exposed = %v, want cove-2's", set.exposed)
	}
	for tok, want := range map[string]string{"tok": `"name":"web"`, "tok2": `"name":"api"`} {
		rec := doTurnEnd(h, "GET", "/exposures", tok, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) || strings.Count(rec.Body.String(), `"name"`) != 1 {
			t.Errorf("GET as %s → %d %s, want only %s", tok, rec.Code, rec.Body, want)
		}
	}
	if rec := doTurnEnd(h, "DELETE", "/exposures/api", "tok2", ""); rec.Code != 204 || len(set.removed) != 1 || set.removed[0] != "cove-2|api" {
		t.Fatalf("DELETE as cove-2 → %d, removed = %v", rec.Code, set.removed)
	}
}

// A name the URL mapper refuses never reaches Expose (no orphan exposure).
func TestExposureURLErrorExposesNothing(t *testing.T) {
	h, set := exposureFixture(func(string, string) (string, error) { return "", errors.New("preview name \"x--y\": no '--'") })
	rec := doTurnEnd(h, "PUT", "/exposures/x--y", "tok", `{"port":3000}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "no '--'") {
		t.Fatalf("PUT → %d %s, want 400 with the reason", rec.Code, rec.Body)
	}
	if len(set.exposed) != 0 {
		t.Fatalf("exposed = %v, want none", set.exposed)
	}
}
