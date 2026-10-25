package collabhttp

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabwire"
	"github.com/aethons-tools/cove/internal/jam/preview"
)

// CloseUnexposed never closes the built-in IDE (it is never exposed), but a
// kit ide on another port, and an app port, follow the exposure set. SSH
// tunnels are not its business.
func TestCloseUnexposedSkipsBuiltInIDE(t *testing.T) {
	l := NewLive()
	closed := make(chan string, 8)
	add := func(name string, right collab.Right, port int, viaPreview bool) {
		l.add(&liveEntry{
			LiveConn: collabwire.LiveConn{Session: "ses_a", User: name, Since: time.Now()},
			right:    right, port: port, viaPreview: viaPreview,
			close: func() { closed <- name },
		})
	}
	add("ide", collab.SSH, preview.IDEPort, true)
	add("app", collab.Preview, 8080, true)
	add("kit-ide", collab.SSH, 8443, true)
	add("sshd", collab.SSH, int(SSHPort), false)
	if n := l.CloseUnexposed("ses_a", nil); n != 2 {
		t.Fatalf("closed %d, want 2", n)
	}
	got := map[string]bool{}
	for range 2 {
		select {
		case name := <-closed:
			got[name] = true
		case <-time.After(time.Second):
			t.Fatal("closes did not run")
		}
	}
	if !got["app"] || !got["kit-ide"] {
		t.Fatalf("closed %v, want app and kit-ide", got)
	}
}
