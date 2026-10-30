package main

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"strings"
	"testing"
)

func TestEditorScheme(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/codium": "vscodium", "codium": "vscodium", "code-insiders": "vscode-insiders",
		"": "vscode", "code": "vscode", "/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code": "vscode",
	} {
		if got := editorScheme(in); got != want {
			t.Errorf("editorScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLauncherNameAndSlug(t *testing.T) {
	for _, c := range []struct{ url, scheme, name, slug string }{
		{"https://jam.example.com", "vscode", "Jam Hub (jam.example.com)", "jam.example.com"},
		{"https://h:8443/x", "vscodium", "Jam Hub (h:8443) VSCodium", "h-8443.vscodium"},
		{"https://h", "vscode-insiders", "Jam Hub (h) Insiders", "h.insiders"},
	} {
		name, err := launcherName(c.url, c.scheme)
		if err != nil || name != c.name {
			t.Errorf("launcherName(%q, %q) = %q, %v; want %q", c.url, c.scheme, name, err, c.name)
		}
		slug, err := launcherSlug(c.url, c.scheme)
		if err != nil || slug != c.slug {
			t.Errorf("launcherSlug(%q, %q) = %q, %v; want %q", c.url, c.scheme, slug, err, c.slug)
		}
	}
	if _, err := launcherName("not a url", "vscode"); err == nil {
		t.Error("launcherName without a host: no error")
	}
}

func TestHubLink(t *testing.T) {
	if got, want := hubLink("vscode", "https://jam.example.com"), "vscode://aethons.jam/hub?jam=https%3A%2F%2Fjam.example.com"; got != want {
		t.Errorf("hubLink = %q, want %q", got, want)
	}
}

func TestMacInfoPlist(t *testing.T) {
	p := macInfoPlist("Jam Hub (a&<b)", "h-8443.vscodium")
	var v any
	if err := xml.Unmarshal([]byte(p), &v); err != nil {
		t.Fatalf("plist is not XML: %v\n%s", err, p)
	}
	for _, want := range []string{"<key>CFBundleExecutable</key><string>jam-hub</string>", "<string>tools.aethons.jam.hub.h-8443.vscodium</string>", "<key>CFBundleIconFile</key><string>jam</string>", "<key>CFBundlePackageType</key><string>APPL</string>", "Jam Hub (a&amp;&lt;b)"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q:\n%s", want, p)
		}
	}
}

func TestMacScript(t *testing.T) {
	if got, want := macScript("vscode://aethons.jam/hub?jam=a'b%26c"), "#!/bin/sh\nexec /usr/bin/open 'vscode://aethons.jam/hub?jam=a'\\''b%26c'\n"; got != want {
		t.Errorf("macScript = %q, want %q", got, want)
	}
}

func TestICNS(t *testing.T) {
	png := []byte("\x89PNG-fake-data")
	n := uint32(len(png))
	var want bytes.Buffer
	want.WriteString("icns")
	_ = binary.Write(&want, binary.BigEndian, 16+n)
	want.WriteString("ic09")
	_ = binary.Write(&want, binary.BigEndian, 8+n)
	want.Write(png)
	if got := icns(png); !bytes.Equal(got, want.Bytes()) {
		t.Errorf("icns = %q, want %q", got, want.Bytes())
	}
}

func TestDesktopEntry(t *testing.T) {
	d := desktopEntry("Jam Hub (h)", "vscode://aethons.jam/hub?jam=https%3A%2F%2Fh", "/i/jam-hub.png")
	for _, want := range []string{"[Desktop Entry]\n", "Type=Application\n", "Name=Jam Hub (h)\n", `Exec=xdg-open "vscode://aethons.jam/hub?jam=https%%3A%%2F%%2Fh"` + "\n", "Icon=/i/jam-hub.png\n", "Terminal=false\n", "Categories=Development;\n"} {
		if !strings.Contains(d, want) {
			t.Errorf("desktop entry lacks %q:\n%s", want, d)
		}
	}
}
