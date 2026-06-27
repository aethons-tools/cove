// Package sshargs builds argv (after the "ssh" binary name) for the ssh
// client. Pure: no I/O.
package sshargs

import "strconv"

// Target identifies a VM's sshd and the local credentials to reach it.
type Target struct {
	Host           string
	User           string
	Port           int
	IdentityFile   string
	KnownHostsFile string
}

// Base returns the common ssh options ending in user@host.
func Base(t Target) []string {
	return []string{
		"-i", t.IdentityFile,
		"-p", strconv.Itoa(t.Port),
		"-o", "UserKnownHostsFile=" + t.KnownHostsFile,
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "IdentitiesOnly=yes",
		t.User + "@" + t.Host,
	}
}

// InteractiveSendEnv builds an interactive (pty) ssh argv that forwards the
// named environment variables and runs remoteCmd.
func InteractiveSendEnv(t Target, envNames []string, remoteCmd string) []string {
	args := []string{"-tt"}
	for _, n := range envNames {
		args = append(args, "-o", "SendEnv="+n)
	}
	args = append(args, Base(t)...)
	if remoteCmd != "" {
		args = append(args, remoteCmd)
	}
	return args
}
