package configrepo

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	cryptossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHUser is the user of an ssh remote URL (ssh://u@h/p or u@h:p); "git"
// when it has none.
func SSHUser(rawURL string) string {
	if strings.Contains(rawURL, "://") {
		if u, err := url.Parse(rawURL); err == nil && u.User != nil && u.User.Username() != "" {
			return u.User.Username()
		}
		return "git"
	}
	if user, _, ok := strings.Cut(rawURL, "@"); ok && user != "" && !strings.ContainsAny(user, "/:") {
		return user
	}
	return "git"
}

// SSHAuth is a Repo.Auth that signs in as user with the private key key
// returns (OpenSSH or PEM), trusting only the host keys in knownHostsFile.
// A host not in the file fails with the line to add. The key is read on
// every call and never quoted in an error.
func SSHAuth(user string, key func() (string, error), knownHostsFile string) func() (transport.AuthMethod, error) {
	return func() (transport.AuthMethod, error) {
		pem, err := key()
		if err != nil {
			return nil, err
		}
		auth, err := gogitssh.NewPublicKeys(user, []byte(pem), "")
		if err != nil {
			return nil, errors.New("config.git.cred: not an OpenSSH or PEM private key")
		}
		check, err := knownhosts.New(knownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("config.git: reading host-keys: %w", err)
		}
		// Offer only the key types host-keys pins, so a host pinned with
		// (say) an RSA key isn't refused for offering ed25519 first.
		auth.HostKeyAlgorithms = pinnedAlgorithms(knownHostsFile)
		auth.HostKeyCallback = func(host string, remote net.Addr, k cryptossh.PublicKey) error {
			err := check(host, remote, k)
			var ke *knownhosts.KeyError
			if errors.As(err, &ke) {
				line := knownhosts.Line([]string{knownhosts.Normalize(host)}, k)
				if len(ke.Want) > 0 {
					return fmt.Errorf("config.git: the host key of %s does not match host-keys (it may have been rotated, or this may be an attack); verify it out of band before replacing the line with: %s",
						host, line)
				}
				return fmt.Errorf("config.git: the host key of %s is not in host-keys; if you trust it, add: %s",
					host, line)
			}
			return err
		}
		return auth, nil
	}
}

// pinnedAlgorithms is the host key algorithms for the key types the
// known-hosts file lists (nil when it lists none: the defaults apply).
func pinnedAlgorithms(file string) []string {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var algs []string
	seen := map[string]bool{}
	for len(data) > 0 {
		_, _, k, _, rest, err := cryptossh.ParseKnownHosts(data)
		if err != nil {
			break
		}
		data = rest
		var add []string
		switch k.Type() {
		case cryptossh.KeyAlgoRSA:
			add = []string{cryptossh.KeyAlgoRSASHA512, cryptossh.KeyAlgoRSASHA256, cryptossh.KeyAlgoRSA}
		default:
			add = []string{k.Type()}
		}
		for _, a := range add {
			if !seen[a] {
				seen[a] = true
				algs = append(algs, a)
			}
		}
	}
	return algs
}
