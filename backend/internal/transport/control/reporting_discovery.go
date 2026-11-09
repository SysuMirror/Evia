package control

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

// DiscoveryAPI uses the shared session, CSRF and contract boundary. Validation
// remains available when the runtime has no configured execution engine.
type DiscoveryAPI struct {
	API     *API
	Service reporting.DiscoveryService
}

func (a *DiscoveryAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}"
	mux.HandleFunc("GET "+base+"/analysis-capabilities", a.API.projectAuth(false, a.capabilities))
	mux.HandleFunc("GET "+base+"/analysis-catalog", a.API.projectAuth(false, a.catalog))
	mux.HandleFunc("POST "+base+"/query-validations", a.API.projectAuth(true, a.validate))
}
func (a *DiscoveryAPI) capabilities(w http.ResponseWriter, r *http.Request, session authorizedActor) error {
	project, e := a.API.catalogProject(r, session, false)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	out, e := a.Service.Capabilities(r.Context(), session.ID, project)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "AnalysisCapabilities", out)
}

type discoveryCatalogResponse struct {
	Environment string                   `json:"environment"`
	Revision    int64                    `json:"catalog_revision"`
	Items       []reporting.CatalogEvent `json:"items"`
	NextCursor  *string                  `json:"next_cursor"`
}

func (a *DiscoveryAPI) catalog(w http.ResponseWriter, r *http.Request, session authorizedActor) error {
	project, e := a.API.catalogProject(r, session, false)
	if e != nil {
		return e
	}
	q, e := catalogQuery(r, true)
	if e != nil {
		return e
	}
	environment := q.Get("environment")
	if environment == "" {
		return analysis.Invalid("INVALID_ENVIRONMENT", "请选择环境", "/environment")
	}
	limit, e := httpapi.Limit(r)
	if e != nil {
		return e
	}
	scope := strings.Join([]string{"analysis_catalog", session.ID, project, environment}, ":")
	position, e := a.API.Cursor.Decode(scope, q.Get("cursor"))
	if e != nil {
		return analysis.Invalid("INVALID_CURSOR", "分页游标已失效，请重新加载", "/cursor")
	}
	afterKey, afterVersion, e := reporting.ParseCatalogPosition(position)
	if e != nil {
		return e
	}
	snapshot, e := a.Service.Catalog(r.Context(), session.ID, project, environment, afterKey, afterVersion, limit)
	if e != nil {
		return e
	}
	out := discoveryCatalogResponse{Environment: environment, Revision: snapshot.Revision, Items: []reporting.CatalogEvent{}}
	if len(snapshot.Items) > limit {
		next := a.API.Cursor.Encode(scope, reporting.CatalogPosition(snapshot.Items[limit-1].Event))
		out.NextCursor = &next
		snapshot.Items = snapshot.Items[:limit]
	}
	for _, item := range snapshot.Items {
		out.Items = append(out.Items, item.Event)
	}
	return a.API.Boundary.JSON(w, 200, "AnalysisCatalog", out)
}
func (a *DiscoveryAPI) validate(w http.ResponseWriter, r *http.Request, session authorizedActor) error {
	project, e := a.API.catalogProject(r, session, false)
	if e != nil {
		return e
	}
	if _, e = catalogQuery(r, false); e != nil {
		return e
	}
	var raw json.RawMessage
	if e = a.API.Boundary.Decode(w, r, "QueryRequest", &raw); e != nil {
		return e
	}
	out, e := a.Service.ValidateQuery(r.Context(), session.ID, project, raw)
	if e != nil {
		return e
	}
	return a.API.Boundary.JSON(w, 200, "QueryValidation", out)
}
