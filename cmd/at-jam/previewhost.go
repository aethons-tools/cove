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

// adminTLSConfig presents previewCert to a client whose SNI is the previews
// domain or under it, and adminCert to every other client (no SNI, or any
// other name), so the choice never depends on certificate matching order.
func adminTLSConfig(adminCert, previewCert tls.Certificate, domain string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Served without consulting GetCertificate when the client sends no SNI.
		Certificates: []tls.Certificate{adminCert},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if hello.ServerName != "" && preview.UnderDomain(hello.ServerName, domain) {
				return &previewCert, nil
			}
			return &adminCert, nil
		},
	}
}

// hostRouter dispatches a request on its Host: domain itself or any name
// under it goes to previews (which 404s what it cannot parse), so no name
// the wildcard cert covers ever reaches the admin API; anything else to rest.
func hostRouter(domain string, previews, rest http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if preview.UnderDomain(r.Host, domain) {
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
