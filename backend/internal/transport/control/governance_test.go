package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

const governanceBase = "/api/v1/projects/" + projectID
const memberID = "66666666-6666-4666-8666-666666666666"
const memberAddBody = `{"username":"reader","role":"viewer"}`
const settingsBody = `{"name":"Updated project","timezone":"Asia/Shanghai"}`
const auditRange = "from=2026-11-01T00%3A00%3A00Z&to=2026-11-10T00%3A00%3A00Z"

type governanceStore struct {
	calls       int
	err         error
	members     []access.Member
	revision    int64
	memberInput access.MemberInput
	memberQuery access.MemberListFilter
	operation   string
	target      string
	settings    access.SettingsUpdate
	key         string
	audit       []access.AuditEntry
	filter      access.AuditFilter
	after       *access.AuditPosition
	limit       int
}

func governanceMember() access.Member {
	return access.Member{UserID: memberID, Username: "reader", DisplayName: "Reader", Role: "viewer", Disabled: true}
}

func (s *governanceStore) call(ctx context.Context, actor, project string) error {
	if actor == "" || project != projectID {
		panic("unscoped governance call")
	}
	if _, machine := access.IdentityFromContext(ctx); machine {
		panic("machine identity reached governance store")
	}
	if _, ok := ctx.Deadline(); !ok {
		panic("missing bounded dependency deadline")
	}
	s.calls++
	return s.err
}

func (s *governanceStore) ListProjectMembers(ctx context.Context, actor, project string, in access.MemberListFilter) (access.MemberPage, error) {
	s.memberQuery = in
	return access.MemberPage{Items: s.members, Revision: 7}, s.call(ctx, actor, project)
}

func (s *governanceStore) MutateProjectMember(ctx context.Context, actor, project, target, operation string, in access.MemberInput, hash string) (access.MemberResult, error) {
	if hash == "" {
		panic("missing canonical idempotency content")
	}
	s.memberInput, s.operation, s.target = in, operation, target
	return access.MemberResult{Member: governanceMember(), Revision: 8}, s.call(ctx, actor, project)
}

func (s *governanceStore) GetProjectSettings(ctx context.Context, actor, project string) (access.ProjectSettings, error) {
	return access.ProjectSettings{ProjectID: projectID, Name: "Example", Timezone: "UTC", Revision: 7}, s.call(ctx, actor, project)
}

func (s *governanceStore) UpdateProjectSettings(ctx context.Context, actor, project string, revision int64, key, hash string, in access.SettingsUpdate) (access.ProjectSettings, error) {
	if hash == "" {
		panic("missing canonical idempotency content")
	}
	s.settings, s.revision, s.key = in, revision, key
	return access.ProjectSettings{ProjectID: projectID, Name: in.Name, Timezone: in.Timezone, Revision: revision + 1}, s.call(ctx, actor, project)
}

func (s *governanceStore) ListAuditLogs(ctx context.Context, reader, project string, filter access.AuditFilter, after *access.AuditPosition, limit int) ([]access.AuditEntry, error) {
	s.filter, s.after, s.limit = filter, after, limit
	return s.audit, s.call(ctx, reader, project)
}

func governanceAPI(t *testing.T, store *governanceStore) (*API, http.Handler) {
	t.Helper()
	validator, err := contract.New()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := cursor.New([]byte(strings.Repeat("g", 32)))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Boundary: &httpapi.Boundary{Validator: validator, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Access:   &fakeAccess{}, Machines: &fakeMachines{}, Cursor: codec, Origin: "https://evia.example",
		Memberships: access.NewMemberships(store), Settings: access.NewSettings(store), AuditLogs: access.NewAuditLogs(store),
	}
	return a, a.Handler()
}

