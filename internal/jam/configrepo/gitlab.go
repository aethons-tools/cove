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

// PRs opens and finds the review requests of the config repo's host: pull
// requests on GitHub, merge requests on GitLab.
type PRs interface {
	OpenPR(ctx context.Context, head, base, title, body string) (PullRequest, error)
	FindPR(ctx context.Context, head, base string) (PullRequest, bool, error)
}

// GitLab opens and finds merge requests on the config repo at URL through
// the GitLab REST API (<host>/api/v4: gitlab.com or self-hosted), as
// Token's bearer.
type GitLab struct {
	URL     string
	Token   func() (string, error)
	Client  *http.Client // nil = http.DefaultClient
	APIBase string       // "" = derived from URL (tests set it)
}

// gitlabProject is repoURL's API base and project path (group/…/repo).
func gitlabProject(repoURL string) (base, project string, err error) {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", "", errors.New("config repo: not a GitLab repo URL")
	}
	p := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if strings.Count(p, "/") < 1 || strings.Contains(p, "//") {
		return "", "", errors.New("config repo: want https://<host>/<group>/<repo>")
	}
	return u.Scheme + "://" + u.Host + "/api/v4", p, nil
}

// glMR is a merge request as the API answers it.
type glMR struct {
	IID    int    `json:"iid"`
	WebURL string `json:"web_url"`
}

func (m glMR) pr() PullRequest { return PullRequest{Number: m.IID, URL: m.WebURL} }

// do sends one API request (path after /projects/<id>) and decodes a 2xx
// answer into out; another status is an error carrying it and GitLab's
// message, never the token.
func (g GitLab) do(ctx context.Context, method, path string, body, out any) (int, error) {
	base, project, err := gitlabProject(g.URL)
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
	req, err := http.NewRequestWithContext(ctx, method, base+"/projects/"+url.PathEscape(project)+path, rd)
	if err != nil {
		return 0, err
	}
	tok, err := g.Token()
	if err != nil {
		return 0, errors.New("config repo: the credential could not be resolved")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := g.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, &remoteError{"config repo: GitLab: " + strings.ReplaceAll(err.Error(), tok, "REDACTED")}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		// GitLab's message is a string, or a list of them.
		var e struct {
			Message json.RawMessage `json:"message"`
			Error   string          `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		msg := e.Error
		var one string
		var many []string
		switch {
		case json.Unmarshal(e.Message, &one) == nil:
			msg = one
		case json.Unmarshal(e.Message, &many) == nil:
			msg = strings.Join(many, "; ")
		}
		return resp.StatusCode, &remoteError{fmt.Sprintf("config repo: GitLab answered %d: %s", resp.StatusCode, strings.ReplaceAll(msg, tok, "REDACTED"))}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("config repo: GitLab's answer: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// OpenPR opens a merge request from head into base; one already open for
// the pair is returned instead.
func (g GitLab) OpenPR(ctx context.Context, head, base, title, body string) (PullRequest, error) {
	var mr glMR
	status, err := g.do(ctx, "POST", "/merge_requests",
		map[string]string{"source_branch": head, "target_branch": base, "title": title, "description": body}, &mr)
	if status == http.StatusConflict {
		if open, ok, ferr := g.FindPR(ctx, head, base); ferr == nil && ok {
			return open, nil
		}
	}
	return mr.pr(), err
}

// FindPR is the open merge request from head into base, if any.
func (g GitLab) FindPR(ctx context.Context, head, base string) (PullRequest, bool, error) {
	q := url.Values{"source_branch": {head}, "target_branch": {base}, "state": {"opened"}}
	var mrs []glMR
	if _, err := g.do(ctx, "GET", "/merge_requests?"+q.Encode(), nil, &mrs); err != nil {
		return PullRequest{}, false, err
	}
	if len(mrs) == 0 {
		return PullRequest{}, false, nil
	}
	return mrs[0].pr(), true, nil
}
