package main

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// A local echo server stands in for the sandbox sshd; relay must shuttle bytes
// both ways over an in-process socket (no Docker, no external network).
func TestRelay_EchoRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		io.Copy(c, c) // echo
		c.Close()
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	in := strings.NewReader("ping\n")
	var out strings.Builder
	if err := relay(conn, in, &out); err != nil && err != io.EOF {
		t.Fatalf("relay: %v", err)
	}
	if out.String() != "ping\n" {
		t.Fatalf("echo mismatch: got %q", out.String())
	}
}

// TestRelay_ReturnsWhenRemoteClosesFirst covers the reverse direction from
// TestRelay_EchoRoundTrip: the SANDBOX side (conn) closes first while in
// (stdin, standing in for an idle terminal) is still open and produces
// nothing. relay must still return promptly — it must not block forever
// waiting on the in→conn direction, since nothing will ever unblock that read
// once the remote is gone. A relay that waits for both directions before
// returning deadlocks here: doSSHProxy's defers never run and the parent ssh
// process hangs forever (OpenSSH has no default keepalive).
func TestRelay_ReturnsWhenRemoteClosesFirst(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		io.WriteString(c, "hello\n")
		c.Close()
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// inR never produces data and is never closed — an idle stdin that
	// outlives the remote hanging up. Its write half is intentionally never
	// used, so a Read on inR blocks forever unless relay simply stops caring
	// about it once the remote side has closed.
	inR, _ := io.Pipe()

	var out strings.Builder
	done := make(chan error, 1)
	go func() { done <- relay(conn, inR, &out) }()

	select {
	case err := <-done:
		if err != nil && err != io.EOF {
			t.Fatalf("relay: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not return within 2s after the remote closed (deadlock)")
	}
	if out.String() != "hello\n" {
		t.Fatalf("got %q, want %q", out.String(), "hello\n")
	}
}
