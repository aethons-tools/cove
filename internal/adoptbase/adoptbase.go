// Package adoptbase holds the pure file transforms that adopt a new base image:
// pinning .at-cove/config.yml's image.base and raising the blessed watermark.
// All functions operate on file *contents* (no I/O) so they are hermetically
// testable; the cmd/adopt-base shell reads/writes the files.
package adoptbase

import (
	"fmt"
	"regexp"
	"strings"
)

// CoveImageRepo is the toolchain-image repository that image.base names.
const CoveImageRepo = "ghcr.io/aethons-tools/cove-image"

const defaultReason = "breaking base change"

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validateDigest(d string) error {
	if !digestRE.MatchString(d) {
		return fmt.Errorf("adoptbase: %q is not a sha256:<64-hex> digest", d)
	}
	return nil
}

// leadingWhitespace returns the indentation prefix of a line.
func leadingWhitespace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// RewriteImageBase returns configYAML with the image.base value set to
// CoveImageRepo@digest, preserving indentation and surrounding comments. It
// errors unless exactly one image.base line for CoveImageRepo is present.
func RewriteImageBase(configYAML, digest string) (string, error) {
	if err := validateDigest(digest); err != nil {
		return "", err
	}
	lines := strings.Split(configYAML, "\n")
	matches := 0
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if !strings.HasPrefix(trimmed, "base:") ||
			(!strings.Contains(trimmed, CoveImageRepo+"@") && !strings.Contains(trimmed, CoveImageRepo+":")) {
			continue
		}
		lines[i] = leadingWhitespace(ln) + "base: " + CoveImageRepo + "@" + digest
		matches++
	}
	if matches != 1 {
		return "", fmt.Errorf("adoptbase: expected exactly 1 image.base line for %s, found %d", CoveImageRepo, matches)
	}
	return strings.Join(lines, "\n"), nil
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
			lines[i] = fmt.Sprintf("# Current watermark: cove-base-image:%s (%s)", tag, reason)
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
