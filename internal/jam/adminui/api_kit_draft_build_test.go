package adminui_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// logBuilder builds every draft at once, writing a line to its log.
type logBuilder struct{}

func (logBuilder) BuildKitDraft(_ context.Context, _ jam.KitDefinition, tag string, log io.Writer) (string, error) {
	_, _ = io.WriteString(log, "#1 building "+tag+"\n")
	return "sha256:img", nil
}
func (logBuilder) HasKitDraftImage(string) (bool, error) { return true, nil }

func TestDraftBuildRoutes(t *testing.T) {
	store, _ := draftFixture(t)
	builds := jam.NewKitDraftBuilds(context.Background(), store, jam.OneDraftManager("local", logBuilder{}), slog.New(slog.DiscardHandler))
	h := adminui.RegisterAppForTest(store, testLogger(), nil, nil, anyCred, nil, adminui.WithKitDraftBuilds(builds))
	etag := draftETag(t, store)
	b, _ := json.Marshal(map[string]string{"etag": etag})
	var v jam.KitDraftBuild
	rec := rawDo(t, h, "POST", "/app/admin/api/kits/app/draft/build", string(b), map[string]string{"Content-Type": "application/json"})
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.ID == "" {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	builds.Wait(5 * time.Second)
	rec = rawDo(t, h, "GET", "/app/admin/api/kits/app/draft/build?since=0", "", nil)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.State != jam.DraftBuildSucceeded || !strings.Contains(v.Log, "#1 building") {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	wantErr(t, rawDo(t, h, "GET", "/app/admin/api/kits/app/draft/build?since=-1", "", nil), 400, "bad_request")
	wantErr(t, rawDo(t, h, "POST", "/app/admin/api/kits/app/draft/build/cancel", "", nil), 404, "not_found")

	// Without a builder, Build says why.
	off := adminui.RegisterAppForTest(store, testLogger(), nil, nil, anyCred, nil)
	wantErr(t, rawDo(t, off, "POST", "/app/admin/api/kits/app/draft/build", string(b), map[string]string{"Content-Type": "application/json"}), 503, "unavailable")
}
