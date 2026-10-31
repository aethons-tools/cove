package configrepo

import (
	"errors"
	"fmt"
	"net"
	"net/url"
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
		auth.HostKeyCallback = func(host string, remote net.Addr, k cryptossh.PublicKey) error {
			err := check(host, remote, k)
			var ke *knownhosts.KeyError
			if errors.As(err, &ke) {
				return fmt.Errorf("config.git: the host key of %s is not in host-keys; if you trust it, add: %s",
					host, knownhosts.Line([]string{knownhosts.Normalize(host)}, k))
			}
			return err
		}
		return auth, nil
	}
}
