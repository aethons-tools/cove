package configrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// GitHub opens and finds pull requests on the config repo at URL through
// the GitHub REST API (api.github.com, or <host>/api/v3 for GitHub
// Enterprise), as Token's bearer.
type GitHub struct {
	URL     string
	Token   func() (string, error)
	Client  *http.Client // nil = http.DefaultClient
	APIBase string       // "" = derived from URL (tests set it)
}

// PullRequest is an open pull request: its number and its page.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
}

// apiBase is repoURL's API base, owner and repo.
func apiBase(repoURL string) (base, owner, repo string, err error) {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", "", "", errors.New("config repo: not a GitHub repo URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", errors.New("config repo: want https://<host>/<owner>/<repo>")
	}
	owner, repo = parts[0], strings.TrimSuffix(parts[1], ".git")
	if strings.EqualFold(u.Hostname(), "github.com") {
		return "https://api.github.com", owner, repo, nil
	}
	return u.Scheme + "://" + u.Host + "/api/v3", owner, repo, nil
}

// do sends one API request and decodes a 2xx answer into out; any other
// status is an error carrying it and GitHub's message, never the token.
func (g GitHub) do(ctx context.Context, method, path string, body, out any) (int, error) {
	base, _, _, err := apiBase(g.URL)
	if err != nil {
		return 0, err
	}
	if g.APIBase != "" {
		base = g.APIBase
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return 0, err
	}
	tok, err := g.Token()
	if err != nil {
		return 0, errors.New("config repo: the credential could not be resolved")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := g.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, fmt.Errorf("config repo: GitHub: %s", strings.ReplaceAll(err.Error(), tok, "REDACTED"))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
			Errors  []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(data, &e)
		msg := e.Message
		for _, x := range e.Errors {
			if x.Message != "" {
				msg += ": " + x.Message
			}
		}
		return resp.StatusCode, fmt.Errorf("config repo: GitHub answered %d: %s", resp.StatusCode, strings.ReplaceAll(msg, tok, "REDACTED"))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("config repo: GitHub's answer: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// OpenPR opens a pull request from head into base; one already open for the
// pair is returned instead.
func (g GitHub) OpenPR(ctx context.Context, head, base, title, body string) (PullRequest, error) {
	_, owner, repo, err := apiBase(g.URL)
	if err != nil {
		return PullRequest{}, err
	}
	var pr PullRequest
	status, err := g.do(ctx, "POST", "/repos/"+owner+"/"+repo+"/pulls",
		map[string]string{"head": head, "base": base, "title": title, "body": body}, &pr)
	if status == http.StatusUnprocessableEntity && err != nil && strings.Contains(err.Error(), "already exists") {
		if open, ok, ferr := g.FindPR(ctx, head, base); ferr == nil && ok {
			return open, nil
		}
	}
	return pr, err
}

// FindPR is the open pull request from head into base, if any.
func (g GitHub) FindPR(ctx context.Context, head, base string) (PullRequest, bool, error) {
	_, owner, repo, err := apiBase(g.URL)
	if err != nil {
		return PullRequest{}, false, err
	}
	q := url.Values{"head": {owner + ":" + head}, "base": {base}, "state": {"open"}}
	var prs []PullRequest
	if _, err := g.do(ctx, "GET", "/repos/"+owner+"/"+repo+"/pulls?"+q.Encode(), nil, &prs); err != nil {
		return PullRequest{}, false, err
	}
	if len(prs) == 0 {
		return PullRequest{}, false, nil
	}
	return prs[0], true, nil
}
