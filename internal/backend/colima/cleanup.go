package colima

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/runner"
)

var _ jam.HostCleaner = (*Colima)(nil)

// anonymousVolumeLabel marks the volumes docker (23+) creates anonymously.
// Pruning only volumes carrying it keeps named volumes (studio state) safe on
// every daemon: before 23, a bare prune removed every unused volume, but no
// volume carried the label.
const anonymousVolumeLabel = "com.docker.volume.anonymous"

// PruneDanglingImages removes the VM's unnamed images: dangling ones whose
// repository is <none>, each with `docker image rm` (no -f, so docker keeps
// one a container or a child image uses; that refusal is not an error). It
// is not `docker image prune`: that also removes images pulled by digest
// only (repository set, no tag) — the blessed jam-studio-base images the
// provenance gate and every kit build use — which then re-pull on each build.
func (c *Colima) PruneDanglingImages(ctx context.Context) error {
	out, err := c.output(ctx, "docker", c.dargs("image", "ls", "--filter", "dangling=true", "--no-trunc", "--format", "{{json .}}")...)
	if err != nil {
		return fmt.Errorf("docker image ls: %w", err)
	}
	ids, err := unnamedImages(out)
	if err != nil {
		return err
	}
	for _, id := range ids {
		_, _ = c.output(ctx, "docker", c.dargs("image", "rm", id)...)
	}
	return nil
}

// unnamedImages reads `docker image ls --format '{{json .}}'` and returns the
// IDs, once each, of the images whose repository is <none>.
func unnamedImages(out string) ([]string, error) {
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var row struct{ ID, Repository string }
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("docker image ls: %w", err)
		}
		if row.Repository == "<none>" && row.ID != "" && !slices.Contains(ids, row.ID) {
			ids = append(ids, row.ID)
		}
	}
	return ids, nil
}

// PruneVolumes removes unused anonymous volumes, VM-wide; never --all.
func (c *Colima) PruneVolumes(ctx context.Context) error {
	return c.docker(ctx, "volume", "prune", "-f", "--filter", "label="+anonymousVolumeLabel)
}

// PruneBuildCache runs `docker builder prune -f`: the unused build cache.
func (c *Colima) PruneBuildCache(ctx context.Context) error {
	return c.docker(ctx, "builder", "prune", "-f")
}

// RemoveImage runs `docker image rm <id>` without -f, so docker refuses an
// image a container uses or that several repositories name.
func (c *Colima) RemoveImage(ctx context.Context, id string) error {
	return c.docker(ctx, "image", "rm", id)
}

func (c *Colima) docker(ctx context.Context, args ...string) error {
	if _, err := c.output(ctx, "docker", c.dargs(args...)...); err != nil {
		return fmt.Errorf("docker %s: %w", strings.Join(args[:2], " "), err)
	}
	return nil
}

// dockerCreatedLayout is `docker image ls`'s CreatedAt.
const dockerCreatedLayout = "2006-01-02 15:04:05 -0700 MST"

// KitImages lists the images tagged only in jam.KitImageRepos (an image
// another repository also names is left out), in docker's order, each
// marked InUse when any container, running or stopped, uses it.
func (c *Colima) KitImages(ctx context.Context) ([]jam.KitImage, error) {
	out, err := c.output(ctx, "docker", c.dargs("image", "ls", "--no-trunc", "--format", "{{json .}}")...)
	if err != nil {
		return nil, fmt.Errorf("docker image ls: %w", err)
	}
	var imgs []jam.KitImage
	byID := map[string]int{}
	foreign := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row struct{ ID, Repository, Tag, CreatedAt, Size string }
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("docker image ls: bad line %q: %w", line, err)
		}
		if !slices.Contains(jam.KitImageRepos, row.Repository) {
			foreign[row.ID] = true
			continue
		}
		tag := row.Repository + ":" + row.Tag
		if i, ok := byID[row.ID]; ok {
			imgs[i].Tags = append(imgs[i].Tags, tag)
			continue
		}
		created, err := time.Parse(dockerCreatedLayout, row.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("docker image ls: %s: bad CreatedAt %q", tag, row.CreatedAt)
		}
		size, err := parseDockerSize(row.Size)
		if err != nil {
			return nil, fmt.Errorf("docker image ls: %s: %w", tag, err)
		}
		byID[row.ID] = len(imgs)
		imgs = append(imgs, jam.KitImage{ID: row.ID, Tags: []string{tag}, Created: created, Size: size})
	}
	imgs = slices.DeleteFunc(imgs, func(k jam.KitImage) bool { return foreign[k.ID] })
	used, err := c.containerImages(ctx)
	if err != nil {
		return nil, err
	}
	for i := range imgs {
		imgs[i].InUse = used[imgs[i].ID]
	}
	return imgs, nil
}

// containerImages is the set of image ids every container (running or
// stopped) uses.
func (c *Colima) containerImages(ctx context.Context) (map[string]bool, error) {
	out, err := c.output(ctx, "docker", c.dargs("ps", "-a", "-q", "--no-trunc")...)
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	ids := strings.Fields(out)
	used := map[string]bool{}
	if len(ids) == 0 {
		return used, nil
	}
	// A container gone since the listing (a --rm studio exiting) fails the
	// inspect for that one only: docker still prints the others.
	var stdout, stderr bytes.Buffer
	err = runner.Scoped(ctx, c.r, nil).RunIO(strings.NewReader(""), &stdout, &stderr, "docker",
		c.dargs(append([]string{"container", "inspect", "--format", "{{.Image}}"}, ids...)...)...)
	if err != nil && !onlyNoSuchContainer(stderr.String()) {
		if line := lastLine(stderr.String()); line != "" {
			err = fmt.Errorf("%w: %s", err, line)
		}
		return nil, fmt.Errorf("docker container inspect: %w", err)
	}
	for _, id := range strings.Fields(stdout.String()) {
		used[id] = true
	}
	return used, nil
}

// onlyNoSuchContainer reports whether docker's stderr holds only "No such
// container" errors (at least one).
func onlyNoSuchContainer(stderr string) bool {
	n := 0
	for _, line := range strings.Split(stderr, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(line, "No such container") {
			return false
		}
		n++
	}
	return n > 0
}
