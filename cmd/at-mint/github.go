package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type githubInput struct {
	AppID     string
	InstallID string
	KeyPEM    []byte
	Repo      string // "owner/name"
}

// mintGitHub mints a short-lived GitHub App installation token scoped to in.Repo
// with contents+pull_requests write. It signs a ~9-minute App JWT (RS256) and
// exchanges it at the installation access-tokens endpoint.
func mintGitHub(ctx context.Context, httpc *http.Client, in githubInput, now time.Time) (string, error) {
	if in.AppID == "" || in.InstallID == "" {
		return "", fmt.Errorf("app-id and install-id are required")
	}
	if in.Repo == "" {
		return "", fmt.Errorf("COVE_RUN_REPO is not set (the repo to scope the token to)")
	}
	key, err := parseRSAPrivateKey(in.KeyPEM)
	if err != nil {
		return "", err
	}
	header := []byte(`{"alg":"RS256","typ":"JWT"}`)
	payload, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(540 * time.Second).Unix(),
		"iss": in.AppID,
	})
	if err != nil {
		return "", err
	}
	jwt, err := signRS256(header, payload, key)
	if err != nil {
		return "", err
	}
	repoName := in.Repo
	if i := strings.IndexByte(repoName, '/'); i >= 0 {
		repoName = repoName[i+1:] // installation is org-scoped; request by name
	}
	body, err := json.Marshal(map[string]any{
		"repositories": []string{repoName},
		"permissions":  map[string]string{"contents": "write", "pull_requests": "write"},
	})
	if err != nil {
		return "", err
	}
	url := "https://api.github.com/app/installations/" + in.InstallID + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	res, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("github installation token: HTTP %d: %s", res.StatusCode, strings.TrimSpace(buf.String()))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		return "", fmt.Errorf("parse github response: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("github response contained no token")
	}
	return out.Token, nil
}
