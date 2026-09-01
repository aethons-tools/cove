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
// (CloseWrite) so the remote end sees EOF too, rather than yanking the whole
// connection out from under any reply still in flight. The conn→out direction
// runs in the foreground and is what actually decides when relay returns: it
// keeps copying until the remote end closes (or errors), so a client that
// finishes sending first still gets every byte the remote sends back before
// relay returns. Racing both halves against a single "first one home wins"
// select would truncate that reply — losing exactly the bytes a caller like
// ssh needs.
func relay(conn net.Conn, in io.Reader, out io.Writer) error {
	errc := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, in)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			conn.Close()
		}
		errc <- err
	}()
	_, err := io.Copy(out, conn)
	if err == nil {
		err = <-errc
	} else {
		<-errc
	}
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
func doSSHProxy(collaborator, kitDir string, r runner.Runner, stdin io.Reader, stdout, stderr io.Writer) error {
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
