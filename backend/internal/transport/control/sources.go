package control

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
)

type SourceService interface {
	CreateSource(context.Context, string, string, ingestion.SourceInput) (ingestion.Issued, error)
	GetSource(context.Context, string, string, string) (ingestion.Source, error)
	ListSources(context.Context, string, string, string, string, int) ([]ingestion.Source, error)
	UpdateSource(context.Context, string, string, string, int64, ingestion.SourceUpdate) (ingestion.Source, error)
	RotateSource(context.Context, string, string, string, string, int64) (ingestion.Issued, error)
}

func (a *API) registerSources(mux *http.ServeMux) {
	if a.Sources == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/sources"
	mux.HandleFunc("POST "+base, a.auth(true, a.createSource))
	mux.HandleFunc("GET "+base, a.auth(false, a.listSources))
	mux.HandleFunc("GET "+base+"/{source_id}", a.auth(false, a.getSource))
	mux.HandleFunc("PATCH "+base+"/{source_id}", a.auth(true, a.updateSource))
	mux.HandleFunc("POST "+base+"/{source_id}/credential-rotations", a.auth(true, a.rotateSource))
}
func (a *API) sourceProject(r *http.Request, s access.Session, write bool) (string, error) {
	p := r.PathValue("project_id")
	id := r.PathValue("source_id")
	if !catalogUUID.MatchString(p) || (id != "" && !catalogUUID.MatchString(id)) {
		return "", ingestion.Invalid("INVALID_RESOURCE_ID", "资源标识格式无效")
	}
	if e := a.Access.Authorize(r.Context(), s.UserID, p, write); e != nil {
		return "", e
	}
	return p, nil
}
func sourceWire(s ingestion.Source) map[string]any {
	return map[string]any{"id": s.ID, "name": s.Name, "environment": s.Environment, "kind": s.Kind, "write_key": s.WriteKey, "status": s.Status, "revision": s.Revision, "application_state": s.ApplicationState, "allowed_origins": s.AllowedOrigins}
}
func issuedWire(s ingestion.Issued) map[string]any {
	out := sourceWire(s.Source)
	if s.SecretOnce != "" {
		out["server_secret_once"] = s.SecretOnce
	}
	return out
}
func (a *API) createSource(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.sourceProject(r, s, true)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	var in wire.SourceInput
	if e = a.Boundary.Decode(w, r, "SourceInput", &in); e != nil {
		return e
	}
	out, e := a.Sources.CreateSource(r.Context(), s.UserID, p, ingestion.SourceInput{Name: in.Name, Environment: in.Environment, Kind: in.Kind, AllowedOrigins: in.AllowedOrigins})
	if e != nil {
		return e
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(out.Source.Revision, 10)))
	return a.Boundary.JSON(w, 201, "SourceIssued", issuedWire(out))
}
func (a *API) getSource(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.sourceProject(r, s, false)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	out, e := a.Sources.GetSource(r.Context(), s.UserID, p, r.PathValue("source_id"))
	if e != nil {
		return e
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(out.Revision, 10)))
	return a.Boundary.JSON(w, 200, "Source", sourceWire(out))
}
func (a *API) listSources(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.sourceProject(r, s, false)
	if e != nil {
		return e
	}
	q, e := catalogQuery(r, true)
	if e != nil {
		return e
	}
	limit, e := httpapi.Limit(r)
	if e != nil {
		return e
	}
	env := q.Get("environment")
	scope := strings.Join([]string{"sources", s.UserID, p, env}, ":")
	after, e := a.Cursor.Decode(scope, q.Get("cursor"))
	if e != nil || (after != "" && !catalogUUID.MatchString(after)) {
		return ingestion.Invalid("INVALID_CURSOR", "分页游标无效")
	}
	rows, e := a.Sources.ListSources(r.Context(), s.UserID, p, env, after, limit)
	if e != nil {
		return e
	}
	items := make([]map[string]any, 0, len(rows))
	var next any
	if len(rows) > limit {
		next = a.Cursor.Encode(scope, rows[limit-1].ID)
		rows = rows[:limit]
	}
	for _, row := range rows {
		items = append(items, sourceWire(row))
	}
	return a.Boundary.JSON(w, 200, "SourceList", map[string]any{"items": items, "next_cursor": next})
}
func (a *API) updateSource(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.sourceProject(r, s, true)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	if len(r.Header.Values("If-Match")) != 1 || !catalogETag.MatchString(r.Header.Get("If-Match")) {
		return ingestion.Invalid("REVISION_REQUIRED", "请提供单一带双引号的 If-Match 版本")
	}
	revision, e := httpapi.IfMatch(r)
	if e != nil {
		return e
	}
	var in wire.SourceUpdate
	if e = a.Boundary.Decode(w, r, "SourceUpdate", &in); e != nil {
		return e
	}
	var origins *[]string
	if in.AllowedOrigins != nil {
		origins = &in.AllowedOrigins
	}
	out, e := a.Sources.UpdateSource(r.Context(), s.UserID, p, r.PathValue("source_id"), revision, ingestion.SourceUpdate{Status: in.Status, AllowedOrigins: origins})
	if e != nil {
		return e
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(out.Revision, 10)))
	return a.Boundary.JSON(w, 200, "Source", sourceWire(out))
}
func (a *API) rotateSource(w http.ResponseWriter, r *http.Request, s access.Session) error {
	p, e := a.sourceProject(r, s, true)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 {
		return ingestion.Invalid("INVALID_IDEMPOTENCY_KEY", "必须提供单一幂等键")
	}
	var in wire.CredentialRotation
	if e = a.Boundary.Decode(w, r, "CredentialRotation", &in); e != nil {
		return e
	}
	out, e := a.Sources.RotateSource(r.Context(), s.UserID, p, r.PathValue("source_id"), keys[0], in.OverlapSeconds)
	if e != nil {
		return e
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(out.Source.Revision, 10)))
	return a.Boundary.JSON(w, 201, "SourceIssued", issuedWire(out))
}
