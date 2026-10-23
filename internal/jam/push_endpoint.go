package jam

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// MaxPushEndpointLen is the longest accepted push endpoint URL, in bytes.
const MaxPushEndpointLen = 2048

// pushHosts are the Web Push services' exact hosts; pushHostSuffixes match
// their per-region/per-device subdomains.
var (
	pushHosts        = []string{"fcm.googleapis.com", "updates.push.services.mozilla.com", "web.push.apple.com"}
	pushHostSuffixes = []string{".push.services.mozilla.com", ".push.apple.com", ".notify.windows.com"}
)

// ValidPushEndpoint checks that raw is a Web Push endpoint Jam may POST to:
// an https URL of at most MaxPushEndpointLen bytes, on the default port, with
// no credentials, whose host is one of the push services (never an IP
// literal or another host — the endpoint comes from a browser, and Jam sends
// to it from the server). The subscribe route and the sender both call it.
func ValidPushEndpoint(raw string) error {
	if raw == "" || len(raw) > MaxPushEndpointLen {
		return fmt.Errorf("push endpoint: must be 1..%d bytes", MaxPushEndpointLen)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("push endpoint: not a URL")
	}
	if u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return errors.New("push endpoint: must be an https URL without credentials")
	}
	if p := u.Port(); p != "" && p != "443" {
		return errors.New("push endpoint: must use the default port")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || net.ParseIP(host) != nil {
		return errors.New("push endpoint: host must be a push service name")
	}
	for _, h := range pushHosts {
		if host == h {
			return nil
		}
	}
	for _, sfx := range pushHostSuffixes {
		if strings.HasSuffix(host, sfx) {
			return nil
		}
	}
	return fmt.Errorf("push endpoint: host %q is not a Web Push service", host)
}
