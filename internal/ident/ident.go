// Package ident mints and parses Jam's surrogate ids: opaque, kind-prefixed,
// time-sortable strings such as "usr_01j9q3…". An id is never derived from a
// name and never reused; its Kind is the only thing a caller may read from it.
package ident

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"
)

// Kind is an id's entity kind, written as its prefix.
type Kind string

const (
	Project    Kind = "prj"
	User       Kind = "usr"
	Connection Kind = "con"
	Account    Kind = "acc"
	Session    Kind = "ses"
	Channel    Kind = "chn"
)

var kinds = map[Kind]bool{Project: true, User: true, Connection: true, Account: true, Session: true, Channel: true}

// alphabet is lowercase Crockford base32. Its characters ascend in ASCII, so
// the text order of equal-length bodies is their numeric order — and, because
// the time comes first, their creation order.
const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// bodyLen is 128 bits in base32: 26 characters, the first holding 3 bits.
const bodyLen = 26

// ID is a surrogate id.
type ID string

// New mints a fresh id of kind k. It panics on an unknown kind (a programming
// error) or if the system entropy source fails.
func New(k Kind) ID { return newAt(k, time.Now(), rand.Reader) }

func newAt(k Kind, t time.Time, r io.Reader) ID {
	if !kinds[k] {
		panic("ident: unknown kind " + string(k))
	}
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	if _, err := io.ReadFull(r, b[6:]); err != nil {
		panic("ident: entropy: " + err.Error())
	}
	return ID(string(k) + "_" + encode(b))
}

func encode(b [16]byte) string {
	n := new(big.Int).SetBytes(b[:])
	mask := big.NewInt(31)
	out := make([]byte, bodyLen)
	for i := bodyLen - 1; i >= 0; i-- {
		out[i] = alphabet[new(big.Int).And(n, mask).Int64()]
		n.Rsh(n, 5)
	}
	return string(out)
}

// Parse validates s as an id: a known kind prefix, "_", and a well-formed body.
func Parse(s string) (ID, error) {
	k, body, ok := strings.Cut(s, "_")
	if !ok || !kinds[Kind(k)] {
		return "", fmt.Errorf("ident: %q has no known kind prefix", s)
	}
	if len(body) != bodyLen || body[0] < '0' || body[0] > '7' {
		return "", fmt.Errorf("ident: %q has a malformed body", s)
	}
	for i := 0; i < len(body); i++ {
		if strings.IndexByte(alphabet, body[i]) < 0 {
			return "", fmt.Errorf("ident: %q has a malformed body", s)
		}
	}
	return ID(s), nil
}

// Kind returns the id's kind (its prefix). It does not validate the id.
func (id ID) Kind() Kind {
	k, _, _ := strings.Cut(string(id), "_")
	return Kind(k)
}

func (id ID) String() string { return string(id) }