func governanceRequest(handler http.Handler, method, path, body string, change func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", "csrf")
	r.Header.Set("If-Match", `"7"`)
	r.Header.Set("Idempotency-Key", "governance-synthetic-key")
	if change != nil {
		change(r)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func governanceStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, expected %d: %s", w.Code, status, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Request-ID") == "" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("missing shared response boundary: %v", w.Header())
	}
	if status >= 400 {
		var problem wire.Problem
		if json.Unmarshal(w.Body.Bytes(), &problem) != nil || problem.Code == "" || problem.Message == "" || problem.RequestID != w.Header().Get("X-Request-ID") {
			t.Fatalf("invalid safe problem: %s", w.Body.String())
		}
	}
}

func TestGovernanceSessionOnlyAuthenticationAndCSRF(t *testing.T) {
	for _, route := range []struct{ method, path, body string }{
		{"GET", governanceBase + "/members", ""},
		{"POST", governanceBase + "/members", memberAddBody},
		{"PATCH", governanceBase + "/members/" + memberID, `{"role":"editor"}`},
		{"DELETE", governanceBase + "/members/" + memberID, ""},
		{"GET", governanceBase + "/settings", ""},
		{"PATCH", governanceBase + "/settings", settingsBody},
		{"GET", governanceBase + "/audit-logs?" + auditRange, ""},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			for _, mode := range []string{"missing cookie", "expired cookie", "bearer", "bearer and cookie", "duplicate cookie"} {
				t.Run(mode, func(t *testing.T) {
					store := &governanceStore{}
					_, handler := governanceAPI(t, store)
					w := governanceRequest(handler, route.method, route.path, route.body, func(r *http.Request) {
						switch mode {
						case "missing cookie":
							r.Header.Del("Cookie")
						case "expired cookie":
							r.Header.Set("Cookie", "evia_session=expired")
						case "bearer":
							r.Header.Del("Cookie")
							r.Header.Set("Authorization", "Bearer "+machineToken)
						case "bearer and cookie":
							r.Header.Set("Authorization", "Bearer invalid")
						case "duplicate cookie":
							r.AddCookie(&http.Cookie{Name: "evia_session", Value: "valid"})
						}
					})
					governanceStatus(t, w, 401)
					if store.calls != 0 {
						t.Fatal("unauthenticated governance access")
					}
				})
			}
			if route.method != "GET" {
				for _, mode := range []string{"missing", "wrong", "duplicate", "cross origin"} {
					t.Run("csrf "+mode, func(t *testing.T) {
						store := &governanceStore{}
						_, handler := governanceAPI(t, store)
						w := governanceRequest(handler, route.method, route.path, route.body, func(r *http.Request) {
							switch mode {
							case "missing":
								r.Header.Del("X-CSRF-Token")
							case "wrong":
								r.Header.Set("X-CSRF-Token", "incorrect")
							case "duplicate":
								r.Header.Add("X-CSRF-Token", "csrf")
							case "cross origin":
								r.Header.Set("Origin", "https://other.example")
							}
						})
						governanceStatus(t, w, 403)
						if store.calls != 0 {
							t.Fatal("mutation preceded CSRF validation")
						}
					})
				}
			}
		})
	}
}

