package gittest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ServeSSH commits files to main of a new bare repository and serves it over
// ssh at url (ssh://git@127.0.0.1:<port>/repo.git) on 127.0.0.1. The server
// has a fresh ed25519 host key (knownHostsLine pins it) and accepts only the
// fresh client key clientKeyPEM. Head, Show and Git work on url. Tests skip
// without the git CLI.
func ServeSSH(t testing.TB, files map[string]string) (url, knownHostsLine, clientKeyPEM string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "r.git")
	work := filepath.Join(root, "work")
	run(t, "", "git", "init", "-q", "--bare", "-b", "main", bare)
	run(t, "", "git", "init", "-q", "-b", "main", work)
	for name, body := range files {
		p := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(t, work, "git", "add", "-A")
	run(t, work, "git", "commit", "-q", "--allow-empty", "-m", "update")
	run(t, work, "git", "push", "-q", bare, "main")

	hostSigner := newSigner(t)
	clientPriv, clientSigner := newKey(t)
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if string(k.Marshal()) == string(clientSigner.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unknown key")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSHConn(c, cfg, bare)
		}
	}()

	addr := ln.Addr().String()
	url = "ssh://git@" + addr + "/repo.git"
	bares.Store(url, bare)
	t.Cleanup(func() { bares.Delete(url) })
	return url, knownhosts.Line([]string{knownhosts.Normalize(addr)}, hostSigner.PublicKey()), clientPriv
}

// SSHHead is branch's commit in the repository ServeSSH served at url
// ("" = none).
func SSHHead(t testing.TB, url, branch string) string {
	t.Helper()
	return Head(t, url, branch)
}

// newKey makes an ed25519 key: its OpenSSH PEM and signer.
func newKey(t testing.TB) (string, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(blk)), signer
}

func newSigner(t testing.TB) ssh.Signer {
	_, s := newKey(t)
	return s
}

func serveSSHConn(nc net.Conn, cfg *ssh.ServerConfig, bare string) {
	defer nc.Close()
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			return
		}
		go serveSSHSession(ch, creqs, bare)
	}
}

func serveSSHSession(ch ssh.Channel, reqs <-chan *ssh.Request, bare string) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			req.Reply(false, nil)
			return
		}
		verb, _, _ := strings.Cut(payload.Command, " ")
		sub, ok := strings.CutPrefix(verb, "git-")
		if !ok || (sub != "upload-pack" && sub != "receive-pack") {
			req.Reply(false, nil)
			return
		}
		req.Reply(true, nil)
		cmd := exec.Command("git", sub, bare)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
		status := uint32(0)
		if err := cmd.Run(); err != nil {
			status = 1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				status = uint32(ee.ExitCode())
			} else {
				fmt.Fprintln(ch.Stderr(), err)
			}
		}
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, status)
		ch.SendRequest("exit-status", false, b)
		return
	}
}
