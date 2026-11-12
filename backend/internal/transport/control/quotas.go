package control

import (
	"context"
	"net/http"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/budget"
	"github.com/howtomakeaname/Evia/backend/internal/platform/canonical"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/quotas"
)

// QuotaService is the durable override store the HTTP boundary needs.
type QuotaService interface {
	Get(ctx context.Context, actor, project, environment string) (quotas.Policy, bool, error)
	Put(ctx context.Context, in quotas.Input, expectedRevision int64) (quotas.Policy, bool, error)
}

// QuotaRuntime applies accepted overrides to the live Redis budget engine.
// ApplyOverride returns bucketReset=false when the existing bucket could not be
// removed, in which case admission fails closed until the bucket expires.
type QuotaRuntime interface {
	SnapshotPolicies(projectID string) []budget.ScopePolicy
	ApplyOverride(ctx context.Context, scope budget.Scope, policy budget.Policy) (bucketReset bool, err error)
}

// QuotaAPI exposes owner-only collection quota management. It is mounted only
// when collection (and therefore the runtime budget engine) is enabled.
type QuotaAPI struct {
	API     *API
	Service QuotaService
	Runtime QuotaRuntime
}

func (a *QuotaAPI) Register(mux *http.ServeMux) {
	if a.Service == nil || a.Runtime == nil {
		return
	}
	const base = "/api/v1/projects/{project_id}/quotas"
	mux.HandleFunc("GET "+base, a.API.projectAuth(false, a.get))
	mux.HandleFunc("PATCH "+base, a.API.projectAuth(true, a.patch))
}

func quotaTimePtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (a *QuotaAPI) get(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
	project, err := governanceProject(r)
	if err != nil {
		return err
	}
	q, err := governanceQuery(r, "environment")
	if err != nil {
		return err
	}
	environment := q.Get("environment")
	if environment != "development" && environment != "staging" && environment != "production" {
		return fault.New(fault.Invalid, "INVALID_QUERY", "查询参数不符合接口定义")
	}
	policy, configured, err := a.Service.Get(r.Context(), actor.ID, project, environment)
	if err != nil {
		return err
	}
	snapshot := a.Runtime.SnapshotPolicies(project)
	out := wire.CollectionQuota{
		ProjectID:     project,
		Environment:   environment,
		Override:      configured,
		RuntimeStatus: "applied",
		CreatedAt:     nil,
		UpdatedAt:     nil,
	}
	if configured {
		out.Source = "project_override"
		out.RatePerSecond = policy.Rate
		out.Burst = int64(policy.Burst)
		out.Revision = policy.Revision
		out.CreatedAt = quotaTimePtr(policy.CreatedAt)
		out.UpdatedAt = quotaTimePtr(policy.UpdatedAt)
	} else {
		out.Source = "default"
		for _, entry := range snapshot {
			if entry.Environment == environment {
				out.RatePerSecond = entry.Rate
				out.Burst = int64(entry.Burst)
				break
			}
		}
	}
	machineETag(w, out.Revision)
	return a.API.Boundary.JSON(w, http.StatusOK, "CollectionQuota", out)
}

func (a *QuotaAPI) patch(w http.ResponseWriter, r *http.Request, actor authorizedActor) error {
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
	var body wire.CollectionQuotaInput
	if err = a.API.Boundary.Decode(w, r, "CollectionQuotaInput", &body); err != nil {
		return err
	}
	hash, err := canonical.Hash(body)
	if err != nil {
		return err
	}
	policy, replayed, err := a.Service.Put(r.Context(), quotas.Input{
		ProjectID:      project,
		ActorID:        actor.ID,
		Environment:    body.Environment,
		Rate:           body.RatePerSecond,
		Burst:          int(body.Burst),
		IdempotencyKey: key,
		RequestHash:    hash,
	}, expected)
	if err != nil {
		return err
	}
	// An idempotent replay already published its policy on the original accept;
	// re-applying it could regress the limiter behind a newer accepted write.
	bucketReset := true
	if !replayed {
		scope := budget.Scope{ProjectID: project, Environment: body.Environment}
		bucketReset, err = a.Runtime.ApplyOverride(r.Context(), scope, budget.Policy{Rate: body.RatePerSecond, Burst: int(body.Burst)})
		if err != nil {
			return fault.New(fault.Unavailable, "QUOTA_RUNTIME_APPLY_FAILED", "配额已持久化但运行时策略发布失败，请检查预算依赖")
		}
	}
	out := wire.CollectionQuota{
		ProjectID:     project,
		Environment:   body.Environment,
		RatePerSecond: policy.Rate,
		Burst:         int64(policy.Burst),
		Override:      true,
		Source:        "project_override",
		Revision:      policy.Revision,
		RuntimeStatus: "applied",
		CreatedAt:     quotaTimePtr(policy.CreatedAt),
		UpdatedAt:     quotaTimePtr(policy.UpdatedAt),
	}
	if !bucketReset {
		out.RuntimeStatus = "bucket_reset_pending"
	}
	machineETag(w, out.Revision)
	return a.API.Boundary.JSON(w, http.StatusOK, "CollectionQuota", out)
}
