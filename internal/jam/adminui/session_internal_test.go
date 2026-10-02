package adminui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSEWriteFraming(t *testing.T) {
	cases := []struct{ name, event, id, data, want string }{
		{"plain", "ev", "a:1", "x", "id: a:1\nevent: ev\ndata: x\n\n"},
		{"no id", "ev", "", "x", "event: ev\ndata: x\n\n"},
		{"CR in data", "ev", "", "a\rb", "event: ev\ndata: a\ndata: b\n\n"},
		{"CRLF in data", "ev", "", "a\r\nb", "event: ev\ndata: a\ndata: b\n\n"},
		{"LF CR mix", "ev", "", "a\n\rb", "event: ev\ndata: a\ndata: \ndata: b\n\n"},
		{"CR in id", "ev", "a\rretry: 1", "x", "id: a retry: 1\nevent: ev\ndata: x\n\n"},
		{"CRLF in id", "ev", "a\r\nb", "x", "id: a b\nevent: ev\ndata: x\n\n"},
		{"CR in event", "e\rid: z", "", "x", "event: e id: z\ndata: x\n\n"},
		{"CRLF in event", "e\r\nf", "", "x", "event: e f\ndata: x\n\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sseWrite(rec, c.event, c.id, c.data)
			if got := rec.Body.String(); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
			if strings.Contains(strings.ReplaceAll(rec.Body.String(), "\r\n", ""), "\r") {
				t.Fatal("raw CR in output")
			}
		})
	}
}
