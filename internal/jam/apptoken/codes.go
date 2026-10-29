package apptoken

import (
	"errors"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// CodeTTL is how long a consent's code may be exchanged.
const CodeTTL = 60 * time.Second

// maxCodes caps the outstanding codes.
const maxCodes = 1024

var errTooManyCodes = errors.New("too many outstanding app-token codes")

// grant is what an approved consent binds its code to.
type grant struct {
	Challenge  string
	Redirect   string
	UserID     ident.ID
	OperatorID string
	Label      string
	expires    time.Time
}

// codes holds the single-use consent codes, keyed by jam.HashToken(code):
// the raw code is never kept.
type codes struct {
	mu  sync.Mutex
	m   map[string]grant
	now func() time.Time
}

func newCodes(now func() time.Time) *codes {
	return &codes{m: map[string]grant{}, now: now}
}

// issue mints a code for g, valid for CodeTTL.
func (c *codes) issue(g grant) (string, error) {
	raw, err := jam.MintToken()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= maxCodes {
		c.pruneLocked()
		if len(c.m) >= maxCodes {
			return "", errTooManyCodes
		}
	}
	g.expires = c.now().Add(CodeTTL)
	c.m[jam.HashToken(raw)] = g
	return raw, nil
}

// redeem burns raw and returns its grant if it was unexpired.
func (c *codes) redeem(raw string) (grant, bool) {
	if raw == "" {
		return grant{}, false
	}
	k := jam.HashToken(raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.m[k]
	delete(c.m, k)
	if !ok || !c.now().Before(g.expires) {
		return grant{}, false
	}
	return g, true
}

// prune drops expired codes.
func (c *codes) prune() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked()
}

func (c *codes) pruneLocked() {
	now := c.now()
	for k, g := range c.m {
		if !now.Before(g.expires) {
			delete(c.m, k)
		}
	}
}
