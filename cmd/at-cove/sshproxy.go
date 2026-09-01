package main

import (
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/state"
)

// relay shuttles bytes between the ssh client (in→conn) and the sandbox sshd
// (conn→out). out carries the raw ssh byte stream, so NOTHING else may be
// written to it — diagnostics go to stderr.
//
// The in→conn direction runs in the background: when in hits EOF (the local
// ssh client has nothing left to send), it half-closes conn's write side
// (CloseWrite, best-effort) so the remote end sees EOF too, rather than
// yanking the whole connection out from under any reply still in flight. The
// conn→out direction runs in the foreground and is what actually decides when
// relay returns: it returns as soon as the remote end closes (or errors) —
// relay does NOT wait on the background goroutine. That asymmetry is
// deliberate, not sloppy cleanup: in is typically stdin, an idle terminal
// that outlives the remote hanging up, so a Read on it may never return. If
// relay waited for both directions, a remote-initiated close (e.g. the
// sandbox sshd exiting) would deadlock forever — doSSHProxy's defers would
// never run and the parent ssh process would hang with no way out (OpenSSH
// has no default keepalive). Once relay returns, doSSHProxy's caller tears
// down the process, reclaiming the background goroutine along with it; it
// does not need to be joined.
func relay(conn net.Conn, in io.Reader, out io.Writer) error {
	go func() {
		_, _ = io.Copy(conn, in)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = conn.Close()
		}
	}()
	_, err := io.Copy(out, conn)
	return err
}

// loadInstanceState resolves a collaborator to its instance state, reusing the
// exact chain doChat uses (main.go:722): install manifest → instanceFor → state.
func loadInstanceState(kitDir, collaborator string) (state.State, error) {
	m, err := loadCurrentInstall(kitDir)
	if err != nil {
		return state.State{}, err
	}
	_, _, instKey, _, err := instanceFor(m.RunConfig, collaborator)
	if err != nil {
		return state.State{}, err
	}
	return state.LoadFor(kitDir, instKey)
}

// doSSHProxy is the ProxyCommand target: resolve the sandbox, discover its
// current (rotating) ssh port via the backend Dial, and relay stdio to it.
// stdout carries the raw ssh byte stream; there is no stderr parameter
// because relay (see above) writes no diagnostics of its own — errors
// propagate as the returned error, which the caller reports on its own stderr.
func doSSHProxy(collaborator, kitDir string, r runner.Runner, stdin io.Reader, stdout io.Writer) error {
	st, err := loadInstanceState(kitDir, collaborator)
	if err != nil {
		return err
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}
	ep, cleanup, err := b.Dial(st.Container)
	if err != nil {
		return err
	}
	defer cleanup()
	conn, err := net.Dial("tcp", net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port)))
	if err != nil {
		return fmt.Errorf("ssh-proxy: dial sandbox: %w", err)
	}
	defer conn.Close()
	return relay(conn, stdin, stdout)
}
