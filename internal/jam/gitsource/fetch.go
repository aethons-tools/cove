package gitsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
)

// Fetch limits used when a Fetcher leaves them zero.
const (
	DefaultMaxBytes         = 50 << 20
	DefaultMaxCheckoutBytes = 64 << 20
	DefaultTimeout          = 60 * time.Second
)

// Fetcher checks kit sources out of git over HTTPS with go-git. The zero
// value is usable; it must not be copied after first use.
type Fetcher struct {
	Creds            Creds
	MaxBytes         int64         // response bytes per fetch; 0 = DefaultMaxBytes
	MaxCheckoutBytes int64         // work-tree bytes written per fetch; 0 = DefaultMaxCheckoutBytes
	Timeout          time.Duration // per fetch, transfer and checkout; 0 = DefaultTimeout
	TempDir          string        // "" = os.TempDir()

	mu    sync.Mutex
	locks map[string]chan struct{} // per repo URL: fetches of one repo are serialized
}

// testHookCheckout, when set, runs inside the checkout right after the temp
// dir exists (tests use it to panic there).
var testHookCheckout func(root string)

// Fetch checks src.Ref out shallowly into a fresh private temp dir and calls
// fn(root, commit); the dir is removed when fn returns, and also if the
// checkout fails or panics. fn's error is returned as is; every other error
// is redacted (no userinfo, query or token).
func (f *Fetcher) Fetch(ctx context.Context, src Source, fn func(root, commit string) error) error {
	var root, commit string
	defer func() { // registered before the dir exists, so a panic cannot leak it
		if root != "" {
			_ = os.RemoveAll(root)
		}
	}()
	err := f.run(ctx, src, func(ctx context.Context, auth transport.AuthMethod) (err error) {
		if root, err = os.MkdirTemp(f.TempDir, "jam-kit-"); err != nil {
			root = ""
			return err
		}
		commit, err = f.checkout(ctx, root, src, auth)
		return err
	})
	if err != nil {
		return err
	}
	return fn(root, commit)
}

// Resolve returns the commit src.Ref points to without a checkout
// (ls-remote). A 40-hex ref is returned as is, lowercased, with no network
// check at all: it is not verified to exist on the remote.
func (f *Fetcher) Resolve(ctx context.Context, src Source) (string, error) {
	var commit string
	err := f.run(ctx, src, func(ctx context.Context, auth transport.AuthMethod) error {
		if isCommitID(src.Ref) {
			commit = strings.ToLower(src.Ref)
			return nil
		}
		refs, err := listRefs(ctx, src.Repo, auth)
		if err != nil {
			return err
		}
		_, commit, err = findRef(refs, src.Ref)
		return err
	})
	return commit, err
}

var errFetchTimeout = errors.New("fetch time limit reached")

// run checks src, takes the repo's lock, and runs op under the fetch's
// timeout and byte budget with src's credential, if any. op's error comes
// back explained and redacted.
func (f *Fetcher) run(ctx context.Context, src Source, op func(context.Context, transport.AuthMethod) error) error {
	if err := src.check(); err != nil {
		return err
	}
	unlock, err := f.lock(ctx, src.Repo)
	if err != nil {
		return fmt.Errorf("fetch %s: waiting for another fetch of it: %w", redactURL(src.Repo), err)
	}
	defer unlock()

	timeout := f.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	b := &budget{max: f.MaxBytes}
	if b.max <= 0 {
		b.max = DefaultMaxBytes
	}
	ctx, cancel := context.WithTimeoutCause(context.WithValue(ctx, budgetKey{}, b), timeout, errFetchTimeout)
	defer cancel()

	cred, hasCred := f.Creds.For(src.Repo)
	var auth transport.AuthMethod // in memory only: never in a URL or .git/config
	if hasCred {
		auth = basicAuth(src.Repo, cred)
	}
	if err := op(ctx, auth); err != nil {
		return explain(err, ctx, b, src, cred, hasCred, timeout)
	}
	return nil
}

