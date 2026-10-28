//go:build browser

// Package browsertest holds the headless-Chrome helpers the admin area's
// browser tests share (`just test-browser`; each test skips without a
// browser). The user area's tests keep their own copies in userui.
package browsertest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Step is a browser action that yields no value: what chromedp.Do runs.
type Step = chromedp.Action[chromedp.Void]

// Ctx returns a chromedp context on a fresh headless browser, skipping the
// test when none is found. JAM_BROWSER names the binary explicitly; otherwise
// the first of the usual names on PATH is used.
func Ctx(t *testing.T) context.Context {
	t.Helper()
	path := os.Getenv("JAM_BROWSER")
	for _, name := range []string{"chrome-headless-shell", "headless-shell", "chromium", "google-chrome"} {
		if path != "" {
			break
		}
		path, _ = exec.LookPath(name)
	}
	if path == "" {
		t.Skip("no headless browser found (set JAM_BROWSER or install chrome-headless-shell)")
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(path),
		// The sandbox VM has no user namespaces for Chrome's own sandbox.
		chromedp.NoSandbox,
	)
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelB := chromedp.NewContext(actx)
	ctx, cancelC := context.WithTimeout(ctx, 30*time.Second)
	t.Cleanup(func() { cancelC(); cancelB(); cancelA() })
	return ctx
}

// Into runs a and stores its value in *dst, so an action that returns a value
// can sit among the steps of one chromedp.Do.
func Into[T any](dst *T, a chromedp.Action[T]) Step {
	return func(ctx context.Context, tg *chromedp.Target) (chromedp.Void, error) {
		v, err := a(ctx, tg)
		if err == nil {
			*dst = v
		}
		return chromedp.Void{}, err
	}
}

// Call sends one protocol command to the tab, ignoring its result.
func Call[P, R any](c cdp.Command[P, R], params P) Step {
	return chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		_, err := cdp.Call(ctx, tg, c, params)
		return err
	})
}

// Problems collects, from when Watch runs, the tab's CSP violations (Log
// "security" entries — they never reach the console API), error log entries,
// and uncaught exceptions.
type Problems struct {
	mu   sync.Mutex
	list []string
}

func (p *Problems) add(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.list = append(p.list, s)
}

// All is what was collected so far.
func (p *Problems) All() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.list...)
}

// Watch subscribes to the tab's problems and enables the domains that report them.
func (p *Problems) Watch(ctx context.Context) Step {
	return chromedp.Steps(
		chromedp.Func(func(_ context.Context, tg *chromedp.Target) error {
			// Subscribe before enabling, so the entries Enable replays arrive too.
			go func() {
				for e, err := range cdp.Events(ctx, tg, cdplog.EntryAdded) {
					if err != nil {
						return
					}
					if e.Entry.Source == cdplog.EntrySourceSecurity || e.Entry.Level == cdplog.EntryLevelError {
						p.add(string(e.Entry.Source) + ": " + e.Entry.Text)
					}
				}
			}()
			go func() {
				for e, err := range cdp.Events(ctx, tg, runtime.ExceptionThrown) {
					if err != nil {
						return
					}
					p.add("exception: " + e.ExceptionDetails.Text)
				}
			}()
			return nil
		}),
		Call(cdplog.Enable, cdp.Empty{}),
		Call(runtime.Enable, cdp.Empty{}),
	)
}

// byText is a JS expression for the first sel element whose trimmed text is
// text (null when none).
func byText(sel, text string) string {
	return fmt.Sprintf(`[...document.querySelectorAll(%q)].find((e) => e.textContent.trim() === %q) ?? null`, sel, text)
}

// WaitText waits until an element matching sel has exactly text.
func WaitText(sel, text string) Step {
	return Into(new(chromedp.Void), chromedp.Poll[chromedp.Void]("("+byText(sel, text)+") !== null"))
}

// ClickText waits for, then clicks, the sel element with exactly text.
func ClickText(sel, text string) Step {
	return chromedp.Steps(
		WaitText(sel, text),
		Into(new(bool), chromedp.Evaluate[bool]("(("+byText(sel, text)+").click(), true)")),
	)
}
