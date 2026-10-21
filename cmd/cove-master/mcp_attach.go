package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Attachments (docs/usage/jam/attachments.md): send's `attach` uploads files
// from the studio through Jam (POST /squawks/uploads) and sends their refs;
// fetch downloads one the session can see (GET /squawks/blobs/{ref}). Bytes
// leave and enter the studio only through Jam.

// maxAttach mirrors Jam's per-message limit.
const maxAttach = 10

// transfers carries uploads and downloads: no overall timeout (a large file
// on a slow link), bounded by the tool call's context instead.
var transfers = &http.Client{}

var blobRef = regexp.MustCompile(`^blb_[0-9a-z]{26}$`)

// uploadAll uploads each path and returns the refs, in order.
func (c *messagingClient) uploadAll(ctx context.Context, paths []string) ([]string, error) {
	if len(paths) > maxAttach {
		return nil, fmt.Errorf("at most %d attachments per message", maxAttach)
	}
	refs := make([]string, 0, len(paths))
	for _, p := range paths {
		ref, err := c.upload(ctx, p)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (c *messagingClient) upload(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("attach %s: %w", path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", fmt.Errorf("attach %s: not a regular file", path)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/squawks/uploads", f)
	if err != nil {
		return "", err
	}
	req.ContentLength = st.Size()
	req.Header.Set("Authorization", "Bearer "+c.token)
	ct := mime.TypeByExtension(filepath.Ext(path))
	if ct == "" {
		ct = "application/octet-stream"
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Filename", url.PathEscape(filepath.Base(path)))
	resp, err := transfers.Do(req)
	if err != nil {
		return "", fmt.Errorf("attach %s: upload failed", path)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxJamResponseBytes))
	switch resp.StatusCode {
	case http.StatusCreated:
	case http.StatusRequestEntityTooLarge, http.StatusServiceUnavailable:
		return "", fmt.Errorf("attach %s: %s", path, strings.TrimSpace(string(body)))
	default:
		return "", fmt.Errorf("attach %s: unexpected status %d", path, resp.StatusCode)
	}
	var out struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Ref == "" {
		return "", fmt.Errorf("attach %s: decoding upload response", path)
	}
	return out.Ref, nil
}

type fetchIn struct {
	Ref  string `json:"ref" jsonschema:"the attachment's ref (blb_…), from a read entry's parts"`
	Path string `json:"path,omitempty" jsonschema:"optional file path to write; default ~/attachments/<ref>-<name>. An existing file is never overwritten"`
}

type fetchOut struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Type string `json:"type,omitempty"`
}

// fetch downloads ref into the studio and returns where it wrote it.
func (c *messagingClient) fetch(ctx context.Context, in fetchIn) (fetchOut, error) {
	if !blobRef.MatchString(in.Ref) {
		return fetchOut{}, fmt.Errorf("ref must be an attachment id (blb_…)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/squawks/blobs/"+in.Ref, nil)
	if err != nil {
		return fetchOut{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := transfers.Do(req)
	if err != nil {
		return fetchOut{}, fmt.Errorf("fetch %s: request failed", in.Ref)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fetchOut{}, fmt.Errorf("fetch %s: status %d", in.Ref, resp.StatusCode)
	}
	path := in.Path
	if path == "" {
		name := "file"
		if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
			name = filepath.Base(filepath.Clean("/" + params["filename"]))
		}
		dir := filepath.Join(c.home, "attachments")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fetchOut{}, fmt.Errorf("fetch: %w", err)
		}
		path = filepath.Join(dir, in.Ref+"-"+name)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fetchOut{}, fmt.Errorf("fetch: %s exists; pick another path", path)
		}
		return fetchOut{}, fmt.Errorf("fetch: %w", err)
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return fetchOut{}, fmt.Errorf("fetch %s: writing: %w", in.Ref, err)
	}
	return fetchOut{Path: path, Size: n, Type: resp.Header.Get("Content-Type")}, nil
}

func addAttachTools(s *mcp.Server, client *messagingClient, cfgErr error) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "fetch",
		Description: "Download an attachment (a read entry's part with a ref, blb_…) into your studio — by default ~/attachments/<ref>-<name> — and return the path, so you can open it (an image, a log). It never overwrites a file.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in fetchIn) (*mcp.CallToolResult, fetchOut, error) {
		if cfgErr != nil {
			return nil, fetchOut{}, cfgErr
		}
		out, err := client.fetch(ctx, in)
		return nil, out, err
	})
}
