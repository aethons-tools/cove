package sshconfig

import "testing"

func TestRenderHostBlock(t *testing.T) {
	got := RenderHostBlock(HostParams{
		Alias:          "cove-demo-agent",
		ProxyCommand:   "/usr/local/bin/at-cove ssh-proxy --project-dir /p demo",
		User:           "agent",
		IdentityFile:   "/home/u/.config/at-cove/id_ed25519",
		KnownHostsFile: "/home/u/.config/at-cove/known_hosts.d/demo-agent",
	})
	want := `Host cove-demo-agent
    HostName cove-demo-agent
    User agent
    IdentityFile /home/u/.config/at-cove/id_ed25519
    IdentitiesOnly yes
    UserKnownHostsFile /home/u/.config/at-cove/known_hosts.d/demo-agent
    StrictHostKeyChecking accept-new
    ProxyCommand /usr/local/bin/at-cove ssh-proxy --project-dir /p demo
`
	if got != want {
		t.Fatalf("RenderHostBlock mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestGitRemoteURL(t *testing.T) {
	if got := GitRemoteURL("cove-demo-agent"); got != "cove-demo-agent:/home/agent/workspace" {
		t.Fatalf("GitRemoteURL = %q", got)
	}
}
