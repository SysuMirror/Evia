//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"
)

func TestQueryPinnedVersionsValidatePublishedMembershipWithoutProperties(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	draft := f.event(t, "opened")
	other := f.event(t, "closed")
	for _, op := range []string{"count", "unique"} {
		r := queryRequest(t, "trend")
		r.Subject = "uid"
		r.Metrics = []analysis.Metric{{Op: op, Accuracy: analysis.AccuracyExact}}
		r.EventVersions = []analysis.EventVersion{{EventKey: "opened", SchemaVersion: 1}}
		_, e := f.s.EnqueueQuery(ctx, f.viewer, f.project, r)
		faultCode(t, e, "QUERY_EVENT_VERSION_NOT_PUBLISHED")
	}
	if _, e := f.svc.CreateRelease(ctx, f.owner, f.project, "opened-v1", releaseInput("opened", draft.Revision)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.svc.CreateRelease(ctx, f.owner, f.project, "closed-v1", releaseInput("closed", other.Revision)); e != nil {
		t.Fatal(e)
	}
	edited := publishable()
	edited.Trigger = catalogString("An updated trigger")
	draft, e := f.svc.SaveDraft(ctx, f.owner, f.project, "opened", draft.Revision, edited)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.CreateRelease(ctx, f.owner, f.project, "opened-v2", releaseInput("opened", draft.Revision)); e != nil {
		t.Fatal(e)
	}
	for _, op := range []string{"count", "unique"} {
		t.Run(op, func(t *testing.T) {
			r := queryRequest(t, "trend")
			r.Subject = "uid"
			r.Metrics = []analysis.Metric{{Op: op, Accuracy: analysis.AccuracyExact}}
			for _, tc := range []struct {
				name, environment, project string
				versions                   []analysis.EventVersion
			}{
				{"missing version", "development", f.project, []analysis.EventVersion{{EventKey: "opened", SchemaVersion: 3}}},
				{"missing event", "development", f.project, []analysis.EventVersion{{EventKey: "missing", SchemaVersion: 1}}},
				{"foreign environment", "production", f.project, []analysis.EventVersion{{EventKey: "opened", SchemaVersion: 1}}},
				{"foreign project", "development", f.otherProject, []analysis.EventVersion{{EventKey: "opened", SchemaVersion: 1}}},
				{"later missing selection", "development", f.project, []analysis.EventVersion{{EventKey: "opened", SchemaVersion: 1}, {EventKey: "missing", SchemaVersion: 1}}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r.Environment, r.EventVersions = tc.environment, tc.versions
					_, e := f.s.EnqueueQuery(ctx, f.owner, tc.project, r)
					faultCode(t, e, "QUERY_EVENT_VERSION_NOT_PUBLISHED")
				})
			}
			for _, version := range []int64{1, 2} {
				r.Environment = "development"
				r.EventVersions = []analysis.EventVersion{{EventKey: "opened", SchemaVersion: version}, {EventKey: "closed", SchemaVersion: 1}}
				job, e := f.s.EnqueueQuery(ctx, f.viewer, f.project, r)
				if e != nil {
					t.Fatal("published historical version rejected", e)
				}
				claim, e := f.s.ClaimQuery(ctx, "versions-worker", time.Minute)
				if e != nil || claim == nil || claim.JobID != job.ID || len(claim.Request.EventVersions) != 2 || claim.Request.EventVersions[0] != r.EventVersions[0] || claim.Request.EventVersions[1] != r.EventVersions[1] {
					t.Fatal("pinned versions lost during durable normalization", claim, e)
				}
				if _, e = f.s.CancelQuery(ctx, f.viewer, f.project, job.ID); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
	var jobs int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_query_jobs`).Scan(&jobs); e != nil || jobs != 4 {
		t.Fatal("invalid version request wrote a query job", jobs, e)
	}
}
