package colima

import (
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
)

// copyVolumeCopy copies /from into /to with cp -a (ownership, modes,
// symlinks, hard links within the copy and times kept).
const copyVolumeCopy = `set -e
cp -a /from/. /to/
`

// copyVolumeVerify checks the copy: every path's name, owner and mode, and
// every regular file's content (md5sum), must list the same on both sides,
// else it exits non-zero. It does NOT check hard-link structure, xattrs,
// ACLs, timestamps or symlink targets (cp -a carries them; they are not
// re-checked). No pipelines: each step's failure stops the script under
// set -e on its own (dash, the Debian /bin/sh, has no pipefail), so a failed
// listing on both sides cannot pass as two equal empty lists.
const copyVolumeVerify = `set -e
for side in from to; do
cd /$side
find . -exec stat -c '%n %u:%g %a' {} + > /tmp/$side.stat
find . -type f -exec md5sum {} + > /tmp/$side.sum
LC_ALL=C sort /tmp/$side.stat /tmp/$side.sum > /tmp/$side.list
done
cmp -s /tmp/from.list /tmp/to.list || { echo "copy verification failed" >&2; exit 1; }
`

// copyVolumeScript is the copy, then its verification. Fixed text: only the
// volume names vary, and they are mount sources, never part of the script.
const copyVolumeScript = copyVolumeCopy + copyVolumeVerify

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
// host) — and verifies the copy (copyVolumeVerify: names, owners, modes and
// file contents; not hard links, xattrs, ACLs, times or symlink targets).
// Only names reach argv.
func (c *Colima) CopyVolume(image, from, to string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	return c.r.Run("docker", c.dargs("run", "--rm", "--pull", "never", "--network", "none", "--user", "0:0", "--entrypoint", "/bin/sh",
		"-v", from+":/from:ro", "-v", to+":/to", image, "-c", copyVolumeScript)...)
}

// FindContainers lists every container (running or not) matching one
// `docker ps` filter ("label=<key>", "name=<substring>"): name → the image it
// was run from and the volumes it mounts.
func (c *Colima) FindContainers(filter string) (map[string]backend.ContainerInfo, error) {
	if err := c.preflight(); err != nil {
		return nil, err
	}
	out, err := c.r.Output("docker", c.dargs("ps", "--all", "--no-trunc", "--filter", filter, "--format", "{{.Names}}\t{{.Image}}\t{{.Mounts}}")...)
	if err != nil {
		return nil, err
	}
	got := map[string]backend.ContainerInfo{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if f[0] == "" {
			continue
		}
		info := backend.ContainerInfo{}
		if len(f) > 1 {
			info.Image = f[1]
		}
		if len(f) > 2 && f[2] != "" {
			info.Mounts = strings.Split(f[2], ",")
		}
		got[f[0]] = info
	}
	return got, nil
}
