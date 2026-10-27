package colima

import "strings"

// copyVolumeScript copies /from into /to with cp -a (ownership, modes,
// symlinks and times kept), then verifies the copy: every path's owner and
// mode and every regular file's content must list the same on both sides,
// else it exits non-zero. Fixed text: only the volume names vary, and they
// are mount sources, never part of the script.
const copyVolumeScript = `set -e
cp -a /from/. /to/
list() { cd "$1" && { find . -exec stat -c '%n %u:%g %a' {} + ; find . -type f -exec md5sum {} + ; } | LC_ALL=C sort; }
list /from > /tmp/from.list
list /to > /tmp/to.list
cmp -s /tmp/from.list /tmp/to.list || { echo "copy verification failed" >&2; exit 1; }`

// VolumeExists reports whether the named volume exists (`docker volume ls`
// filters by substring, so the name is matched exactly here).
func (c *Colima) VolumeExists(name string) (bool, error) {
	if err := c.preflight(); err != nil {
		return false, err
	}
	out, err := c.r.Output("docker", c.dargs("volume", "ls", "--quiet", "--filter", "name="+name)...)
	if err != nil {
		return false, err
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == name {
			return true, nil
		}
	}
	return false, nil
}

// CopyVolume copies every file of volume from into volume to (both exist)
// with a throwaway root container of image — no network, from mounted
// read-only, never pulled (--pull never: image must already be on the
// host) — and verifies the copy (copyVolumeScript). Only names reach argv.
func (c *Colima) CopyVolume(image, from, to string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	return c.r.Run("docker", c.dargs("run", "--rm", "--pull", "never", "--network", "none", "--user", "0:0", "--entrypoint", "/bin/sh",
		"-v", from+":/from:ro", "-v", to+":/to", image, "-c", copyVolumeScript)...)
}

// ListContainers lists every container (running or not) matching one
// `docker ps` filter ("label=<key>", "name=<substring>"), name → the image it
// was run from.
func (c *Colima) ListContainers(filter string) (map[string]string, error) {
	if err := c.preflight(); err != nil {
		return nil, err
	}
	out, err := c.r.Output("docker", c.dargs("ps", "--all", "--no-trunc", "--filter", filter, "--format", "{{.Names}}\t{{.Image}}")...)
	if err != nil {
		return nil, err
	}
	got := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, image, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if name != "" {
			got[name] = image
		}
	}
	return got, nil
}
