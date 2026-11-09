package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type MembershipService interface {
	List(context.Context, string, string, access.MemberListFilter) (access.MemberPage, error)
	Add(context.Context, string, string, access.MemberInput) (access.MemberResult, error)
	Update(context.Context, string, string, string, access.MemberInput) (access.MemberResult, error)
	Remove(context.Context, string, string, string, access.MemberInput) (access.MemberResult, error)
}

type SettingsService interface {
	Get(context.Context, string, string) (access.ProjectSettings, error)
	Update(context.Context, string, string, int64, string, access.SettingsUpdate) (access.ProjectSettings, error)
}

type AuditService interface {
	List(context.Context, string, string, access.AuditFilter, *access.AuditPosition, int) ([]access.AuditEntry, error)
}

func (a *API) registerGovernance(mux *http.ServeMux) {
	const base = "/api/v1/projects/{project_id}"
	if a.Memberships != nil {
		mux.HandleFunc("GET "+base+"/members", a.auth(false, a.listMembers))
		mux.HandleFunc("POST "+base+"/members", a.auth(true, a.addMember))
		mux.HandleFunc("PATCH "+base+"/members/{user_id}", a.auth(true, a.updateMember))
		mux.HandleFunc("DELETE "+base+"/members/{user_id}", a.auth(true, a.removeMember))
	}
	if a.Settings != nil {
		mux.HandleFunc("GET "+base+"/settings", a.auth(false, a.getSettings))
		mux.HandleFunc("PATCH "+base+"/settings", a.auth(true, a.updateSettings))
	}
	if a.AuditLogs != nil {
		mux.HandleFunc("GET "+base+"/audit-logs", a.auth(false, a.listAuditLogs))
	}
}

// Authorization belongs to the service's persistence transaction, after all
// transport input is validated. A prior membership lookup is never authority.
func governanceProject(r *http.Request) (string, error) {
	project := r.PathValue("project_id")
	if !catalogUUID.MatchString(project) {
		return "", fault.New(fault.Invalid, "INVALID_PROJECT_ID", "项目标识格式不正确")
	}
	return project, nil
}

func governanceQuery(r *http.Request, allowed ...string) (url.Values, error) {
	if len(r.URL.RawQuery) > 8192 {
		return nil, fault.New(fault.Invalid, "INVALID_QUERY", "查询参数过长")
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, fault.New(fault.Invalid, "INVALID_QUERY", "查询参数编码无效")
	}
	for key, values := range q {
		valid := false
		for _, name := range allowed {
			valid = valid || key == name
		}
		if !valid || len(values) != 1 || values[0] == "" {
			return nil, fault.New(fault.Invalid, "INVALID_QUERY", "查询参数不符合接口定义")
		}
	}
	return q, nil
}

func governanceLimit(q url.Values) (int, error) {
	value, ok := q["limit"]
	if !ok {
		return 50, nil
	}
	limit, err := strconv.Atoi(value[0])
	if err != nil || limit < 1 || limit > 100 || strconv.Itoa(limit) != value[0] {
		return 0, fault.New(fault.Invalid, "INVALID_LIMIT", "分页数量必须在 1 到 100 之间")
	}
	return limit, nil
}

func governanceWriteHeaders(r *http.Request) (int64, string, error) {
	revision, err := machineRevision(r)
	if err != nil {
		return 0, "", err
	}
	key, err := savedIdempotency(r)
	return revision, key, err
}

func governanceNoBody(r *http.Request) error {
	var probe [1]byte
	n, err := r.Body.Read(probe[:])
	if n != 0 || err != io.EOF {
		return fault.New(fault.Invalid, "INVALID_BODY", "此操作不接受请求正文")
	}
	return nil
}

func governanceCursorError() error {
	return fault.New(fault.Invalid, "INVALID_CURSOR", "分页游标已失效，请重新加载")
}

func governanceDecodePosition(raw string, target any) error {
	if _, err := contract.Decode([]byte(raw)); err != nil {
		return governanceCursorError()
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return governanceCursorError()
	}
	return nil
}

func governanceTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
