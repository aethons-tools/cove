package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

func TestSignRS256RoundTrips(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	header := []byte(`{"alg":"RS256","typ":"JWT"}`)
	payload := []byte(`{"iss":"123","iat":1,"exp":2}`)
	jwt, err := signRS256(header, payload, key)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 JWT segments, got %d", len(parts))
	}
	if parts[0] != b64url(header) || parts[1] != b64url(payload) {
		t.Fatal("header/payload segments not base64url of inputs")
	}
	// Verify the signature over "header.payload" with the public key.
	signingInput := parts[0] + "." + parts[1]
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := b64urlDecodeForTest(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
}

func TestParseRSAPrivateKeyPKCS1(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	got, err := parseRSAPrivateKey(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got.N.Cmp(key.N) != 0 {
		t.Fatal("parsed key modulus differs")
	}
}

func TestParseRSAPrivateKeyPKCS8(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	got, err := parseRSAPrivateKey(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got.N.Cmp(key.N) != 0 {
		t.Fatal("parsed PKCS#8 key modulus differs")
	}
}

func TestParseRSAPrivateKeyRejectsNonRSAPKCS8(t *testing.T) {
	// A PKCS#8-wrapped ECDSA key parses as PKCS#8 but is not RSA — must be rejected.
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := parseRSAPrivateKey(pemBytes); err == nil {
		t.Fatal("want error for a non-RSA (ECDSA) PKCS#8 key")
	}
}

func TestParseRSAPrivateKeyRejectsGarbage(t *testing.T) {
	if _, err := parseRSAPrivateKey([]byte("not a pem")); err == nil {
		t.Fatal("want error for non-PEM input")
	}
}

func b64urlDecodeForTest(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