func (f *Fetcher) lock(ctx context.Context, repo string) (func(), error) {
	f.mu.Lock()
	if f.locks == nil {
		f.locks = map[string]chan struct{}{}
	}
	ch := f.locks[repo]
	if ch == nil {
		ch = make(chan struct{}, 1)
		f.locks[repo] = ch
	}
	f.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// checkout checks src.Ref out at depth 1 into root, a fresh temp dir. The
// work tree is written through a capped filesystem (see capFS), so the
// checkout is bounded in bytes and stops when ctx ends; the object store in
// .git holds the received pack, which the transfer budget bounds.
func (f *Fetcher) checkout(ctx context.Context, root string, src Source, auth transport.AuthMethod) (string, error) {
	if err := os.Chmod(root, 0o700); err != nil { // MkdirTemp's mode already; made explicit
		return "", err
	}
	if testHookCheckout != nil {
		testHookCheckout(root)
	}
	max := f.MaxCheckoutBytes
	if max <= 0 {
		max = DefaultMaxCheckoutBytes
	}
	wt := &capFS{Filesystem: osfs.New(root), c: &capCounter{ctx: ctx, max: max}}
	dot, err := osfs.New(root).Chroot(git.GitDirName)
	if err != nil {
		return "", err
	}
	store := filesystem.NewStorage(dot, cache.NewObjectLRUDefault())

	if isCommitID(src.Ref) {
		return checkoutCommit(ctx, store, wt, src.Repo, strings.ToLower(src.Ref), auth)
	}
	refs, err := listRefs(ctx, src.Repo, auth)
	if err != nil {
		return "", err
	}
	name, _, err := findRef(refs, src.Ref)
	if err != nil {
		return "", err
	}
	// git.CloneContext is git.PlainCloneContext with the storage and work
	// tree passed in, so the work tree can be the capped filesystem.
	repo, err := git.CloneContext(ctx, store, wt, &git.CloneOptions{
		URL:           src.Repo,
		Auth:          auth,
		ReferenceName: name,
		SingleBranch:  true,
		NoCheckout:    false,
		Depth:         1,
		Tags:          git.NoTags,
	})
	if err != nil {
		return "", err
	}
	head, err := repo.Head()
	if err != nil {
		return "", err
	}
	h := head.Hash()
	if tag, err := repo.TagObject(h); err == nil { // HEAD at an annotated tag
		c, err := tag.Commit()
		if err != nil {
			return "", err
		}
		h = c.Hash
	}
	return h.String(), nil
}

var errCommitNotFound = errors.New("commit not found")

// checkoutCommit fetches exactly sha (+sha:refs/heads/jam, depth 1) into a
// new repository and checks it out.
func checkoutCommit(ctx context.Context, store *filesystem.Storage, wt billy.Filesystem, repoURL, sha string, auth transport.AuthMethod) (string, error) {
	repo, err := git.Init(store, wt)
	if err != nil {
		return "", err
	}
	remote, err := repo.CreateRemote(&config.RemoteConfig{Name: git.DefaultRemoteName, URLs: []string{repoURL}})
	if err != nil {
		return "", err
	}
	err = remote.FetchContext(ctx, &git.FetchOptions{
		RefSpecs: []config.RefSpec{config.RefSpec("+" + sha + ":refs/heads/jam")},
		Depth:    1,
		Auth:     auth,
		Tags:     git.NoTags,
	})
	if errors.As(err, new(git.NoMatchingRefSpecError)) || err != nil && strings.Contains(err.Error(), "not our ref") {
		return "", errCommitNotFound
	}
	if err != nil {
		return "", err
	}
	w, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	if err := w.Checkout(&git.CheckoutOptions{Branch: "refs/heads/jam"}); err != nil {
		return "", err
	}
	return sha, nil
}

func listRefs(ctx context.Context, repoURL string, auth transport.AuthMethod) ([]*plumbing.Reference, error) {
	r := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: git.DefaultRemoteName, URLs: []string{repoURL}})
	return r.ListContext(ctx, &git.ListOptions{Auth: auth, PeelingOption: git.AppendPeeled})
}

var errRefNotFound = errors.New("ref not found")