func TestGovernanceStrictQueryIdentifiersAndLimits(t *testing.T) {
	for _, path := range []string{
		"/api/v1/projects/invalid/members", governanceBase + "/members?environment=production",
		governanceBase + "/members?limit=1&limit=2", governanceBase + "/members?limit=101",
		governanceBase + "/members?limit=200", governanceBase + "/members?limit=01",
		governanceBase + "/members?limit=0", governanceBase + "/members?limit=", governanceBase + "/members?cursor=",
		governanceBase + "/settings?environment=development", governanceBase + "/settings?unknown=%zz",
		governanceBase + "/audit-logs", governanceBase + "/audit-logs?" + auditRange + "&limit=101",
		governanceBase + "/audit-logs?" + auditRange + "&action=x&action=y",
		governanceBase + "/audit-logs?" + auditRange + "&action=Invalid",
		governanceBase + "/audit-logs?" + auditRange + "&action=" + strings.Repeat("a", 129),
		governanceBase + "/audit-logs?" + auditRange + "&actor_id=invalid",
		governanceBase + "/audit-logs?from=2026-10-01T00:00:00Z&to=2026-11-10T00:00:00Z",
		governanceBase + "/audit-logs?from=2026-11-10T00:00:00Z&to=2026-11-01T00:00:00Z",
		governanceBase + "/audit-logs?" + auditRange + "&environment=development",
		governanceBase + "/audit-logs?" + auditRange + "&cursor=invalid",
	} {
		t.Run(path, func(t *testing.T) {
			store := &governanceStore{}
			_, handler := governanceAPI(t, store)
			governanceStatus(t, governanceRequest(handler, "GET", path, "", nil), 400)
			if store.calls != 0 {
				t.Fatal("invalid query reached persistence")
			}
		})
	}
	for _, suffix := range []string{"/members", "/audit-logs?" + auditRange} {
		store := &governanceStore{}
		_, handler := governanceAPI(t, store)
		governanceStatus(t, governanceRequest(handler, "GET", governanceBase+suffix, "", nil), 200)
		if suffix == "/members" && store.memberQuery.Limit != 51 || suffix != "/members" && store.limit != 51 {
			t.Fatal("default 50 plus lookahead was not preserved")
		}
	}
}

func TestGovernanceMutationValidationBeforePersistence(t *testing.T) {
	for _, route := range []struct{ method, path, body string }{
		{"POST", governanceBase + "/members", memberAddBody},
		{"PATCH", governanceBase + "/members/" + memberID, `{"role":"editor"}`},
		{"DELETE", governanceBase + "/members/" + memberID, ""},
		{"PATCH", governanceBase + "/settings", settingsBody},
	} {
		for _, mode := range []string{"missing revision", "duplicate revision", "weak revision", "zero revision", "oversize revision", "missing key", "duplicate key", "invalid key", "unknown query"} {
			t.Run(route.method+route.path+mode, func(t *testing.T) {
				store := &governanceStore{}
				_, handler := governanceAPI(t, store)
				w := governanceRequest(handler, route.method, route.path, route.body, func(r *http.Request) {
					switch mode {
					case "missing revision":
						r.Header.Del("If-Match")
					case "duplicate revision":
						r.Header.Add("If-Match", `"7"`)
					case "weak revision":
						r.Header.Set("If-Match", `W/"7"`)
					case "zero revision":
						r.Header.Set("If-Match", `"0"`)
					case "oversize revision":
						r.Header.Set("If-Match", `"9007199254740992"`)
					case "missing key":
						r.Header.Del("Idempotency-Key")
					case "duplicate key":
						r.Header.Add("Idempotency-Key", "other")
					case "invalid key":
						r.Header.Set("Idempotency-Key", "invalid key")
					case "unknown query":
						r.URL.RawQuery = "environment=production"
					}
				})
				governanceStatus(t, w, 400)
				if store.calls != 0 {
					t.Fatal("invalid mutation headers/query reached persistence")
				}
			})
		}
	}
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/members", `{"username":"reader","role":"viewer","extra":1}`},
		{"POST", "/members", `{"username":"reader","role":"viewer","role":"owner"}`},
		{"POST", "/members", memberAddBody + `{}`},
		{"POST", "/members", `{"username":"reader","role":"admin"}`},
		{"PATCH", "/members/invalid", `{"role":"editor"}`},
		{"PATCH", "/members/" + memberID, `{"role":"viewer","username":"reader"}`},
		{"PATCH", "/members/" + memberID, `{"role":null}`},
		{"DELETE", "/members/" + memberID, `{}`},
		{"DELETE", "/members/" + memberID, " "},
		{"PATCH", "/settings", `{"name":"new","timezone":"UTC","extra":1}`},
		{"PATCH", "/settings", `{"name":"new","timezone":"UTC","timezone":"Asia/Shanghai"}`},
		{"PATCH", "/settings", settingsBody + `{}`},
		{"PATCH", "/settings", `{"name":"new"}`},
		{"PATCH", "/settings", `{"name":"new","timezone":"Local"}`},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			store := &governanceStore{}
			_, handler := governanceAPI(t, store)
			governanceStatus(t, governanceRequest(handler, tc.method, governanceBase+tc.path, tc.body, nil), 400)
			if store.calls != 0 {
				t.Fatal("invalid body reached persistence")
			}
		})
	}
	store := &governanceStore{}
	_, handler := governanceAPI(t, store)
	governanceStatus(t, governanceRequest(handler, "PATCH", governanceBase+"/settings", strings.Repeat(" ", 256*1024+1), nil), 413)
	if store.calls != 0 {
		t.Fatal("oversized body reached persistence")
	}
}

