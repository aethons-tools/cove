package switchboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// RESTClient is the real Discord REST adapter. The bot token is sent only as the
// Authorization header — never logged, never on argv.
type RESTClient struct {
	token    string
	channels []string
	baseURL  string
	http     *http.Client
}

// RESTOption configures a RESTClient.
type RESTOption func(*RESTClient)

// WithBaseURL overrides the Discord API base (for tests). Default: v10 API.
func WithBaseURL(u string) RESTOption { return func(c *RESTClient) { c.baseURL = u } }

// WithHTTPClient overrides the HTTP client (for tests).
func WithHTTPClient(h *http.Client) RESTOption { return func(c *RESTClient) { c.http = h } }

// NewRESTClient builds a Discord REST adapter for the given watched channels.
func NewRESTClient(token string, channels []string, opts ...RESTOption) *RESTClient {
	c := &RESTClient{token: token, channels: channels, baseURL: "https://discord.com/api/v10", http: http.DefaultClient}
	for _, o := range opts {
		o(c)
	}
	return c
}

type discordMessage struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Author  struct {
		Username   string `json:"username"`
		GlobalName string `json:"global_name"`
	} `json:"author"`
}

func (c *RESTClient) Poll(ctx context.Context, cursors map[string]string) ([]Message, map[string]string, error) {
	nc := map[string]string{}
	for k, v := range cursors {
		nc[k] = v
	}
	var out []Message
	for _, ch := range c.channels {
		u := fmt.Sprintf("%s/channels/%s/messages?limit=100", c.baseURL, ch)
		if after := cursors[ch]; after != "" {
			u += "&after=" + url.QueryEscape(after)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bot "+c.token)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("switchboard: poll %s: %w", ch, err)
		}
		var page []discordMessage
		derr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, nil, fmt.Errorf("switchboard: poll %s: status %d", ch, resp.StatusCode)
		}
		if derr != nil {
			return nil, nil, fmt.Errorf("switchboard: poll %s decode: %w", ch, derr)
		}
		// Discord returns newest-first; walk in reverse for chronological order.
		for i := len(page) - 1; i >= 0; i-- {
			m := page[i]
			author := m.Author.GlobalName
			if author == "" {
				author = m.Author.Username
			}
			out = append(out, Message{ID: m.ID, Channel: ch, Author: author, Content: m.Content})
			nc[ch] = m.ID
		}
	}
	return out, nc, nil
}

// Seed returns per-channel cursors at the newest existing message id, without
// returning the messages — so Run starts from an empty inbox. A channel with no
// messages gets no cursor entry.
func (c *RESTClient) Seed(ctx context.Context) (map[string]string, error) {
	cursors := map[string]string{}
	for _, ch := range c.channels {
		u := fmt.Sprintf("%s/channels/%s/messages?limit=1", c.baseURL, ch)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bot "+c.token)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("switchboard: seed %s: %w", ch, err)
		}
		var page []discordMessage
		derr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("switchboard: seed %s: status %d", ch, resp.StatusCode)
		}
		if derr != nil {
			return nil, fmt.Errorf("switchboard: seed %s decode: %w", ch, derr)
		}
		if len(page) > 0 {
			cursors[ch] = page[0].ID // newest-first, so [0] is the latest
		}
	}
	return cursors, nil
}

func (c *RESTClient) Post(ctx context.Context, channel, content string) error {
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/channels/%s/messages", c.baseURL, channel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("switchboard: post %s: %w", channel, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("switchboard: post %s: status %d", channel, resp.StatusCode)
	}
	return nil
}
