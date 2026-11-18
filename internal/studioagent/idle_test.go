package studioagent

import (
	"reflect"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

func TestControlFromPBIdleTimeout(t *testing.T) {
	for _, tc := range []struct {
		ms   uint64
		want time.Duration
	}{{90_000, 90 * time.Second}, {0, 0}} {
		got, ok := controlFromPB(&attachpb.ControlDown{Msg: &attachpb.ControlDown_IdleTimeout{IdleTimeout: &attachpb.IdleTimeout{Ms: tc.ms}}})
		want := Control{Kind: IdleTimeout, IdleTimeout: tc.want}
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("ms %d: got %+v %v, want %+v", tc.ms, got, ok, want)
		}
	}
}

func TestControlFromPBIdleDeadline(t *testing.T) {
	at := time.UnixMilli(1_790_000_000_123)
	got, ok := controlFromPB(&attachpb.ControlDown{Msg: &attachpb.ControlDown_IdleDeadline{IdleDeadline: &attachpb.IdleDeadline{At: at.UnixMilli()}}})
	if !ok || got.Kind != IdleDeadline || !got.IdleDeadline.Equal(at) {
		t.Fatalf("got %+v %v, want deadline %v", got, ok, at)
	}
	got, ok = controlFromPB(&attachpb.ControlDown{Msg: &attachpb.ControlDown_IdleDeadline{IdleDeadline: &attachpb.IdleDeadline{}}})
	if !ok || got.Kind != IdleDeadline || !got.IdleDeadline.IsZero() {
		t.Fatalf("at 0: got %+v %v, want the zero deadline (none)", got, ok)
	}
}
