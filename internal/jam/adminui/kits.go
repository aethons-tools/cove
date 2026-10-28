package adminui

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
)

// kitURL is the page path for a kit.
func kitURL(name string) string { return "/app/admin/kits/" + url.PathEscape(name) }

func pushedMsg(name string, v int, unchanged bool) string {
	if unchanged {
		return fmt.Sprintf("%s unchanged (current v%d)", name, v)
	}
	return fmt.Sprintf("pushed %s v%d", name, v)
}

func sourceMsg(what string, res jam.KitSourceResult) string {
	msg := fmt.Sprintf("%s: %s is now v%d", what, res.Name, res.Version)
	if res.Unchanged {
		msg = fmt.Sprintf("%s: %s unchanged (current v%d)", what, res.Name, res.Version)
	}
	if len(res.Also) == 0 {
		return msg
	}
	with := make([]string, len(res.Also))
	for i, a := range res.Also {
		with[i] = fmt.Sprintf("%s v%d", a.Name, a.Version)
		if a.Unchanged {
			with[i] += " (unchanged)"
		}
	}
	return msg + ". With: " + strings.Join(with, ", ")
}
