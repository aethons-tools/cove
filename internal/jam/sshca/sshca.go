// Package sshca is Jam's SSH certificate authority for studio collaboration:
// short-lived user certs for principal "agent" that a studio's sshd trusts via
// TrustedUserCAKeys. The CA key lives only in memory; errors never echo it.
package sshca

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// Validity is how long an issued cert is good for.
	Validity = 8 * time.Hour
	// Principal is the only login an issued cert is valid for: the studio's
	// agent user.
	Principal = "agent"
)

// CA signs user certs with Jam's ed25519 SSH CA key.
type CA struct{ signer ssh.Signer }

// Parse loads an ed25519 CA private key (OpenSSH or PKCS#8 PEM, unencrypted).
// Its errors never include the key bytes.
func Parse(pemKey []byte) (*CA, error) {
	raw, err := ssh.ParseRawPrivateKey(pemKey)
	if err != nil {
		return nil, errors.New("ssh CA key is not a valid unencrypted private key")
	}
	var k ed25519.PrivateKey
	switch v := raw.(type) {
	case *ed25519.PrivateKey:
		k = *v
	case ed25519.PrivateKey:
		k = v
	default:
		return nil, errors.New("ssh CA key must be ed25519")
	}
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		return nil, errors.New("ssh CA key is not usable as a signer")
	}
	return &CA{signer: s}, nil
}

// PublicKey is the CA's public key as one authorized_keys line (newline
// terminated) — what a studio's TrustedUserCAKeys file holds.
func (c *CA) PublicKey() []byte { return ssh.MarshalAuthorizedKey(c.signer.PublicKey()) }

// Issue signs userPub (an ed25519 authorized_keys line) as a user cert for
// Principal, valid from a minute before now (clock skew) until now+Validity,
// recording keyID for sshd's auth log. It returns the cert as an
// authorized_keys line and its expiry. Pty, port forwarding and user rc are
// permitted; agent and X11 forwarding are not. The cert is usable only from
// SourceAddress.
// SourceAddress is every cert's source-address critical option: the studio's
// loopback, where cove-master dials sshd for a tunnel.
const SourceAddress = "127.0.0.1/32,::1/128"

func (c *CA) Issue(userPub []byte, keyID string, now time.Time) ([]byte, time.Time, error) {
	pk, _, _, _, err := ssh.ParseAuthorizedKey(userPub)
	if err != nil {
		return nil, time.Time{}, errors.New("not an SSH public key")
	}
	if pk.Type() != ssh.KeyAlgoED25519 {
		return nil, time.Time{}, errors.New("user key must be ed25519")
	}
	var serial [8]byte
	if _, err := rand.Read(serial[:]); err != nil {
		return nil, time.Time{}, fmt.Errorf("cert serial: %w", err)
	}
	exp := now.Add(Validity)
	cert := &ssh.Certificate{
		Key:             pk,
		Serial:          binary.BigEndian.Uint64(serial[:]),
		CertType:        ssh.UserCert,
		KeyId:           keyID,
		ValidPrincipals: []string{Principal},
		ValidAfter:      uint64(now.Add(-time.Minute).Unix()),
		ValidBefore:     uint64(exp.Unix()),
		Permissions: ssh.Permissions{CriticalOptions: map[string]string{
			// Only the in-studio loopback path cove-master dials: a cert must not
			// work through a host-published sshd port, around the tunnel,
			// collab.Decide and the revocation sweep.
			"source-address": SourceAddress,
		}, Extensions: map[string]string{
			"permit-pty": "", "permit-port-forwarding": "", "permit-user-rc": "",
		}},
	}
	if err := cert.SignCert(rand.Reader, c.signer); err != nil {
		return nil, time.Time{}, fmt.Errorf("sign cert: %w", err)
	}
	return ssh.MarshalAuthorizedKey(cert), exp, nil
}
