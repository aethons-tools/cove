package preview

import (
	"strings"
	"testing"
)

func TestNamesAndPorts(t *testing.T) {
	for _, ok := range []string{"a", "app", "vite-dev", "a1", strings.Repeat("x", 30)} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a_b", "a--b", "xn", "a.b", strings.Repeat("x", 31), "jam"} {
		if ValidName(bad) == nil {
			t.Errorf("ValidName(%q) accepted", bad)
		}
	}
	for _, p := range []int{1024, 5173, 65535} {
		if ValidPort(p) != nil {
			t.Errorf("port %d refused", p)
		}
	}
	for _, p := range []int{0, 80, 1023, 2222, 3128, 65536} {
		if ValidPort(p) == nil {
			t.Errorf("port %d accepted", p)
		}
	}
}

func TestHostRoundTrip(t *testing.T) {
	const id = "ses_01m4bc9fmg5v4bc7cq3at6jp6n"
	const label = "01m4bc9fmg5v4bc7cq3at6jp6n"
	h, err := Host("preview.jam.example", "vite", id)
	if err != nil || h != "vite--"+label+".preview.jam.example" {
		t.Fatalf("Host = %q, %v", h, err)
	}
	for _, in := range []string{h, strings.ToUpper(h), h + ":443"} {
		name, got, ok := Parse(in, "preview.jam.example")
		if !ok || name != "vite" || got != label {
			t.Errorf("Parse(%q) = %q %q %v", in, name, got, ok)
		}
	}
	for _, bad := range []string{
		"vite--01m4bc9fmg5v4bc7cq3at6jp6n.preview.jam.example.evil.com",
		"vite--01m4bc9fmg5v4bc7cq3at6jp6n.other.example",
		"a.vite--01m4bc9fmg5v4bc7cq3at6jp6n.preview.jam.example",
		"vite-01m4bc9fmg5v4bc7cq3at6jp6n.preview.jam.example",
		"vite--short.preview.jam.example",
		"jam.preview.jam.example",
		"preview.jam.example",
	} {
		if _, _, ok := Parse(bad, "preview.jam.example"); ok {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
	if LoginHost("preview.jam.example") != "jam.preview.jam.example" || !IsPreviewHost("jam.preview.jam.example:443", "preview.jam.example") {
		t.Fatal("login host")
	}
}

func TestNonSessionIDsHashToStableLabels(t *testing.T) {
	ids := []string{"standing-test-jam-ic-admin-spider", "spider-42", "ses_short", "ses_01M4BC9FMG5V4BC7CQ3AT6JP6N"}
	seen := map[string]string{}
	for _, id := range ids {
		l := Label(id)
		if !validLabel(l) || l != Label(id) {
			t.Fatalf("Label(%q) = %q: invalid or unstable", id, l)
		}
		if prev, dup := seen[l]; dup {
			t.Fatalf("Label collision %q / %q", id, prev)
		}
		seen[l] = id
		h, err := Host("p.example", "app", id)
		if err != nil {
			t.Fatal(err)
		}
		name, got, ok := Parse(h, "p.example")
		if !ok || name != "app" || got != l {
			t.Fatalf("Parse(%q) = %q %q %v; want app %q", h, name, got, ok, l)
		}
		if a, ok := ActorForLabel(got, ids); !ok || a != id {
			t.Fatalf("ActorForLabel = %q %v; want %q", a, ok, id)
		}
	}
	if _, ok := ActorForLabel("01m4bc9fmg5v4bc7cq3at6jp6n", ids); ok {
		t.Fatal("unknown label matched")
	}
}

func TestParseNormalisationAndNegatives(t *testing.T) {
	const d = "preview.jam.example"
	const l = "01m4bc9fmg5v4bc7cq3at6jp6n"
	if _, got, ok := Parse("vite--"+l+"."+d+".", d); !ok || got != l {
		t.Errorf("trailing dot: %q %v", got, ok)
	}
	if !IsPreviewHost("jam."+d+".", d) {
		t.Error("FQDN login host")
	}
	if h, _ := Host("Preview.Jam.Example", "vite", "ses_"+l); h != "vite--"+l+"."+d {
		t.Errorf("Host domain not lowercased: %q", h)
	}
	for _, bad := range []string{
		strings.Repeat("x", 31) + "--" + l + "." + d,
		"vite--" + strings.ToUpper(l)[:0] + "01m4bc9fmg5v4bc7cq3at6jp6!" + "." + d,
		"vite--01m4bc9fmg5v4bc7cq3at6jp6_." + d,
		"vite--." + d,
		"vite--" + l + "--x." + d,
		"vite--01m4bc9fmg5v4bc7cq3at--x." + d,
		"xn--" + l + "." + d,
		"example.com:8080",
		"localhost:80",
	} {
		if _, _, ok := Parse(bad, d); ok {
			t.Errorf("Parse(%q) accepted", bad)
		}
		if IsPreviewHost(bad, d) {
			t.Errorf("IsPreviewHost(%q) accepted", bad)
		}
	}
}

func TestUnderDomain(t *testing.T) {
	const d = "preview.jam.example"
	for _, h := range []string{d, "jam." + d, "foo." + d, "a.b." + d, "Foo.Preview.JAM.example", "foo." + d + ".", "foo." + d + ":8443", "bad_label." + d} {
		if !UnderDomain(h, d) {
			t.Errorf("UnderDomain(%q) = false", h)
		}
	}
	for _, h := range []string{"", "jam.example", "xpreview.jam.example", "preview.jam.example.evil", "[::1]:8443", "127.0.0.1"} {
		if UnderDomain(h, d) {
			t.Errorf("UnderDomain(%q) = true", h)
		}
	}
}

func TestValidPortNamesReservedPorts(t *testing.T) {
	for port, want := range map[int]string{2222: "not 2222 (sshd), 3128 (egress proxy) or 13337 (the built-in IDE)", 3128: "not 2222 (sshd), 3128 (egress proxy) or 13337 (the built-in IDE)"} {
		if err := ValidPort(port); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ValidPort(%d) = %v, want %q", port, err, want)
		}
	}
}

func TestIDEPortReserved(t *testing.T) {
	if err := ValidPort(IDEPort); err == nil {
		t.Fatal("the IDE port must not be exposable")
	}
	if err := ValidName(IDEName); err != nil {
		t.Fatalf("ide must stay a valid name (a kit overrides the built-in IDE by exposing it): %v", err)
	}
}
