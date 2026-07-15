package logging

import "strings"

func Scrub(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		s = strings.ReplaceAll(s, sec, "«redacted»")
	}
	return s
}