func TestGovernanceMemberMutationsAndSettingsWire(t *testing.T) {
	for _, tc := range []struct {
		method, suffix, body, operation string
		status                          int
	}{
		{"POST", "/members", memberAddBody, "add", 201},
		{"PATCH", "/members/" + memberID, `{"role":"editor"}`, "update", 200},
		{"DELETE", "/members/" + memberID, "", "remove", 200},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			store := &governanceStore{}
			_, handler := governanceAPI(t, store)
			w := governanceRequest(handler, tc.method, governanceBase+tc.suffix, tc.body, nil)
			governanceStatus(t, w, tc.status)
			var out wire.ProjectMemberResult
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.Revision != 8 || out.Member.UserID != memberID || !out.Member.Disabled || out.Member.DisplayName != "Reader" || w.Header().Get("ETag") != `"8"` {
				t.Fatal(w.Body.String(), w.Header())
			}
			if store.calls != 1 || store.operation != tc.operation || store.memberInput.Revision != 7 || store.memberInput.IdempotencyKey != "governance-synthetic-key" {
				t.Fatalf("mutation metadata lost: %+v", store)
			}
			if tc.operation == "add" && (store.memberInput.Username != "reader" || store.target != "") || tc.operation != "add" && store.target != memberID {
				t.Fatal("wrong member target")
			}
		})
	}
	store := &governanceStore{}
	_, handler := governanceAPI(t, store)
	w := governanceRequest(handler, "GET", governanceBase+"/settings", "", nil)
	governanceStatus(t, w, 200)
	var out wire.ProjectSettings
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.ProjectID != projectID || out.Revision != 7 || w.Header().Get("ETag") != `"7"` {
		t.Fatal(w.Body.String(), err)
	}
	w = governanceRequest(handler, "PATCH", governanceBase+"/settings", settingsBody, nil)
	governanceStatus(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Revision != 8 || out.Name != "Updated project" || out.Timezone != "Asia/Shanghai" || w.Header().Get("ETag") != `"8"` {
		t.Fatal(w.Body.String(), err)
	}
	if store.revision != 7 || store.key != "governance-synthetic-key" || store.settings.Timezone != "Asia/Shanghai" {
		t.Fatal("settings metadata lost")
	}
}

