// Package github is at-task's real CodeHost: a tiny GitHub PR client over net/http.
// Live calls are exercised by the integration-tagged test.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const api = "https://api.github.com"

// Client opens pull requests on GitHub. It satisfies worker.CodeHost.
type Client struct {
	http  *http.Client
	token string
}

func New(token string, httpc *http.Client) *Client {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &Client{http: httpc, token: token}
}

// OpenPR creates a PR, or returns the URL of an existing open PR for the same head.
func (c *Client) OpenPR(ctx context.Context, repo, base, head, title, body string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"title": title, "head": head, "base": base, "body": body})
	code, raw, err := c.do(ctx, http.MethodPost, api+"/repos/"+repo+"/pulls", payload)
	if err != nil {
		return "", err
	}
	if code == http.StatusCreated {
		var out struct {
			HTMLURL string `json:"html_url"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", err
		}
		return out.HTMLURL, nil
	}
	if code == http.StatusUnprocessableEntity && strings.Contains(string(raw), "already exists") {
		return c.existing(ctx, repo, base, head)
	}
	return "", fmt.Errorf("github: create PR: http %d: %s", code, strings.TrimSpace(string(raw)))
}

func (c *Client) existing(ctx context.Context, repo, base, head string) (string, error) {
	owner := strings.SplitN(repo, "/", 2)[0]
	q := url.Values{"head": {owner + ":" + head}, "base": {base}, "state": {"open"}}
	code, raw, err := c.do(ctx, http.MethodGet, api+"/repos/"+repo+"/pulls?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("github: list PRs: http %d", code)
	}
	var prs []struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(raw, &prs); err != nil {
		return "", err
	}
	if len(prs) == 0 {
		return "", fmt.Errorf("github: PR reported existing but none found for %s", head)
	}
	return prs[0].HTMLURL, nil
}

func (c *Client) do(ctx context.Context, method, u string, payload []byte) (int, []byte, error) {
	var bodyReader *bytes.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(resp.Body)
	return resp.StatusCode, raw.Bytes(), nil
}
