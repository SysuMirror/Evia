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

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/overview"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type overviewServiceStub struct {
	calls   int
	actor   string
	request overview.Request
	result  overview.Result
	err     error
}

func (s *overviewServiceStub) Get(_ context.Context, actor string, request overview.Request) (overview.Result, error) {
	s.calls++
	s.actor = actor
	s.request = request
	return s.result, s.err
}

func overviewHandler(t *testing.T, service OverviewService, machines MachineService) http.Handler {
	t.Helper()
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	return (&API{Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: &fakeAccess{}, Machines: machines, Overview: service, Origin: "https://evia.example"}).Handler()
}

func overviewPath(suffix string) string {
	return "/api/v1/projects/" + projectID + "/overview?environment=development&from=2026-11-21T00%3A00%3A00Z&to=2026-11-22T00%3A00%3A00Z" + suffix
}
func overviewSessionRequest(handler http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestOverviewTransportMapsBoundedAggregate(t *testing.T) {
	watermark := time.Date(2026, 11, 21, 23, 0, 0, 123, time.UTC)
	service := &overviewServiceStub{result: overview.Result{ProjectID: projectID, Environment: "development", From: time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC), Interval: "hour", TotalEvents: 4, UniqueSessions: 2, IdentifiedUsers: 2, AnonymousIDs: 3, DataWatermark: &watermark, Trend: []overview.TrendPoint{{Bucket: time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC), Events: 4, Sessions: 2}}, TopEvents: []overview.TopEvent{{EventKey: "sys.page_view", Count: 4}}, Metadata: overview.Metadata{Accuracy: "exact", Coverage: "provisional", KnownGaps: []string{"pipeline_completeness_not_established"}, GeneratedAt: time.Date(2026, 11, 22, 0, 0, 1, 0, time.UTC)}}}
	w := overviewSessionRequest(overviewHandler(t, service, &fakeMachines{}), overviewPath(""))
	governanceStatus(t, w, http.StatusOK)
	var out wire.ProjectOverview
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if service.calls != 1 || service.request.Interval != "" || out.TotalEvents != 4 || out.UniqueSessions != 2 || len(out.Trend) != 1 || len(out.TopEvents) != 1 || out.DataWatermark == nil || out.Metadata.Accuracy != "exact" {
		t.Fatalf("mapping mismatch: calls=%d request=%+v out=%+v", service.calls, service.request, out)
	}
}

func TestOverviewTransportRejectsMalformedRangeBeforeService(t *testing.T) {
	for _, path := range []string{
		"/api/v1/projects/" + projectID + "/overview",
		overviewPath("&unknown=1"),
		overviewPath("&environment=production"),
		overviewPath("&from=bad"),
		"/api/v1/projects/not-a-uuid/overview?environment=development&from=2026-11-21T00%3A00%3A00Z&to=2026-11-22T00%3A00%3A00Z",
	} {
		service := &overviewServiceStub{}
		w := overviewSessionRequest(overviewHandler(t, service, &fakeMachines{}), path)
		if w.Code != http.StatusBadRequest || service.calls != 0 {
			t.Fatalf("%s got %d calls=%d body=%s", path, w.Code, service.calls, w.Body.String())
		}
	}
}

func TestOverviewMachineRequiresExactQueriesRead(t *testing.T) {
	for _, tc := range []struct {
		permissions []string
		want        int
	}{{[]string{access.PermissionQueriesRead}, 200}, {[]string{access.PermissionQueriesRun}, 403}, {nil, 403}} {
		service := &overviewServiceStub{result: overview.Result{ProjectID: projectID, Environment: "development", From: time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC), Interval: "hour", Trend: []overview.TrendPoint{}, TopEvents: []overview.TopEvent{}, Metadata: overview.Metadata{Accuracy: "exact", Coverage: "provisional", KnownGaps: []string{}, GeneratedAt: time.Now().UTC()}}}
		machines := &scopedMachineAuthentication{permissions: tc.permissions}
		r := httptest.NewRequest(http.MethodGet, overviewPath(""), nil)
		r.Header.Set("Authorization", "Bearer "+machineToken)
		w := httptest.NewRecorder()
		overviewHandler(t, service, machines).ServeHTTP(w, r)
		if w.Code != tc.want || (service.calls == 1) != (tc.want == 200) {
			t.Fatalf("permissions=%v got=%d calls=%d body=%s", tc.permissions, w.Code, service.calls, w.Body.String())
		}
	}
}

func TestOverviewRouteAbsentWhenAnalysisDisabled(t *testing.T) {
	w := overviewSessionRequest(overviewHandler(t, nil, &fakeMachines{}), overviewPath(""))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "ENDPOINT_NOT_FOUND") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}
