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

	"github.com/howtomakeaname/Evia/backend/internal/artifacts"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type fakeArtifactService struct {
	plan     artifacts.ReleasePlan
	artifact artifacts.Artifact
	err      error
	got      struct{ actor, project, release string }
}

func (f *fakeArtifactService) BuildTypeScript(_ context.Context, actor, project, releaseID string) (artifacts.ReleasePlan, artifacts.Artifact, error) {
	f.got.actor, f.got.project, f.got.release = actor, project, releaseID
	return f.plan, f.artifact, f.err
}

func artifactAPIWithService(t *testing.T, svc ArtifactService) http.Handler {
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
		Boundary:  &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:    &fakeAccess{},
		Machines:  &fakeMachines{},
		Cursor:    codec,
		Origin:    "https://evia.example",
		Artifacts: svc,
	}
	return a.Handler()
}

func artifactRequest(handler http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	r.Header.Set("X-CSRF-Token", "csrf")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func sampleArtifact(t *testing.T) (artifacts.ReleasePlan, artifacts.Artifact) {
	plan := artifacts.ReleasePlan{
		ReleaseID:    "33333333-3333-4333-8333-333333333333",
		Revision:     2,
		Environment:  "production",
		ManifestHash: strings.Repeat("b", 64),
		Events: []artifacts.EventSchema{{
			Key: "lesson_opened", SchemaVersion: 1, DisplayName: "Lesson opened",
			Schema: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []any{"lesson_id"},
				"properties": map[string]any{
					"lesson_id": map[string]any{"type": "integer"},
				},
			},
		}},
	}
	artifact, err := artifacts.Generate(plan)
	if err != nil {
		t.Fatal(err)
	}
	return plan, artifact
}

func TestTypeScriptArtifactMapsFullResponse(t *testing.T) {
	plan, artifact := sampleArtifact(t)
	svc := &fakeArtifactService{plan: plan, artifact: artifact}
	handler := artifactAPIWithService(t, svc)
	path := "/api/v1/projects/" + projectID + "/releases/33333333-3333-4333-8333-333333333333/artifacts/typescript"
	w := artifactRequest(handler, path)
	governanceStatus(t, w, http.StatusOK)
	if svc.got.release != "33333333-3333-4333-8333-333333333333" || svc.got.project != projectID {
		t.Fatalf("service scope wrong: %+v", svc.got)
	}
	var out wire.TypeScriptArtifact
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Language != "typescript" || out.LanguageVersion != artifacts.LanguageVersion ||
		out.EventCount != 1 || out.ByteSize != int64(artifact.ByteSize) || out.Checksum != artifact.Checksum {
		t.Fatalf("artifact metadata wrong: %+v", out)
	}
	if len(out.Compatibility) != 1 || out.Compatibility[0].InterfaceName != "LessonOpenedProperties" || !out.Compatibility[0].ValidIdentifier {
		t.Fatalf("compatibility mapping wrong: %+v", out.Compatibility)
	}
	if !strings.Contains(out.Source, "export interface LessonOpenedProperties {") {
		t.Fatalf("source missing generated declaration: %s", out.Source)
	}
}

func TestTypeScriptArtifactRejectsBadInputAndUnauthenticated(t *testing.T) {
	handler := artifactAPIWithService(t, &fakeArtifactService{})
	for _, path := range []string{
		"/api/v1/projects/not-a-uuid/releases/33333333-3333-4333-8333-333333333333/artifacts/typescript",
		"/api/v1/projects/" + projectID + "/releases/not-a-uuid/artifacts/typescript",
		"/api/v1/projects/" + projectID + "/releases/33333333-3333-4333-8333-333333333333/artifacts/typescript?unknown=1",
	} {
		if got := artifactRequest(handler, path); got.Code != http.StatusBadRequest {
			t.Fatalf("path %s expected 400 got %d", path, got.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID+"/releases/33333333-3333-4333-8333-333333333333/artifacts/typescript", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestTypeScriptArtifactPropagatesMissingRelease(t *testing.T) {
	svc := &fakeArtifactService{err: fault.New(fault.NotFound, "RELEASE_NOT_FOUND", "发布版本不存在或不可见")}
	handler := artifactAPIWithService(t, svc)
	w := artifactRequest(handler, "/api/v1/projects/"+projectID+"/releases/33333333-3333-4333-8333-333333333333/artifacts/typescript")
	governanceStatus(t, w, http.StatusNotFound)
}

func TestArtifactRouteAbsentWhenNotWired(t *testing.T) {
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
	w := artifactRequest(a.Handler(), "/api/v1/projects/"+projectID+"/releases/33333333-3333-4333-8333-333333333333/artifacts/typescript")
	if w.Code != http.StatusNotFound {
		t.Fatalf("unwired artifact API must not mount routes, got %d", w.Code)
	}
}
