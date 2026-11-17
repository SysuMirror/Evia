package control

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

const folderID = "66666666-6666-4666-8666-666666666666"
const folderBase = "/api/v1/projects/" + projectID + "/dashboard-folders"
const folderItem = folderBase + "/" + folderID

// dashboardFolderHTTPService is a transport double: persistence, hierarchy
// and emptiness rules are covered by the PostgreSQL integration tests.
type dashboardFolderHTTPService struct {
	calls                       int
	err                         error
	environment, after, key, id string
	revision                    int64
	limit                       int
	parentID, title             string
}

func (f *dashboardFolderHTTPService) call(actor, project string) error {
	if actor == "" || project != projectID {
		panic("unscoped dashboard folder service")
	}
	f.calls++
	return f.err
}
func folderResource() reporting.DashboardFolder {
	return reporting.DashboardFolder{
		ID:          folderID,
		ProjectID:   projectID,
		Environment: "development",
		Depth:       1,
		Title:       "Forum",
		Revision:    1,
		CreatedBy:   userID,
		CreatedAt:   time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC),
	}
}
func (f *dashboardFolderHTTPService) CreateDashboardFolder(_ context.Context, actor, project, key string, in reporting.DashboardFolderCreate) (reporting.DashboardFolder, error) {
	f.key, f.environment, f.parentID, f.title = key, in.Environment, in.ParentID, in.Title
	return folderResource(), f.call(actor, project)
}
func (f *dashboardFolderHTTPService) ListDashboardFolders(_ context.Context, actor, project, env, after string, limit int) ([]reporting.DashboardFolder, error) {
	f.environment, f.after, f.limit = env, after, limit
	return []reporting.DashboardFolder{folderResource()}, f.call(actor, project)
}
func (f *dashboardFolderHTTPService) GetDashboardFolder(_ context.Context, actor, project, id string) (reporting.DashboardFolder, error) {
	f.id = id
	return folderResource(), f.call(actor, project)
}
func (f *dashboardFolderHTTPService) UpdateDashboardFolder(_ context.Context, actor, project, id string, revision int64, in reporting.DashboardFolderUpdate) (reporting.DashboardFolder, error) {
	f.id, f.revision, f.title = id, revision, in.Title
	out := folderResource()
	out.Revision = revision + 1
	out.Title = in.Title
	return out, f.call(actor, project)
}
func (f *dashboardFolderHTTPService) DeleteDashboardFolder(_ context.Context, actor, project, id string) error {
	f.id = id
	return f.call(actor, project)
}

func dashboardFolderHTTP(t *testing.T, f *dashboardFolderHTTPService, access AccessService) (http.Handler, *cursor.Codec) {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	codec, e := cursor.New([]byte(strings.Repeat("s", 32)))
	if e != nil {
		t.Fatal(e)
	}
	a := &API{Boundary: &httpapi.Boundary{Validator: v, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Access: access, Cursor: codec, Origin: "https://evia.example", DashboardFolders: f}
	return a.Handler(), codec
}

func TestDashboardFolderHTTPRoutesContractsAndRevisions(t *testing.T) {
	f := &dashboardFolderHTTPService{}
	access := &catalogAccess{fakeAccess: &fakeAccess{}}
	h, _ := dashboardFolderHTTP(t, f, access)
	for _, tc := range []struct {
		method, path, body string
		headers            map[string]string
		status             int
		write              bool
	}{
		{"POST", folderBase, `{"environment":"development","title":"Forum"}`, map[string]string{"Idempotency-Key": "folder-create"}, 201, true},
		{"GET", folderBase + "?environment=development", "", nil, 200, false},
		{"GET", folderItem, "", nil, 200, false},
		{"PATCH", folderItem, `{"title":"Renamed"}`, map[string]string{"If-Match": `"1"`}, 200, true},
		{"DELETE", folderItem, "", nil, 204, true},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			before := f.calls
			w := catalogRequest(h, tc.method, tc.path, tc.body, tc.headers)
			if tc.status == 204 {
				// 204 carries no JSON body, so the shared JSON assertion does not apply.
				if w.Code != 204 || w.Body.Len() != 0 {
					t.Fatalf("status %d body %q, want empty 204", w.Code, w.Body.String())
				}
			} else {
				catalogStatus(t, w, tc.status)
			}
			if f.calls != before+1 {
				t.Fatal("missing service dispatch")
			}
			if access.writes[len(access.writes)-1] != tc.write {
				t.Fatal("incorrect role policy")
			}
		})
	}
	if f.key != "folder-create" || f.revision != 1 || f.id != folderID {
		t.Fatal("idempotency key or revision not forwarded", f)
	}
}

func TestDashboardFolderHTTPStrictInputs(t *testing.T) {
	f := &dashboardFolderHTTPService{}
	h, _ := dashboardFolderHTTP(t, f, &catalogAccess{fakeAccess: &fakeAccess{}})
	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
		status                   int
	}{
		{"list missing env", "GET", folderBase, "", nil, 400},
		{"list unknown parameter", "GET", folderBase + "?environment=development&search=x", "", nil, 400},
		{"bad folder uuid", "GET", folderBase + "/wrong", "", nil, 400},
		{"create unknown body field", "POST", folderBase, `{"environment":"development","title":"X","sort":1}`, map[string]string{"Idempotency-Key": "k"}, 400},
		{"create missing title", "POST", folderBase, `{"environment":"development"}`, map[string]string{"Idempotency-Key": "k"}, 400},
		{"patch without if-match", "PATCH", folderItem, `{"title":"X"}`, nil, 400},
		{"patch unknown body field", "PATCH", folderItem, `{"title":"X","parent_id":null}`, map[string]string{"If-Match": `"1"`}, 400},
		{"delete with query", "DELETE", folderItem + "?environment=development", "", nil, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := f.calls
			catalogStatus(t, catalogRequest(h, tc.method, tc.path, tc.body, tc.headers), tc.status)
			if f.calls != before {
				t.Fatal("invalid input reached service")
			}
		})
	}
}

func TestDashboardHTTPFolderScopeForwarding(t *testing.T) {
	f := &dashboardHTTPService{}
	access := &catalogAccess{fakeAccess: &fakeAccess{}}
	h, _ := dashboardHTTP(t, f, access)
	path := dashboardBase + "?environment=development&folder_id=" + folderID
	catalogStatus(t, catalogRequest(h, "GET", path, "", nil), 200)
	if f.folderScope != folderID {
		t.Fatalf("folder scope not forwarded: %q", f.folderScope)
	}
	catalogStatus(t, catalogRequest(h, "GET", dashboardBase+"?environment=development&folder_id=root", "", nil), 200)
	if f.folderScope != "root" {
		t.Fatalf("root scope not forwarded: %q", f.folderScope)
	}
	for _, raw := range []string{"bogus", "not-a-uuid"} {
		before := f.calls
		catalogStatus(t, catalogRequest(h, "GET", dashboardBase+"?environment=development&folder_id="+raw, "", nil), 400)
		if f.calls != before {
			t.Fatal("invalid folder scope reached service")
		}
	}
}
