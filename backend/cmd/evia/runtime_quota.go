package main

import (
	"context"
	"sync"

	"github.com/howtomakeaname/Evia/backend/internal/platform/budget"
	"github.com/howtomakeaname/Evia/backend/internal/transport/control"
)

var _ control.QuotaRuntime = (*quotaRuntime)(nil)

// quotaRuntime is the serve-process adapter that publishes accepted quota
// overrides to the live Redis limiter. The mutex serializes concurrent owner
// writes so each ReplacePolicies call sees the complete current override set.
type quotaRuntime struct {
	limiter    *budget.Limiter
	mu         sync.Mutex
	defaultCfg budget.Policy
	overrides  map[budget.Scope]budget.Policy
}

func newQuotaRuntime(limiter *budget.Limiter, defaultCfg budget.Policy, overrides map[budget.Scope]budget.Policy) *quotaRuntime {
	copied := make(map[budget.Scope]budget.Policy, len(overrides))
	for scope, policy := range overrides {
		copied[scope] = policy
	}
	return &quotaRuntime{limiter: limiter, defaultCfg: defaultCfg, overrides: copied}
}

func (r *quotaRuntime) SnapshotPolicies(projectID string) []budget.ScopePolicy {
	return r.limiter.SnapshotPolicies(projectID)
}

// ApplyOverride publishes one scope policy and removes the existing Redis
// bucket so the next reservation initializes with the new policy instead of
// failing on a stored policy mismatch. When the delete fails, admission stays
// fail-closed until the bucket expires and bucketReset=false reports that.
func (r *quotaRuntime) ApplyOverride(ctx context.Context, scope budget.Scope, policy budget.Policy) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overrides[scope] = policy
	if err := r.limiter.ReplacePolicies(r.defaultCfg, r.overrides); err != nil {
		delete(r.overrides, scope)
		return false, err
	}
	if err := r.limiter.ResetBucket(ctx, scope.ProjectID, scope.Environment); err != nil {
		return false, nil
	}
	return true, nil
}
