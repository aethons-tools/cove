package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/preview"
)

// Studio previews (docs/usage/jam/collaboration.md#previews) ride the admin
// listener: its TLS config carries the previews' wildcard cert beside the
// admin cert (picked by SNI), and hostRouter sends preview and login hosts to
// the preview handler, everything else to the admin API.

// adminTLSConfig serves adminCert by default and previewCert to clients whose
// SNI it covers (crypto/tls picks the first certificate matching the name).
func adminTLSConfig(adminCert, previewCert tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{adminCert, previewCert},
	}
}

// hostRouter dispatches a request on its Host: a preview host or the login
// host under domain goes to previews, anything else to rest.
func hostRouter(domain string, previews, rest http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if preview.IsPreviewHost(r.Host, domain) {
			previews.ServeHTTP(w, r)
			return
		}
		rest.ServeHTTP(w, r)
	})
}

// loadPreviewCert loads the previews' cert/key pair and checks its leaf covers
// both a preview host and the login host under domain, so a wrong cert fails
// serve at startup rather than every viewer's handshake. Errors name the files
// and hosts, never key material.
func loadPreviewCert(certFile, keyFile, domain string) (tls.Certificate, error) {
	c, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("previews.tls (%s, %s): %w", certFile, keyFile, err)
	}
	leaf := c.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
			return tls.Certificate{}, fmt.Errorf("previews.tls.cert %s: %w", certFile, err)
		}
	}
	sample := "x--" + strings.Repeat("a", 26) + "." + domain
	for _, h := range []string{sample, preview.LoginHost(domain)} {
		if leaf.VerifyHostname(h) != nil {
			return tls.Certificate{}, fmt.Errorf("previews.tls.cert %s does not cover %s (want a certificate for *.%s)", certFile, h, domain)
		}
	}
	return c, nil
}
