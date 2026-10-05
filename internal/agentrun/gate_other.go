//go:build !unix

package agentrun

import "os/exec"

func inOwnGroup(*exec.Cmd)           {}
func signalExit(*exec.ExitError) int { return 0 }
