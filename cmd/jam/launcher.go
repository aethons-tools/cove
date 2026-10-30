package main

// The Jam Hub launcher `jam vscode install` adds: a tiny macOS .app (keep it in
// the Dock) or a Linux .desktop entry that opens <scheme>://aethons.jam/hub?jam=<url>,
// which starts VS Code (or VSCodium) on that Jam's hub. The builders here are pure.

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

// editorScheme is the URI scheme of the editor whose CLI is codeCLI.
func editorScheme(codeCLI string) string {
	switch strings.TrimSuffix(filepath.Base(codeCLI), ".cmd") {
	case "codium":
		return "vscodium"
	case "code-insiders":
		return "vscode-insiders"
	}
	return "vscode"
}

func launcherHost(jamURL string) (string, error) {
	u, err := url.Parse(jamURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q has no host", jamURL)
	}
	return u.Host, nil
}

// launcherName is the launcher's display name: one per Jam and editor.
func launcherName(jamURL, scheme string) (string, error) {
	h, err := launcherHost(jamURL)
	if err != nil {
		return "", err
	}
	n := "Jam Hub (" + h + ")"
	switch scheme {
	case "vscodium":
		n += " VSCodium"
	case "vscode-insiders":
		n += " Insiders"
	}
	return n, nil
}

var slugUnsafe = regexp.MustCompile(`[^A-Za-z0-9.-]`)

// launcherSlug names the launcher's files and bundle id.
func launcherSlug(jamURL, scheme string) (string, error) {
	h, err := launcherHost(jamURL)
	if err != nil {
		return "", err
	}
	s := slugUnsafe.ReplaceAllString(h, "-")
	switch scheme {
	case "vscodium":
		s += ".vscodium"
	case "vscode-insiders":
		s += ".insiders"
	}
	return s, nil
}

// hubLink opens jamURL's hub in the editor with scheme.
func hubLink(scheme, jamURL string) string {
	return scheme + "://aethons.jam/hub?jam=" + url.QueryEscape(jamURL)
}

var plistTmpl = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
<key>CFBundleName</key><string>{{.Name}}</string>
<key>CFBundleDisplayName</key><string>{{.Name}}</string>
<key>CFBundleIdentifier</key><string>tools.aethons.jam.hub.{{.Slug}}</string>
<key>CFBundleExecutable</key><string>jam-hub</string>
<key>CFBundleIconFile</key><string>jam</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>1.0</string>
<key>LSMinimumSystemVersion</key><string>10.13</string>
</dict>
</plist>
`))

// macInfoPlist is the .app's Contents/Info.plist, its values XML-escaped.
func macInfoPlist(name, slug string) string {
	var b bytes.Buffer
	esc := func(s string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(s))
		return e.String()
	}
	_ = plistTmpl.Execute(&b, struct{ Name, Slug string }{esc(name), esc(slug)})
	return b.String()
}

// macScript is the .app's executable: it opens link, which the editor handles.
func macScript(link string) string {
	return "#!/bin/sh\nexec /usr/bin/open " + shellQuote(link) + "\n"
}

// icns wraps one 512 px PNG as an Apple icon file (a single ic09 entry).
func icns(png512 []byte) []byte {
	var b bytes.Buffer
	n := uint32(len(png512))
	b.WriteString("icns")
	_ = binary.Write(&b, binary.BigEndian, 16+n)
	b.WriteString("ic09")
	_ = binary.Write(&b, binary.BigEndian, 8+n)
	b.Write(png512)
	return b.Bytes()
}

// desktopEntry is the Linux launcher. The desktop entry spec reserves % in
// Exec (field codes), so the link's escapes are doubled.
func desktopEntry(name, link, iconPath string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=" + name + "\n" +
		`Exec=xdg-open "` + strings.ReplaceAll(link, "%", "%%") + "\"\n" +
		"Icon=" + iconPath + "\n" +
		"Terminal=false\n" +
		"Categories=Development;\n"
}

var errNoLauncher = errors.New("no launcher on this OS")

// writeLauncher writes the Jam Hub launcher for jamURL and the editor with
// scheme under home: a .app in ~/Applications on darwin (replaced whole), a
// .desktop entry and its icon on linux, else errNoLauncher. It returns the
// launcher's path.
func writeLauncher(home, goos, scheme, jamURL string, png []byte) (string, error) {
	name, err := launcherName(jamURL, scheme)
	if err != nil {
		return "", err
	}
	slug, err := launcherSlug(jamURL, scheme)
	if err != nil {
		return "", err
	}
	link := hubLink(scheme, jamURL)
	switch goos {
	case "darwin":
		app := filepath.Join(home, "Applications", name+".app")
		if err := os.RemoveAll(app); err != nil {
			return "", err
		}
		contents := filepath.Join(app, "Contents")
		for _, f := range []struct {
			path string
			data []byte
			mode os.FileMode
		}{
			{filepath.Join(contents, "Info.plist"), []byte(macInfoPlist(name, slug)), 0o644},
			{filepath.Join(contents, "MacOS", "jam-hub"), []byte(macScript(link)), 0o755},
			{filepath.Join(contents, "Resources", "jam.icns"), icns(png), 0o644},
		} {
			if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(f.path, f.data, f.mode); err != nil {
				return "", err
			}
		}
		return app, nil
	case "linux":
		share := filepath.Join(home, ".local", "share")
		icon := filepath.Join(share, "icons", "hicolor", "512x512", "apps", "jam-hub.png")
		entry := filepath.Join(share, "applications", "jam-hub-"+slug+".desktop")
		for _, f := range []struct {
			path string
			data []byte
		}{{icon, png}, {entry, []byte(desktopEntry(name, link, icon))}} {
			if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(f.path, f.data, 0o644); err != nil {
				return "", err
			}
		}
		return entry, nil
	}
	return "", errNoLauncher
}
