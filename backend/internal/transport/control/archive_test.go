package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/archiving"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type fakeArchiveService struct {
	page      archiving.Page
	manifest  archiving.Manifest
	gotFilter archiving.ListFilter
}

func (f *fakeArchiveService) ListManifests(_ context.Context, _, _ string, filter archiving.ListFilter) (archiving.Page, error) {
	f.gotFilter = filter
	return f.page, nil
}

func (f *fakeArchiveService) GetManifest(_ context.Context, _, _, manifestID string) (archiving.Manifest, error) {
	f.manifest.ManifestID = manifestID
	return f.manifest, nil
}

func archiveAPIWithService(t *testing.T, svc ArchiveService) http.Handler {
	t.Helper()
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:   &fakeAccess{}, Machines: &fakeMachines{}, Cursor: codec, Origin: "https://evia.example",
		Archives: svc,
	}
	return a.Handler()
}

func archiveRequest(handler http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	r.Header.Set("X-CSRF-Token", "csrf")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func sampleArchiveManifests() []archiving.Manifest {
	cutoff := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	completed := time.Date(2026, 8, 13, 10, 5, 0, 0, time.UTC)
	path := "/var/lib/evia/archives/33333333-3333-4333-8333-333333333333.jsonl"
	sha := strings.Repeat("a", 64)
	bytes := int64(2048)
	sweep := "55555555-5555-4555-8555-555555555555"
	return []archiving.Manifest{
		{
			ManifestID: "33333333-3333-4333-8333-333333333333", ProjectID: projectID, Environment: "production",
			RetentionDays: 90, Cutoff: cutoff, WindowStart: &start, WindowEnd: &end, RowCount: 128,
			ArtifactPath: &path, ArtifactSHA: &sha, ArtifactBytes: &bytes, Status: archiving.StatusMaterialized,
			SweepID: &sweep, CreatedAt: completed, CompletedAt: &completed,
		},
	}
}

func TestArchiveManifestsListMapsAndPassesQuery(t *testing.T) {
	svc := &fakeArchiveService{page: archiving.Page{Items: sampleArchiveManifests()}}
	handler := archiveAPIWithService(t, svc)

	w := archiveRequest(handler, "/api/v1/projects/"+projectID+"/archive-manifests?environment=production&limit=25")
	governanceStatus(t, w, http.StatusOK)
	if svc.gotFilter.Environment != "production" || svc.gotFilter.Limit != 25 {
		t.Fatalf("query mapping wrong: %+v", svc.gotFilter)
	}
	var out wire.ArchiveManifestList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Items) != 1 {
		t.Fatalf("bad manifest list: %s", w.Body.String())
	}
	first := out.Items[0]
	if first.ManifestID == "" || first.Status != "materialized" || first.ArtifactSha256 == nil ||
		first.WindowStartAt == nil || first.SweepID == nil || first.RowCount != 128 || first.RetentionDays != 90 {
		t.Fatalf("manifest mapping wrong: %+v", first)
	}
}

func TestArchiveManifestsDefaultLimitAndRejectsBadParams(t *testing.T) {
	svc := &fakeArchiveService{page: archiving.Page{Items: sampleArchiveManifests()}}
	handler := archiveAPIWithService(t, svc)

	w := archiveRequest(handler, "/api/v1/projects/"+projectID+"/archive-manifests")
	governanceStatus(t, w, http.StatusOK)
	if svc.gotFilter.Limit != 50 {
		t.Fatalf("default limit wrong: %d", svc.gotFilter.Limit)
	}
	for _, path := range []string{
		"/api/v1/projects/" + projectID + "/archive-manifests?limit=0",
		"/api/v1/projects/" + projectID + "/archive-manifests?limit=101",
		"/api/v1/projects/" + projectID + "/archive-manifests?unknown=1",
	} {
		if got := archiveRequest(handler, path); got.Code != http.StatusBadRequest {
			t.Fatalf("path %s expected 400 got %d", path, got.Code)
		}
	}
}

func TestArchiveManifestGetMapsSingleResource(t *testing.T) {
	svc := &fakeArchiveService{manifest: sampleArchiveManifests()[0]}
	handler := archiveAPIWithService(t, svc)
	w := archiveRequest(handler, "/api/v1/projects/"+projectID+"/archive-manifests/33333333-3333-4333-8333-333333333333")
	governanceStatus(t, w, http.StatusOK)
	var out wire.ArchiveManifest
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.ManifestID != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("get mapping wrong: %s", w.Body.String())
	}
	if got := archiveRequest(handler, "/api/v1/projects/"+projectID+"/archive-manifests/not-a-uuid"); got.Code != http.StatusBadRequest {
		t.Fatalf("malformed id expected 400 got %d", got.Code)
	}
}

func TestArchiveManifestsRouteAbsentWhenNotWired(t *testing.T) {
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:   &fakeAccess{}, Machines: &fakeMachines{}, Cursor: codec, Origin: "https://evia.example",
	}
	w := archiveRequest(a.Handler(), "/api/v1/projects/"+projectID+"/archive-manifests")
	governanceStatus(t, w, http.StatusNotFound)
}
