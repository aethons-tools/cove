package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

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
	// the collaborator from it at connect time.
	proxy := fmt.Sprintf("%s ssh-proxy --project-dir %s %s", self, filepath.Dir(kitDir), collaborator)
	alias := "cove-" + st.Container
	block := sshconfig.RenderHostBlock(sshconfig.HostParams{
		Alias:          alias,
		ProxyCommand:   proxy,
		User:           "agent",
		IdentityFile:   priv,
		KnownHostsFile: filepath.Join(configDir(), "known_hosts.d", st.Container),
	})

	if write {
		if err := writeManagedBlock(alias, block); err != nil {
			return err
		}
		fmt.Fprintf(out, "wrote managed block for %s to %s\n", alias, sshConfigPath())
		fmt.Fprintf(out, "git remote add sandbox %s\n", sshconfig.GitRemoteURL(alias))
		return nil
	}

	fmt.Fprint(out, block)
	fmt.Fprintf(out, "\n# git-over-SSH:\n# git remote add sandbox %s\n", sshconfig.GitRemoteURL(alias))
	return nil
}

func sshConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "config")
}

// writeManagedBlock idempotently upserts the block into ~/.ssh/config, creating
// ~/.ssh (0700) and the file (0600) if absent.
func writeManagedBlock(alias, block string) error {
	path := sshConfigPath()
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
