//go:build !unix

package agentrun

import (
	"errors"
	"os/exec"
)

func keepGroup(*exec.Cmd)         {}
func signalGroup(int, bool) error { return errors.New("process groups are unsupported here") }