func TestGovernanceMembershipPaginationRevisionAndScope(t *testing.T) {
	store := &governanceStore{members: []access.Member{governanceMember(), governanceMember()}}
	a, handler := governanceAPI(t, store)
	w := governanceRequest(handler, "GET", governanceBase+"/members?limit=1", "", nil)
	governanceStatus(t, w, 200)
	var out wire.ProjectMemberList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Items) != 1 || out.NextCursor == nil || out.Revision != 7 || w.Header().Get("ETag") != `"7"` {
		t.Fatal(w.Body.String(), err)
	}
	w = governanceRequest(handler, "GET", governanceBase+"/members?cursor="+url.QueryEscape(*out.NextCursor), "", nil)
	governanceStatus(t, w, 200)
	if store.memberQuery.After != memberID || store.memberQuery.Revision != 7 || store.memberQuery.Limit != 51 {
		t.Fatalf("cursor revision/position lost: %+v", store.memberQuery)
	}
	scope := "project_members:" + userID + ":" + projectID
	for _, raw := range []string{
		`{"afterUUID":"` + memberID + `","revision":0}`,
		`{"afterUUID":"invalid","revision":7}`,
		`{"afterUUID":"` + memberID + `","revision":7,"unknown":1}`,
		`{"afterUUID":"` + memberID + `","revision":7,"revision":8}`,
		`{"afterUUID":"` + memberID + `","revision":7.5}`,
		`{"afterUUID":"` + memberID + `","revision":9007199254740992}`,
		`{"afterUUID":"` + memberID + `","revision":7} {}`,
	} {
		before := store.calls
		token := a.Cursor.Encode(scope, raw)
		governanceStatus(t, governanceRequest(handler, "GET", governanceBase+"/members?cursor="+url.QueryEscape(token), "", nil), 400)
		if store.calls != before {
			t.Fatal("invalid decoded cursor reached persistence")
		}
	}
	for _, foreign := range []string{"project_members:" + memberID + ":" + projectID, "project_members:" + userID + ":" + memberID} {
		token := a.Cursor.Encode(foreign, `{"afterUUID":"`+memberID+`","revision":7}`)
		governanceStatus(t, governanceRequest(handler, "GET", governanceBase+"/members?cursor="+url.QueryEscape(token), "", nil), 400)
	}
	store.err = fault.New(fault.Conflict, "MEMBERSHIP_REVISION_CONFLICT", "成员列表已更新")
	w = governanceRequest(handler, "GET", governanceBase+"/members?cursor="+url.QueryEscape(*out.NextCursor), "", nil)
	governanceStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "MEMBERSHIP_REVISION_CONFLICT") {
		t.Fatal(w.Body.String())
	}
}

func auditFixture(id string) access.AuditEntry {
	return access.AuditEntry{ID: id, ProjectID: projectID, ActorID: userID, ActorKind: "human", Action: "project.settings.update", ResourceKey: projectID, OccurredAt: time.Date(2026, 11, 8, 12, 0, 0, 123456000, time.UTC)}
}

func TestGovernanceAuditPaginationCanonicalFiltersAndWire(t *testing.T) {
	store := &governanceStore{audit: []access.AuditEntry{auditFixture("12"), auditFixture("11")}}
	a, handler := governanceAPI(t, store)
	path := governanceBase + "/audit-logs?" + auditRange + "&limit=1&action=project.settings.update&actor_id=" + userID
	w := governanceRequest(handler, "GET", path, "", nil)
	governanceStatus(t, w, 200)
	var out wire.ProjectAuditList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Items) != 1 || out.NextCursor == nil || out.Items[0].OccurredAt != "2026-11-08T12:00:00.123456Z" || out.Items[0].CredentialID != nil || !strings.Contains(w.Body.String(), `"credential_id":null`) {
		t.Fatal(w.Body.String(), err)
	}
	if store.limit != 2 || store.filter.Action != "project.settings.update" || store.filter.ActorID != userID || w.Header().Get("ETag") != "" {
		t.Fatal("audit filter or list semantics changed")
	}
	canonical := governanceBase + "/audit-logs?from=2026-11-01T08%3A00%3A00%2B08%3A00&to=2026-11-10T08%3A00%3A00%2B08%3A00&action=project.settings.update&actor_id=" + userID + "&cursor=" + url.QueryEscape(*out.NextCursor)
	governanceStatus(t, governanceRequest(handler, "GET", canonical, "", nil), 200)
	if store.after == nil || store.after.ID != "12" || !store.after.OccurredAt.Equal(auditFixture("12").OccurredAt) || store.filter.From.Location() != time.UTC {
		t.Fatal("audit position or canonical UTC filter lost")
	}
	for _, altered := range []string{
		strings.Replace(path, "project.settings.update", "project.create", 1),
		strings.Replace(path, "actor_id="+userID, "actor_id="+memberID, 1),
		strings.Replace(path, "2026-11-01", "2026-11-02", 1),
		strings.Replace(path, "2026-11-10", "2026-11-09", 1),
	} {
		before := store.calls
		governanceStatus(t, governanceRequest(handler, "GET", altered+"&cursor="+url.QueryEscape(*out.NextCursor), "", nil), 400)
		if store.calls != before {
			t.Fatal("cross-filter cursor reached persistence")
		}
	}
	scope := auditCursorScope(userID, projectID, store.filter)
	for _, raw := range []string{
		`{"occurred_at":"2026-11-08T12:00:00Z","id":"0"}`,
		`{"occurred_at":"2026-11-08T12:00:00Z","id":"01"}`,
		`{"occurred_at":"2026-11-08T12:00:00Z","id":"9223372036854775808"}`,
		`{"occurred_at":"2026-11-08T12:00:00.000000001Z","id":"12"}`,
		`{"occurred_at":"invalid","id":"12"}`,
		`{"occurred_at":"2026-10-31T12:00:00Z","id":"12"}`,
		`{"occurred_at":"2026-11-08T12:00:00Z","id":"12","extra":1}`,
		`{"occurred_at":"2026-11-08T12:00:00Z","id":"12","id":"13"}`,
	} {
		before := store.calls
		token := a.Cursor.Encode(scope, raw)
		governanceStatus(t, governanceRequest(handler, "GET", path+"&cursor="+url.QueryEscape(token), "", nil), 400)
		if store.calls != before {
			t.Fatal("invalid decoded audit cursor reached persistence")
		}
	}
	for _, foreign := range []string{auditCursorScope(memberID, projectID, store.filter), auditCursorScope(userID, memberID, store.filter)} {
		token := a.Cursor.Encode(foreign, `{"occurred_at":"2026-11-08T12:00:00Z","id":"12"}`)
		governanceStatus(t, governanceRequest(handler, "GET", path+"&cursor="+url.QueryEscape(token), "", nil), 400)
	}
}

