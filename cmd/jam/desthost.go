package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"

	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/preview"
)

// Destination endpoints with a subdomain (docs/modules/ROOT/pages/usage/jam/serve.adoc,
// "Destinations domain") ride the cove-facing listener: its TLS config
// carries the destinations' wildcard cert beside the Jam cert (picked by
// SNI), and destHostRouter sends every name under the domain to the broker,
// ahead of the gRPC demux and the cove mux.

// destinationsConfig names the destinations domain and its wildcard cert
// (which must cover *.<domain>).
type destinationsConfig struct {
	Domain string    `yaml:"domain"`
	TLS    *tlsFiles `yaml:"tls"`
	// list is set when the block is a list: destination definitions, as serve
	// configs held them before the config repo did. parseServeConfig drops
	// it, and serve warns, as it always has for a stray list.
	list bool
}

// UnmarshalYAML takes a mapping as the destinations domain block, and a list
// as the old destination definitions (list set), rather than failing on it.
func (d *destinationsConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		*d = destinationsConfig{list: true}
		return nil
	}
	type plain destinationsConfig
	return n.Decode((*plain)(d))
}

// validateDestinations checks a set destinations block: domain is a lowercase
// DNS name of at least two labels; tls.cert/tls.key are set; the domain
// neither is, holds nor lies under previews.domain (a name would be both a
// preview's and an endpoint's); and the cove-facing listener's host, when the
// config names one, is not under it (its requests would all go to the
// broker). The domain itself may be the Jam's own host.
func (c serveConfig) validateDestinations() error {
	d := c.Destinations
	if d == nil {
		return nil
	}
	if !validDomain(d.Domain) {
		return fmt.Errorf("destinations.domain %q must be a lowercase DNS name of at least two labels (e.g. jam.example.com)", d.Domain)
	}
	if d.TLS == nil || d.TLS.Cert == "" || d.TLS.Key == "" {
		return fmt.Errorf("destinations.tls.cert and destinations.tls.key are required (a certificate covering *.%s)", d.Domain)
	}
	if p := c.Previews; p != nil && (preview.UnderDomain(d.Domain, p.Domain) || preview.UnderDomain(p.Domain, d.Domain)) {
		return fmt.Errorf("destinations.domain %q overlaps previews.domain %q; use domains neither of which is under the other", d.Domain, p.Domain)
	}
	if h, _, err := net.SplitHostPort(c.Listen); err == nil && h != "" && net.ParseIP(h) == nil {
		if _, under := jam.SubdomainOf(h, d.Domain); under {
			return fmt.Errorf("listen host %q is under destinations.domain %q; serve Jam at a name outside it, or at the domain itself", h, d.Domain)
		}
	}
	return nil
}

// loadDestinationsCert loads the destinations' cert/key pair and checks its
// leaf covers a subdomain of domain, so a wrong cert fails serve at startup
// rather than every client's handshake. Errors name the files, never key
// material.
func loadDestinationsCert(certFile, keyFile, domain string) (tls.Certificate, error) {
	c, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("destinations.tls (%s, %s): %w", certFile, keyFile, err)
	}
	leaf := c.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
			return tls.Certificate{}, fmt.Errorf("destinations.tls.cert %s: %w", certFile, err)
		}
	}
	if h := "x." + domain; leaf.VerifyHostname(h) != nil {
		return tls.Certificate{}, fmt.Errorf("destinations.tls.cert %s does not cover %s (want a certificate for *.%s)", certFile, h, domain)
	}
	return c, nil
}

// coveTLSConfig is the cove-facing listener's TLS config: destCert (nil: no
// destinations domain) for a client whose SNI is a name under domain, and
// jamCert for every other client (no SNI, the domain itself, or any other
// name), so the choice never depends on certificate matching order.
func coveTLSConfig(jamCert tls.Certificate, destCert *tls.Certificate, domain string) *tls.Config {
	cfg := &tls.Config{Certificates: []tls.Certificate{jamCert}, NextProtos: []string{"h2", "http/1.1"}}
	if destCert != nil {
		cfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if _, under := jam.SubdomainOf(hello.ServerName, domain); under {
				return destCert, nil
			}
			return &jamCert, nil
		}
	}
	return cfg
}

// destHostRouter sends a request whose Host is a name under the destinations
// domain to broker (which serves the subdomain's endpoints and 404s the
// rest), so it never reaches the Attach gRPC server, /connector or any other
// path Jam serves; everything else (the Jam host) to rest.
func destHostRouter(domain string, broker, rest http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, under := jam.SubdomainOf(r.Host, domain); under {
			broker.ServeHTTP(w, r)
			return
		}
		rest.ServeHTTP(w, r)
	})
}
