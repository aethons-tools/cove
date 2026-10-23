//go:build browser

package userui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// The editor mounts Excalidraw on a board, saves what is drawn, and shows an
// edit made elsewhere (an agent's) without a reload.
func TestBrowserWhiteboardEditor(t *testing.T) {
	if !uiassets.WhiteboardBuilt() {
		t.Skip("whiteboard bundle not built (just whiteboard)")
	}
	ctx := browserCtx(t)
	e, p := fixture()
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	b, err := e.Deps.Boards.Create(jam.Poster{ID: e.alice.ID}, engID, "Arch", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := withStatic(Handler(e.Deps, nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, jam.WithParticipant(r, p))
	}))
	defer srv.Close()

	var mounted bool
	start := time.Now()
	defer func() { t.Logf("took %v", time.Since(start)) }()
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1400, 1000),
		chromedp.Navigate(srv.URL+"/app/user/?c="+engID+"&board="+string(b.ID)),
		chromedp.WaitVisible(`.excalidraw canvas`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('.excalidraw')`, &mounted),
		// Draw a rectangle: the toolbar's rectangle tool, then drag on the canvas.
		chromedp.Click(`[data-testid="toolbar-rectangle"]`, chromedp.ByQuery),
		chromedp.MouseEvent(input.MouseMoved, 700, 450),
		chromedp.MouseEvent(input.MousePressed, 700, 450, chromedp.ButtonLeft),
		chromedp.MouseEvent(input.MouseMoved, 760, 500, chromedp.ButtonLeft),
		chromedp.MouseEvent(input.MouseMoved, 820, 540, chromedp.ButtonLeft),
		chromedp.MouseEvent(input.MouseReleased, 820, 540, chromedp.ButtonLeft),
	); err != nil {
		t.Fatal(err)
	}
	t.Logf("drawn at %v", time.Since(start))
	if !mounted {
		t.Fatal("Excalidraw didn't mount")
	}
	var saved []json.RawMessage
	for range 50 {
		saved, _, _ = e.Deps.Boards.Elements(e.alice.ID, b.ID, 0)
		if len(saved) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(saved) != 1 || !strings.Contains(string(saved[0]), `"type":"rectangle"`) {
		t.Fatalf("saved = %s", saved)
	}
	// An agent adds a shape; the open editor shows it (here via the poll's
	// push path: dispatch the event the live stream would).
	if _, _, err := e.Deps.Boards.Apply(e.alice.ID, b.ID, []jam.BoardOp{{Op: "add", Type: "ellipse", ID: "agent-shape", Text: "from an agent"}}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`window.dispatchEvent(new CustomEvent('jam-board', {detail: `+jsString(string(b.ID))+`}))`, nil),
		chromedp.Sleep(800*time.Millisecond),
		chromedp.Evaluate(`window.__jamBoardCount ? window.__jamBoardCount() : -1`, &n),
	); err != nil {
		t.Fatal(err)
	}
	if n < 3 { // the rectangle, the ellipse and its label
		t.Fatalf("editor shows %d elements after the agent's edit", n)
	}
	if path := os.Getenv("COVE_BOARD_SCREENSHOT"); path != "" {
		var png []byte
		err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&png))
		t.Logf("screenshot at %v: %v", time.Since(start), err)
		if err == nil {
			_ = os.WriteFile(path, png, 0o644)
		}
	}
}

func jsString(s string) string { b, _ := json.Marshal(s); return string(b) }