func TestGovernanceSafeFailuresAndPolicyMapping(t *testing.T) {
	for _, route := range []struct{ method, suffix, body string }{
		{"GET", "/members", ""}, {"POST", "/members", memberAddBody},
		{"PATCH", "/members/" + memberID, `{"role":"editor"}`}, {"DELETE", "/members/" + memberID, ""},
		{"GET", "/settings", ""}, {"PATCH", "/settings", settingsBody},
		{"GET", "/audit-logs?" + auditRange, ""},
	} {
		for _, failure := range []struct {
			err    error
			status int
		}{
			{fault.New(fault.NotFound, "PROJECT_NOT_FOUND", "项目不可见"), 404},
			{fault.New(fault.Forbidden, "GOVERNANCE_FORBIDDEN", "无权执行此操作"), 403},
			{fault.New(fault.Conflict, "REVISION_CONFLICT", "版本冲突"), 409},
			{fault.New(fault.Conflict, "IDEMPOTENCY_CONFLICT", "幂等冲突"), 409},
			{errors.New("synthetic-private-database-details"), 500},
		} {
			t.Run(route.method+route.suffix+strconv.Itoa(failure.status), func(t *testing.T) {
				store := &governanceStore{err: failure.err}
				_, handler := governanceAPI(t, store)
				w := governanceRequest(handler, route.method, governanceBase+route.suffix, route.body, nil)
				governanceStatus(t, w, failure.status)
				if store.calls != 1 || strings.Contains(w.Body.String(), "synthetic-private-database-details") || strings.Contains(w.Body.String(), machineToken) {
					t.Fatal("incorrect failure boundary", store.calls, w.Body.String())
				}
			})
		}
	}
}

func TestGovernanceProjectPermissions(t *testing.T) {
	for _, role := range []string{"owner", "editor", "viewer"} {
		permissions := projectWire(access.Project{Role: role}).Permissions
		has := map[string]bool{}
		for _, permission := range permissions {
			has[permission] = true
		}
		if !has["settings.read"] {
			t.Fatal("human member cannot discover settings read")
		}
		for _, permission := range []string{"settings.write", "members.manage", "audit.read", "data.erasure"} {
			if has[permission] != (role == "owner") {
				t.Fatal(role, permission, permissions)
			}
		}
	}
}
