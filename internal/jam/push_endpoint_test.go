package jam

import (
	"strings"
	"testing"
)

func TestValidPushEndpoint(t *testing.T) {
	for _, ok := range []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://fcm.googleapis.com:443/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/x",
		"https://eu.push.services.mozilla.com/wpush/v2/x",
		"https://web.push.apple.com/QGx",
		"https://api.push.apple.com/x",
		"https://wns2-par02p.notify.windows.com/w/?token=x",
		"https://FCM.googleapis.com/x",
	} {
		if err := ValidPushEndpoint(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"http://fcm.googleapis.com/x",
		"https://10.0.0.5/x",
		"https://[::1]/x",
		"https://127.0.0.1/x",
		"https://internal.corp/x",
		"https://fcm.googleapis.com:8443/x",
		"https://evilfcm.googleapis.com.attacker.io/x",
		"https://evilfcm.googleapis.com/x",
		"https://push.services.mozilla.com.attacker.io/x",
		"https://notify.windows.com/x",
		"https://user:pw@fcm.googleapis.com/x",
		"https:fcm.googleapis.com",
		"not a url",
		"https://fcm.googleapis.com/" + strings.Repeat("a", MaxPushEndpointLen),
	} {
		if err := ValidPushEndpoint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
