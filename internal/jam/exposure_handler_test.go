package jam

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type fakeExposureSetter struct {
	exposed  []string // "name|port"
	removed  []string
	list     []Exposure
	exposeEr error
	unexpEr  error
}

func (f *fakeExposureSetter) Expose(_, n string, p int) (Exposure, error) {
	if f.exposeEr != nil {
		return Exposure{}, f.exposeEr
	}
	f.exposed = append(f.exposed, n+"|"+strconv.Itoa(p))
	return Exposure{Name: n, Port: p}, nil
}

func (f *fakeExposureSetter) Unexpose(_, n string) error {
	f.removed = append(f.removed, n)
	return f.unexpEr
}

func (f *fakeExposureSetter) Exposures(string) []Exposure { return f.list }

func exposureFixture(urlFor func(actorID, name string) (string, error)) (*ExposureHandler, *fakeExposureSetter) {
	st := newFakeEscStore()
	st.actors[HashToken("tok")] = Actor{ID: "cove-1"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1"}
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
	if len(set.exposed) != 1 || set.exposed[0] != "web|3000" {
		t.Fatalf("exposed = %v", set.exposed)
	}

	set.list = []Exposure{{Name: "web", Port: 3000}, {Name: "api", Port: 8080}}
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