// findRef picks refs/heads/<ref>, else refs/tags/<ref>, and the commit it
// names (an annotated tag peeled).
func findRef(refs []*plumbing.Reference, ref string) (plumbing.ReferenceName, string, error) {
	byName := map[plumbing.ReferenceName]plumbing.Hash{}
	for _, r := range refs {
		byName[r.Name()] = r.Hash()
	}
	if h, ok := byName[plumbing.NewBranchReferenceName(ref)]; ok {
		return plumbing.NewBranchReferenceName(ref), h.String(), nil
	}
	tag := plumbing.NewTagReferenceName(ref)
	if h, ok := byName[tag+"^{}"]; ok {
		return tag, h.String(), nil
	}
	if h, ok := byName[tag]; ok {
		return tag, h.String(), nil
	}
	return "", "", errRefNotFound
}

// explain turns a fetch error into one an operator can act on, with no
// userinfo, query or credential in it.
func explain(err error, ctx context.Context, b *budget, src Source, cred Cred, hasCred bool, timeout time.Duration) error {
	repo := redactURL(src.Repo)
	switch {
	case b.exceeded.Load():
		return fmt.Errorf("fetch %s: exceeded the %d-byte transfer limit", repo, b.max)
	case errors.Is(err, errCheckoutTooBig):
		return fmt.Errorf("fetch %s: checkout %v", repo, err)
	case errors.Is(context.Cause(ctx), errFetchTimeout):
		return fmt.Errorf("fetch %s: timed out after the Fetcher's %s limit", repo, timeout)
	case ctx.Err() != nil:
		return fmt.Errorf("fetch %s: stopped by the caller: %w", repo, context.Cause(ctx))
	case errors.Is(err, errRefNotFound):
		return fmt.Errorf("ref %s not found in %s", src.Ref, repo)
	case errors.Is(err, errCommitNotFound):
		return fmt.Errorf("commit %s not found in %s (or the server does not serve commits by id)", strings.ToLower(src.Ref), repo)
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed):
		if hasCred {
			return fmt.Errorf("auth failed for %s (check kits.git cred %s)", redactURL(cred.Prefix), cred.Name)
		}
		return fmt.Errorf("auth required for %s (add a kits.git entry)", repoHost(src.Repo))
	case errors.Is(err, transport.ErrRepositoryNotFound):
		if hasCred {
			return fmt.Errorf("repository %s not found (or kits.git cred %s cannot read it)", repo, cred.Name)
		}
		return fmt.Errorf("repository %s not found (if private, add a kits.git entry)", repo)
	}
	msg := redactURL(err.Error())
	if hasCred {
		msg = scrub(msg, cred.Value)
		if _, token, ok := strings.Cut(cred.Value, ":"); ok {
			msg = scrub(msg, token)
		}
	}
	return fmt.Errorf("fetch %s: %s", repo, msg)
}

func scrub(msg, secret string) string {
	if secret == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, "REDACTED")
}

func repoHost(repo string) string {
	if u, err := url.Parse(repo); err == nil {
		return u.Host
	}
	return "the repository host"
}

// The byte limit, redirect policy and scheme check are scoped to one Fetch
// or Resolve call through the request context. go-git v5 picks the HTTP
// transport from a process-global protocol table (client.InstallProtocol);
// CloneOptions/FetchOptions/ListOptions take no per-call transport, and
// ProxyOptions only swaps a proxy URL. So one wrapper transport is installed
// for http and https at init, and it reads the call's *budget from the
// request context: requests from other go-git users (no budget in context)
// pass through unchanged, and concurrent fetches never share a counter.
// go-git sends every request with the call's context (advertisement,
// upload-pack and redirects alike), so the budget always reaches it.
func init() {
	t := githttp.NewClient(&http.Client{
		Transport:     budgetTransport{base: http.DefaultTransport},
		CheckRedirect: sameHostRedirect,
	})
	client.InstallProtocol("https", t)
	client.InstallProtocol("http", t)
}

type budgetKey struct{}

// budget counts the response bytes of one fetch.
type budget struct {
	max      int64
	used     atomic.Int64
	exceeded atomic.Bool
}

