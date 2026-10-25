package adminclient

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// Kit drafts (docs/usage/jam/kits-drafts.md). A write is sent against an
// etag (the draft's, from a status or the previous write): a stale one is a
// *StatusError with Code 412.

// draftClient is a copy of the client's http.Client that refuses redirects
// (a 3xx is answered as is, never followed with the bearer or a body) and,
// when timeout > 0, runs under that timeout.
func (c *Client) draftClient(timeout time.Duration) *http.Client {
	hc := *c.httpc // keeps the Transport
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if timeout > 0 {
		hc.Timeout = timeout
	}
	return &hc
}

func draftPath(name string) string { return "/admin/kits/" + url.PathEscape(name) + "/draft" }

// draftFilePath escapes each segment of p, so a name with spaces or unicode
// travels intact and a '/' stays a separator.
func draftFilePath(name, p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return draftPath(name) + "/files/" + strings.Join(segs, "/")
}

func ifMatch(etag string) http.Header { return http.Header{"If-Match": {etag}} }

// OpenKitDraft opens kit name's draft from version from (0 = current).
func (c *Client) OpenKitDraft(name string, from int) (jam.KitDraftStatus, error) {
	body, err := json.Marshal(jam.KitDraftOpenBody{From: from})
	if err != nil {
		return jam.KitDraftStatus{}, err
	}
	var s jam.KitDraftStatus
	_, err = c.sendHeaders(c.draftClient(0), "POST", draftPath(name), "application/json", body, nil, &s)
	return s, err
}

// KitDraftStatus is kit name's draft status (ErrNotFound without a draft).
func (c *Client) KitDraftStatus(name string) (jam.KitDraftStatus, error) {
	var s jam.KitDraftStatus
	_, err := c.sendHeaders(c.draftClient(0), "GET", draftPath(name), "", nil, nil, &s)
	return s, err
}

// DiscardKitDraft drops kit name's draft.
func (c *Client) DiscardKitDraft(name, etag string) error {
	_, err := c.sendHeaders(c.draftClient(0), "DELETE", draftPath(name), "", nil, ifMatch(etag), nil)
	return err
}

// KitDraftFile is file p of kit name's draft: its bytes and its mode
// ("0644" or "0755", from X-Kit-File-Mode).
func (c *Client) KitDraftFile(name, p string) ([]byte, string, error) {
	var b []byte
	h, err := c.sendHeaders(c.draftClient(0), "GET", draftFilePath(name, p), "", nil, nil, &b)
	if err != nil {
		return nil, "", err
	}
	return b, h.Get("X-Kit-File-Mode"), nil
}

// PutKitDraftFile writes file p (mode "" keeps the file's, else "0644" or "0755").
func (c *Client) PutKitDraftFile(name, p string, data []byte, mode, etag string) (jam.KitDraftSave, error) {
	path := draftFilePath(name, p)
	if mode != "" {
		path += "?mode=" + url.QueryEscape(mode)
	}
	var s jam.KitDraftSave
	_, err := c.sendHeaders(c.draftClient(0), "PUT", path, "application/octet-stream", data, ifMatch(etag), &s)
	return s, err
}

// RemoveKitDraftFile removes file p from kit name's draft.
func (c *Client) RemoveKitDraftFile(name, p, etag string) (jam.KitDraftSave, error) {
	var s jam.KitDraftSave
	_, err := c.sendHeaders(c.draftClient(0), "DELETE", draftFilePath(name, p), "", nil, ifMatch(etag), &s)
	return s, err
}

// ReplaceKitDraft replaces kit name's draft tree with an uncompressed tar
// (kittree.WriteTar), under the raw-upload timeout.
func (c *Client) ReplaceKitDraft(name string, tarBody []byte, etag string) (jam.KitDraftSave, error) {
	var s jam.KitDraftSave
	_, err := c.sendHeaders(c.draftClient(configGitTimeout), "PUT", draftPath(name), "application/x-tar", tarBody, ifMatch(etag), &s)
	return s, err
}

// PublishKitDraft publishes kit name's draft as its next version.
func (c *Client) PublishKitDraft(name string, pin bool, etag string) (jam.KitDraftPublish, error) {
	body, err := json.Marshal(jam.KitDraftPublishBody{Pin: pin})
	if err != nil {
		return jam.KitDraftPublish{}, err
	}
	var res jam.KitDraftPublish
	_, err = c.sendHeaders(c.draftClient(0), "POST", draftPath(name)+"/publish", "application/json", body, ifMatch(etag), &res)
	return res, err
}
