package jam

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// oldFlatFile is a destination file with every field, as Jam rendered it
// before endpoints existed.
const oldFlatFile = `kind: destination
version: 1
route: /gl/
upstream: https://gitlab.example/api/v4
identity_in: custom
cred_name: gl-pat
apply: custom
identity_in_spec:
  header: Private-Token
  prefixes:
    - 'Jam '
apply_spec:
  header: Private-Token
  template: '{cred}'
env:
  GITLAB_HOST: '{host}'
git: true
allow_paths:
  - /projects/*
note: use glab
log_headers_in: examined
log_headers_out: changed
operator-notes: ops
`

// oldFlatJSON are destinations as Jam's store and backups encoded them before
// endpoints existed.
var oldFlatJSON = []string{
	`{"name":"gl","route":"/gl/","upstream":"https://gitlab.example/api/v4","identity_in":"custom","cred_name":"gl-pat","apply":"custom","identity_in_spec":{"header":"Private-Token","prefixes":["Jam "]},"apply_spec":{"header":"Private-Token","template":"{cred}"},"env":{"GITLAB_HOST":"{host}"},"git":true,"allow_paths":["/projects/*"],"note":"use glab","log_headers_in":"examined","log_headers_out":"changed","operator_notes":"ops"}`,
	`{"name":"anthropic","route":"/anthropic/","upstream":"https://api.anthropic.com","identity_in":"bearer","cred_name":"c","apply":"bearer"}`,
	`{"name":"x","route":"","upstream":"","identity_in":"","cred_name":"","apply":"","oauth_beta":true}`,
}

// oldFlatDest is oldFlatFile / oldFlatJSON[0] in the model.
func oldFlatDest() Destination {
	return Destination{Name: "gl", CredName: "gl-pat", Env: map[string]string{"GITLAB_HOST": "{host}"}, Note: "use glab", OperatorNotes: "ops",
		Endpoints: []Endpoint{{
			Name: "gl", Route: "/gl/", Upstream: "https://gitlab.example/api/v4",
			IdentityIn: ApplyCustom, IdentityInSpec: &InboundSpec{Header: "Private-Token", Prefixes: []string{"Jam "}},
			Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "Private-Token", Template: "{cred}"},
			Git: true, AllowPaths: []string{"/projects/*"}, LogHeadersIn: HeaderLogExamined, LogHeadersOut: HeaderLogChanged,
		}},
	}
}

// A flat file is one endpoint named after the destination, and renders back
// byte for byte, so the config plan sees no change.
func TestDestinationFileFlatFormUnchanged(t *testing.T) {
	f, err := ParseDestinationFile("destinations/gl.yml", "gl", []byte(oldFlatFile))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Destination, oldFlatDest()) {
		t.Fatalf("parsed %+v", f.Destination)
	}
	if got := string(newConfigObject(f).Files["destinations/gl.yml"]); got != oldFlatFile {
		t.Fatalf("render:\n%s\nwant:\n%s", got, oldFlatFile)
	}
}

// Stored documents and backups from before endpoints decode to one endpoint
// (none when the flat fields are empty) and encode back byte for byte.
func TestDestinationJSONFlatFormUnchanged(t *testing.T) {
	for i, in := range oldFlatJSON {
		var d Destination
		if err := json.Unmarshal([]byte(in), &d); err != nil {
			t.Fatal(err)
		}
		if i == 0 && !reflect.DeepEqual(d, oldFlatDest()) {
			t.Errorf("decoded %+v", d)
		}
		if i == 2 && (len(d.Endpoints) != 0 || !d.LegacyOAuthBeta) {
			t.Errorf("empty flat form decoded %+v", d)
		}
		out, err := json.Marshal(d)
		if err != nil || string(out) != in {
			t.Errorf("re-encoded %s, %v\nwant %s", out, err, in)
		}
	}
}

const listFile = `kind: destination
version: 1
cred_name: gh
endpoints:
  - name: rest
    route: /api/v3/
    upstream: https://api.github.com
    identity_in: bearer
    apply: bearer
    log_headers_in: all
  - name: git
    route: /git/
    upstream: https://github.com
    identity_in: basic-password
    apply: basic-password
    git: true
    allow_paths:
      - /*/*.git/*
env:
  GH_HOST: '{host}'
note: GitHub
`

func listDest() Destination {
	return Destination{Name: "github", CredName: "gh", Env: map[string]string{"GH_HOST": "{host}"}, Note: "GitHub", Endpoints: []Endpoint{
		{Name: "rest", Route: "/api/v3/", Upstream: "https://api.github.com", IdentityIn: ApplyBearer, Apply: ApplyBearer, LogHeadersIn: HeaderLogAll},
		{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: ApplyBasicPassword, Apply: ApplyBasicPassword, Git: true, AllowPaths: []string{"/*/*.git/*"}},
	}}
}

// The endpoints: list parses, renders back as written, and round-trips
// through JSON.
func TestDestinationListForm(t *testing.T) {
	f, err := ParseDestinationFile("destinations/github.yml", "github", []byte(listFile))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Destination, listDest()) {
		t.Fatalf("parsed %+v", f.Destination)
	}
	if got := string(newConfigObject(f).Files["destinations/github.yml"]); got != listFile {
		t.Fatalf("render:\n%s\nwant:\n%s", got, listFile)
	}
	b, err := json.Marshal(listDest())
	if err != nil || !strings.Contains(string(b), `"endpoints":[`) || strings.Contains(string(b), `"route":""`) {
		t.Fatalf("json %s, %v", b, err)
	}
	var back Destination
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, listDest()) {
		t.Fatalf("json round trip %+v, %v", back, err)
	}
}

// A list of one unnamed endpoint is named after the destination, as the flat
// form is, and so renders flat.
func TestDestinationListOfOneNormalizes(t *testing.T) {
	in := "kind: destination\nversion: 1\nendpoints:\n  - route: /o/\n    upstream: https://o.example\n"
	f, err := ParseDestinationFile("destinations/o.yml", "o", []byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Destination.Endpoints) != 1 || f.Destination.Endpoints[0].Name != "o" {
		t.Fatalf("parsed %+v", f.Destination)
	}
	flat, err := ParseDestinationFile("destinations/o.yml", "o", []byte("kind: destination\nversion: 1\nroute: /o/\nupstream: https://o.example\n"))
	if err != nil || newConfigObject(f).Digest() != newConfigObject(flat).Digest() {
		t.Fatalf("list of one and flat differ: %v", err)
	}
}

// Files are decoded strictly in both forms, and may not mix them.
func TestDestinationFileRefusals(t *testing.T) {
	for name, in := range map[string]string{
		"unknown flat field":     "kind: destination\nversion: 1\nroute: /o/\nupstreem: x\n",
		"unknown endpoint field": "kind: destination\nversion: 1\nendpoints:\n  - name: a\n    rout: /o/\n",
		"both forms":             "kind: destination\nversion: 1\nroute: /o/\nendpoints:\n  - name: a\n    route: /a/\n",
	} {
		if _, err := ParseDestinationFile("destinations/o.yml", "o", []byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
