// Package gitlab is at-task's GitLab CodeHost: a tiny Merge Request client over
// net/http, mirroring internal/dispatch/github. Live calls are exercised by the
// integration-tagged test (if added). It satisfies worker.CodeHost.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Client opens merge requests on a GitLab host (gitlab.com or self-hosted).
type Client struct {
	http  *http.Client
	base  string // https://<host>/api/v4
	token string
}

// New builds a client for host (a bare hostname, e.g. gitlab.com). token is a
// PAT / Project Access Token with api + write_repository.
func New(token, host string, httpc *http.Client) *Client {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &Client{http: httpc, base: "https://" + host + "/api/v4", token: token}
}

// OpenPR creates a Merge Request, or returns the URL of an existing open MR for
// the same source branch. repo is the project path (group/.../name).
func (c *Client) OpenPR(ctx context.Context, repo, base, head, title, body string) (string, error) {
	proj := url.QueryEscape(repo) // group/sub/name -> group%2Fsub%2Fname
	payload, _ := json.Marshal(map[string]string{
		"source_branch": head, "target_branch": base, "title": title, "description": body,
	})
	code, raw, err := c.do(ctx, http.MethodPost, c.base+"/projects/"+proj+"/merge_requests", payload)
	if err != nil {
		return "", err
	}
	if code == http.StatusCreated {
		return webURL(raw)
	}
	if code == http.StatusConflict { // an open MR already exists for this source branch
		return c.existing(ctx, proj, base, head)
	}
	return "", fmt.Errorf("gitlab: create MR: http %d: %s", code, strings.TrimSpace(string(raw)))
}

func (c *Client) existing(ctx context.Context, proj, base, head string) (string, error) {
	q := url.Values{"source_branch": {head}, "target_branch": {base}, "state": {"opened"}}
	code, raw, err := c.do(ctx, http.MethodGet, c.base+"/projects/"+proj+"/merge_requests?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("gitlab: list MRs: http %d", code)
	}
	var mrs []struct {
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(raw, &mrs); err != nil {
		return "", err
	}
	if len(mrs) == 0 {
		return "", fmt.Errorf("gitlab: MR reported existing but none found for %s", head)
	}
	return mrs[0].WebURL, nil
}

func webURL(raw []byte) (string, error) {
	var out struct {
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.WebURL, nil
}

func (c *Client) do(ctx context.Context, method, u string, payload []byte) (int, []byte, error) {
	var r *bytes.Reader
	if payload != nil {
		r = bytes.NewReader(payload)
	} else {
		r = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes(), nil
}
