package control

import (
	"context"
	"net/http"
	"regexp"
	"strconv"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/retention"
)

// RetentionService is the persistence subset the retention HTTP surface needs.
type RetentionService interface {
	Get(ctx context.Context, actor, project, environment string) (retention.Policy, bool, error)
	Put(ctx context.Context, in retention.Input, expectedRevision int64) (retention.Policy, error)
}

// RetentionAPI shares management authentication. Retention is owner-only human
// governance: the service and its PostgreSQL transaction reject machines and
// non-owners, so routes use the session project authorizer rather than a
// machine permission grant.
type RetentionAPI struct {
	API     *API
	Service RetentionService
}

var retentionETag = regexp.MustCompile(`^"[0-9]{1,16}"$`)

// retentionRevision accepts If-Match "0" for the expect-absent create in
// addition to positive revisions used by optimistic updates.
func retentionRevision(r *http.Request) (int64, error) {
	if len(r.Header.Values("If-Match")) != 1 || !retentionETag.MatchString(r.Header.Get("If-Match")) {
		return 0, fault.New(fault.Invalid, "REVISION_REQUIRED", "请提供单一带双引号的 If-Match 版本")
	}
	raw := r.Header.Get("If-Match")
	n, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
	if err != nil || n < 0 || n > 9007199254740991 {
		return 0, fault.New(fault.Invalid, "REVISION_REQUIRED", "修订版本无效")
	}
	return n, nil
}

func (a *RetentionAPI) Register(mux *http.ServeMux) {
	if a.Service == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/retention-policy"
	mux.HandleFunc("GET "+base, a.API.projectAuth(false, a.get))
	mux.HandleFunc("PUT "+base, a.API.projectAuth(true, a.put))
}

func retentionWire(p retention.Policy, configured bool, project, environment string) wire.RetentionPolicy {
	out := wire.RetentionPolicy{ProjectID: project, Environment: environment, Revision: 0}
	if !configured {
		return out
	}
	days := int64(p.RetentionDays)
	out.RetentionDays = &days
	out.Revision = p.Revision
	createdAt := p.CreatedAt
	updatedAt := p.UpdatedAt
	out.CreatedAt = &createdAt
	out.UpdatedAt = &updatedAt
	return out
}

func (a *RetentionAPI) get(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	q, err := governanceQuery(r, "environment")
	if err != nil {
		return err
	}
	environment := q.Get("environment")
	// environment is a contractually required query parameter; governanceQuery
	// only rejects unknown parameters, so enforce presence explicitly here.
	if environment == "" {
		return fault.New(fault.Invalid, "INVALID_QUERY", "查询参数不符合接口定义")
	}
	policy, configured, err := a.Service.Get(r.Context(), actor.ID, project, environment)
	if err != nil {
		return err
	}
	out := retentionWire(policy, configured, project, environment)
	machineETag(w, out.Revision)
	return a.API.Boundary.JSON(w, http.StatusOK, "RetentionPolicy", out)
}

func (a *RetentionAPI) put(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	if _, err = governanceQuery(r); err != nil {
		return err
	}
	expected, err := retentionRevision(r)
	if err != nil {
		return err
	}
	key, err := savedIdempotency(r)
	if err != nil {
		return err
	}
	var body wire.RetentionPolicyInput
	if err = a.API.Boundary.Decode(w, r, "RetentionPolicyInput", &body); err != nil {
		return err
	}
	hash, err := canonical.Hash(body)
	if err != nil {
		return err
	}
	in := retention.Input{
		ProjectID:      project,
		ActorID:        actor.ID,
		Environment:    body.Environment,
		RetentionDays:  int(body.RetentionDays),
		IdempotencyKey: key,
		RequestHash:    hash,
	}
	policy, err := a.Service.Put(r.Context(), in, expected)
	if err != nil {
		return err
	}
	out := retentionWire(policy, true, project, body.Environment)
	machineETag(w, out.Revision)
	return a.API.Boundary.JSON(w, http.StatusOK, "RetentionPolicy", out)
}