var errBudgetExceeded = errors.New("transfer limit exceeded")

type budgetTransport struct{ base http.RoundTripper }

func (t budgetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	b, ok := req.Context().Value(budgetKey{}).(*budget)
	if !ok {
		return t.base.RoundTrip(req)
	}
	// Defence in depth: whatever URL go-git built or a redirect produced,
	// a Fetcher only ever speaks https, or http to loopback.
	if req.URL.User != nil || !(req.URL.Scheme == "https" || req.URL.Scheme == "http" && isLoopback(req.URL.Hostname())) {
		return nil, fmt.Errorf("refused request to %s://%s", req.URL.Scheme, req.URL.Host)
	}
	if b.exceeded.Load() {
		return nil, errBudgetExceeded
	}
	res, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	res.Body = &countingBody{ReadCloser: res.Body, b: b}
	return res, nil
}

type countingBody struct {
	io.ReadCloser
	b *budget
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if c.b.used.Add(int64(n)) > c.b.max {
		c.b.exceeded.Store(true)
		return n, errBudgetExceeded
	}
	return n, err
}

// maxRedirects caps the redirect hops a Fetcher's request follows.
const maxRedirects = 10

// sameHostRedirect refuses, for a Fetcher's requests, more than maxRedirects
// hops and any redirect off the original host, so a credential is never even
// offered to another server (go-git would strip it, but the request itself
// is refused). Other go-git users keep go-git's own policy.
func sameHostRedirect(req *http.Request, via []*http.Request) error {
	if _, ok := req.Context().Value(budgetKey{}).(*budget); !ok || len(via) == 0 {
		return nil
	}
	if len(via) >= maxRedirects {
		return fmt.Errorf("refused redirect: more than %d", maxRedirects)
	}
	if !strings.EqualFold(req.URL.Host, via[0].URL.Host) || req.URL.Scheme != via[0].URL.Scheme {
		return fmt.Errorf("refused redirect from %s to another host or scheme (%s://%s)", via[0].URL.Host, req.URL.Scheme, req.URL.Host)
	}
	return nil
}

var errCheckoutTooBig = errors.New("checkout too large")

// capCounter bounds the bytes one checkout writes and stops it when its
// context ends (go-git's work-tree checkout takes no context of its own).
type capCounter struct {
	ctx  context.Context
	max  int64
	used atomic.Int64
}

func (c *capCounter) add(n int64) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if c.used.Add(n) > c.max {
		return fmt.Errorf("%w: exceeded the %d-byte limit", errCheckoutTooBig, c.max)
	}
	return nil
}

// capFS is the checkout's work tree: an os filesystem whose files count
// every byte written (and every Truncate growth) against one capCounter.
type capFS struct {
	billy.Filesystem
	c *capCounter
}

func (fs *capFS) Create(name string) (billy.File, error) {
	return fs.wrap(fs.Filesystem.Create(name))
}

func (fs *capFS) OpenFile(name string, flag int, perm os.FileMode) (billy.File, error) {
	return fs.wrap(fs.Filesystem.OpenFile(name, flag, perm))
}

func (fs *capFS) TempFile(dir, prefix string) (billy.File, error) {
	return fs.wrap(fs.Filesystem.TempFile(dir, prefix))
}

func (fs *capFS) Chroot(path string) (billy.Filesystem, error) {
	sub, err := fs.Filesystem.Chroot(path)
	if err != nil {
		return nil, err
	}
	return &capFS{Filesystem: sub, c: fs.c}, nil
}

func (fs *capFS) wrap(f billy.File, err error) (billy.File, error) {
	if err != nil {
		return nil, err
	}
	if err := fs.c.add(0); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &capFile{File: f, c: fs.c}, nil
}

type capFile struct {
	billy.File
	c *capCounter
}

func (f *capFile) Write(p []byte) (int, error) {
	if err := f.c.add(int64(len(p))); err != nil {
		return 0, err
	}
	return f.File.Write(p)
}

func (f *capFile) Truncate(size int64) error {
	if err := f.c.add(size); err != nil {
		return err
	}
	return f.File.Truncate(size)
}
