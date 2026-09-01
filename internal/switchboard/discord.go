package switchboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// RESTClient is the real Discord REST adapter. The bot token is sent only as the
// Authorization header — never logged, never on argv.
type RESTClient struct {
	token    string
	channels []string
	baseURL  string
	http     *http.Client
	sleep    func(time.Duration)
}

// RESTOption configures a RESTClient.
type RESTOption func(*RESTClient)

// WithBaseURL overrides the Discord API base (for tests). Default: v10 API.
func WithBaseURL(u string) RESTOption { return func(c *RESTClient) { c.baseURL = u } }

// WithHTTPClient overrides the HTTP client (for tests).
func WithHTTPClient(h *http.Client) RESTOption { return func(c *RESTClient) { c.http = h } }

// WithSleep overrides the backoff sleep (for tests).
func WithSleep(s func(time.Duration)) RESTOption { return func(c *RESTClient) { c.sleep = s } }

// NewRESTClient builds a Discord REST adapter for the given watched channels.
func NewRESTClient(token string, channels []string, opts ...RESTOption) *RESTClient {
	c := &RESTClient{token: token, channels: channels, baseURL: "https://discord.com/api/v10", http: http.DefaultClient, sleep: time.Sleep}
	for _, o := range opts {
		o(c)
	}
	return c
}

// maxRetryAttempts caps the number of retry attempts for a 429/5xx response
// (in addition to the initial attempt) before giving up and returning the
// last response to the caller for normal status-code error handling.
const maxRetryAttempts = 4

// doWithRetry performs the request built by newReq (called fresh on every
// attempt so bodies are re-readable across retries — see Post) and retries on
// HTTP 429 or any 5xx response: it sleeps (honoring Retry-After in seconds for
// 429 when present, otherwise exponential backoff) and tries again, up to
// maxRetryAttempts. A non-2xx status that isn't 429/5xx (e.g. 403) is returned
// immediately without retrying. The token never leaves the Authorization
// header set by newReq — it is not part of the URL, logs, or errors, on the
// initial attempt or any retry.
func (c *RESTClient) doWithRetry(ctx context.Context, newReq func() (*http.Request, error)) (*http.Response, error) {
	var resp *http.Response
	for attempt := 0; attempt <= maxRetryAttempts; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}
		resp, err = c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return resp, nil
		}
		if attempt == maxRetryAttempts {
			return resp, nil
		}
		wait := time.Duration(1<<attempt) * time.Second
		if resp.StatusCode == http.StatusTooManyRequests {
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, perr := strconv.Atoi(ra); perr == nil {
					wait = time.Duration(secs) * time.Second
				}
			}
		}
		resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		c.sleep(wait)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	return resp, nil
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
	// Single page of up to 100 messages per channel per poll. Discord's `after`
	// is forward pagination (oldest-after-cursor), so a >100 backlog drains 100
	// per poll across successive polls — bounded catch-up latency, never message loss.
	for _, ch := range c.channels {
		u := fmt.Sprintf("%s/channels/%s/messages?limit=100", c.baseURL, ch)
		if after := cursors[ch]; after != "" {
			u += "&after=" + url.QueryEscape(after)
		}
		resp, err := c.doWithRetry(ctx, func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bot "+c.token)
			return req, nil
		})
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
		resp, err := c.doWithRetry(ctx, func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bot "+c.token)
			return req, nil
		})
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
	// Rebuild the request from the marshalled body bytes on every attempt so
	// the body is re-readable across retries.
	resp, err := c.doWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bot "+c.token)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return fmt.Errorf("switchboard: post %s: %w", channel, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("switchboard: post %s: status %d", channel, resp.StatusCode)
	}
	return nil
}
