package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aethons-tools/cove/internal/keys"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshconfig"
)

// doView prints (or --writes) an OpenSSH Host block for reaching the sandbox
// with VS Code Remote-SSH, plus a git-over-SSH remote line. The block routes
// through `at-cove ssh-proxy`, so the alias survives recreate (rotating port).
func doView(collaborator, kitDir string, r runner.Runner, write bool, out io.Writer) error {
	st, err := loadInstanceState(kitDir, collaborator)
	if err != nil {
		return err
	}
	priv, _, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil || self == "" {
		self = "at-cove"
	}
	// projectDir is the parent of the kit's .at-cove dir; ssh-proxy re-resolves
	// the collaborator from it at connect time. It must be absolutized: ssh
	// invokes ProxyCommand via `/bin/sh -c` from an arbitrary cwd (not the
	// directory `view` was run from), so a relative --project-dir embedded here
	// would resolve against the wrong directory at connect time.
	projectDir, err := filepath.Abs(filepath.Dir(kitDir))
	if err != nil {
		return fmt.Errorf("view: absolutize project dir: %w", err)
	}
	// Each field is shell-quoted: OpenSSH runs ProxyCommand through /bin/sh -c,
	// so an unquoted path containing a space or shell metacharacter would
	// word-split into a broken command.
	proxy := fmt.Sprintf("%s ssh-proxy --project-dir %s %s",
		shellQuote(self), shellQuote(projectDir), shellQuote(collaborator))
	alias := "cove-" + st.Container
	block := sshconfig.RenderHostBlock(sshconfig.HostParams{
		Alias:          alias,
		ProxyCommand:   proxy,
		User:           "agent",
		IdentityFile:   priv,
		KnownHostsFile: filepath.Join(configDir(), "known_hosts.d", st.Container),
	})

	if write {
		path, err := sshConfigPath()
		if err != nil {
			return fmt.Errorf("view --write: %w", err)
		}
		if err := writeManagedBlock(path, alias, block); err != nil {
			return err
		}
		fmt.Fprintf(out, "wrote managed block for %s to %s\n", alias, path)
		fmt.Fprintf(out, "git remote add sandbox %s\n", sshconfig.GitRemoteURL(alias))
		return nil
	}

	fmt.Fprint(out, block)
	fmt.Fprintf(out, "\n# git-over-SSH:\n# git remote add sandbox %s\n", sshconfig.GitRemoteURL(alias))
	return nil
}

// shellQuote POSIX single-quotes s for safe interpolation into a ProxyCommand
// line, which OpenSSH runs via `/bin/sh -c`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sshConfigPath resolves ~/.ssh/config, failing loudly (rather than silently
// falling back to a cwd-relative path) if $HOME can't be resolved.
func sshConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

// writeManagedBlock idempotently upserts the block into path (~/.ssh/config),
// creating its directory (0700) and the file (0600) if absent.
func writeManagedBlock(path, alias, block string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	updated := sshconfig.UpsertManagedBlock(string(existing), alias, block)
	return os.WriteFile(path, []byte(updated), 0o600)
}
