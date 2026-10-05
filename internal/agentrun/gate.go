package agentrun

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// gateOutputCap bounds the output a gate reports (stdout+stderr combined).
const gateOutputCap = 4096

// capWriter keeps the first gateOutputCap bytes and discards the rest without
// ever blocking the child.
type capWriter struct {
	buf       []byte
	truncated bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	if room := gateOutputCap - len(c.buf); room > 0 {
		if len(p) > room {
			c.buf = append(c.buf, p[:room]...)
			c.truncated = true
		} else {
			c.buf = append(c.buf, p...)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

// runGate runs an alarm's gate: sh -c command in dir with env (nil inherits
// cove-master's), killed after timeout. exit is the shell's status (-1 when
// killed at the timeout; 127 when the shell itself could not start); out is
// the capped, UTF-8-safe combined output, marked when truncated. It never
// starts an agent turn.
func runGate(ctx context.Context, dir string, env []string, command string, timeout time.Duration) (exit int, timedOut bool, out []byte, truncated bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir, cmd.Env = dir, env
	var w capWriter
	cmd.Stdout, cmd.Stderr = &w, &w
	cmd.WaitDelay = 2 * time.Second // a child holding the pipe can't hang us
	err := cmd.Run()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		exit, timedOut = -1, true
	case cmd.ProcessState != nil:
		exit = cmd.ProcessState.ExitCode()
	case err != nil:
		exit = 127
		w.Write([]byte(err.Error()))
	}
	// Invalid bytes become U+FFFD, which can grow the text: re-cap on a rune
	// boundary.
	s, truncated := strings.ToValidUTF8(string(w.buf), "\uFFFD"), w.truncated
	if len(s) > gateOutputCap {
		cut := gateOutputCap
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s, truncated = s[:cut], true
	}
	if truncated {
		s += "\n[output truncated]"
	}
	return exit, timedOut, []byte(s), truncated
}
