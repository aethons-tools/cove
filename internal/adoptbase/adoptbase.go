// Package adoptbase holds the pure file transform that adopts a breaking base
// image: raising the blessed watermark. It operates on file *contents* (no
// I/O) so it is hermetically testable; the cmd/adopt-base shell reads/writes
// the file.
package adoptbase

import (
	"fmt"
	"regexp"
	"strings"
)

const defaultReason = "breaking base change"

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validateDigest(d string) error {
	if !digestRE.MatchString(d) {
		return fmt.Errorf("adoptbase: %q is not a sha256:<64-hex> digest", d)
	}
	return nil
}

// RewriteWatermark returns watermarkTxt with its single digest line set to digest
// and its "# Current watermark:" comment rewritten to name tag and reason. reason
// defaults when empty. It errors unless exactly one of each line is present.
func RewriteWatermark(watermarkTxt, tag, digest, reason string) (string, error) {
	if err := validateDigest(digest); err != nil {
		return "", err
	}
	if reason == "" {
		reason = defaultReason
	}
	lines := strings.Split(watermarkTxt, "\n")
	comments, digests := 0, 0
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(trimmed, "# Current watermark:"):
			lines[i] = fmt.Sprintf("# Current watermark: jam-studio-base:%s (%s)", tag, reason)
			comments++
		case trimmed != "" && !strings.HasPrefix(trimmed, "#"):
			lines[i] = digest
			digests++
		}
	}
	if comments != 1 || digests != 1 {
		return "", fmt.Errorf("adoptbase: unexpected watermark.txt shape: %d 'Current watermark' comment(s) and %d digest line(s), want 1 and 1", comments, digests)
	}
	return strings.Join(lines, "\n"), nil
}
