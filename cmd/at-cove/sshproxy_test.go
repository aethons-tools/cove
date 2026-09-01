package main

import (
	"io"
	"net"
	"strings"
	"testing"
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
