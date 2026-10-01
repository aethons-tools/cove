package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"strings"
)

// ScanContextTar is the prepare-time, fail-closed pre-flight for a context
// tar.gz before it is streamed to `docker build -`. It walks the tar headers
// (through gzip) enforcing the hard caps and name/entry safety, so an untrusted
// context can neither bomb the build VM nor smuggle an unsafe entry past docker:
//
//   - at most maxZipEntries entries;
//   - cumulative uncompressed bytes ≤ maxDecompressedZip, measured by actually
//     reading the stream (a counting reader), not trusting the header Size;
//   - no symlink / hardlink / otherwise non-regular entries;
//   - no absolute or `..`-containing names;
//   - a root Dockerfile is present.
//
// It extracts nothing — docker owns the actual extraction; this only gates.
func ScanContextTar(b []byte) error {
	gr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("base.context: not a readable gzip: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var entries int
	var total int64
	var sawDockerfile bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("base.context: not a readable tar: %w", err)
		}
		entries++
		if entries > maxZipEntries {
			return fmt.Errorf("base.context: more than %d entries", maxZipEntries)
		}
		name := hdr.Name
		if path.IsAbs(name) || strings.HasPrefix(name, "/") || hasDotDotComponent(name) {
			return fmt.Errorf("base.context: entry %q escapes the build context", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeSymlink:
			return fmt.Errorf("base.context: entry %q is a symlink (not allowed)", hdr.Name)
		case tar.TypeLink:
			return fmt.Errorf("base.context: entry %q is a hard link (not allowed)", hdr.Name)
		case tar.TypeReg:
			// ok — counted below
		default:
			return fmt.Errorf("base.context: entry %q is not a regular file", hdr.Name)
		}
		if path.Clean(name) == "Dockerfile" {
			sawDockerfile = true
		}
		// Count the ACTUAL decompressed bytes (not the header's Size), bounding the
		// read so a lying header or a decompression bomb trips the cap instead of
		// filling memory.
		n, err := io.Copy(io.Discard, io.LimitReader(tr, int64(maxDecompressedZip)-total+1))
		if err != nil {
			return fmt.Errorf("base.context: %w", err)
		}
		total += n
		if total > int64(maxDecompressedZip) {
			return fmt.Errorf("base.context: decompressed size exceeds the %d-byte cap", maxDecompressedZip)
		}
	}
	if !sawDockerfile {
		return fmt.Errorf("base.context: tar has no root Dockerfile")
	}
	return nil
}

// hasDotDotComponent reports a ".." path component in a slash-separated name.
// Combined with the absolute-path check it blocks escape on the POSIX build hosts.
func hasDotDotComponent(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}
