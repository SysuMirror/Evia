//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func TestMetricMachineExactOperationAndStoredEnvironment(t *testing.T) {
	f, d := metricFixture(t)
	ctx := context.Background()
	readerCtx, reader := queryMachine(t, f, "development", "metrics.read")
	if _, e := f.s.GetMetricDraft(readerCtx, reader.Principal.ID, f.project, d.ID); e != nil {
		t.Fatal(e)
	}
	_, e := f.s.MetricValidationSnapshot(readerCtx, reader.Principal.ID, f.project, d.ID, reporting.MetricValidate{DraftRevision: 1})
	machineForbidden(t, e)
	writerCtx, writer := queryMachine(t, f, "development", "metrics.write")
	snapshot, e := f.s.MetricValidationSnapshot(writerCtx, writer.Principal.ID, f.project, d.ID, reporting.MetricValidate{DraftRevision: 1})
	if e != nil {
		t.Fatal("write-only validation", e)
	}
	validation, e := reporting.BuildMetricValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.PublishMetric(writerCtx, writer.Principal.ID, f.project, d.ID, "denied-publish", metricPublish(d, validation))
	machineForbidden(t, e)
	_, e = f.s.GetMetricDraft(writerCtx, writer.Principal.ID, f.project, d.ID)
	machineForbidden(t, e)
	publisherCtx, publisher := queryMachine(t, f, "development", "metrics.publish")
	v, e := f.s.PublishMetric(publisherCtx, publisher.Principal.ID, f.project, d.ID, "machine-publish", metricPublish(d, validation))
	if e != nil {
		t.Fatal(e)
	}
	if v.CreatedBy != publisher.Principal.ID {
		t.Fatal("machine publication actor changed")
	}
	crossCtx, cross := queryMachine(t, f, "production", "metrics.read", "metrics.write", "metrics.publish", "metrics.run", "queries.run")
	_, e = f.s.GetMetricVersion(crossCtx, cross.Principal.ID, f.project, d.ID, 1)
	machineForbidden(t, e)
	_, e = f.s.SaveMetricDraft(crossCtx, cross.Principal.ID, f.project, d.ID, 1, metricDefinition())
	machineForbidden(t, e)
	_, e = f.s.PublishMetric(crossCtx, cross.Principal.ID, f.project, d.ID, "other-environment", metricPublish(d, validation))
	machineForbidden(t, e)
	staging, e := f.s.CreateMetric(ctx, f.owner, f.project, "machine-staging", reporting.MetricCreate{Environment: "staging", Definition: metricDefinition()})
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.GetMetric(readerCtx, reader.Principal.ID, f.project, staging.ID)
	machineForbidden(t, e)
	_, e = f.s.ListMetrics(readerCtx, reader.Principal.ID, f.project, "production", "", 10, false)
	machineForbidden(t, e)
	_, e = f.s.GetMetric(readerCtx, reader.Principal.ID, f.otherProject, d.ID)
	machineForbidden(t, e)
	// Fresh principals may receive all 24 known grants; existing grants are immutable.
	allCtx, all := queryMachine(t, f, "development", []string{"project.read", "catalog.drafts.read", "catalog.drafts.write", "catalog.releases.read", "catalog.releases.publish", "catalog.releases.activate", "sources.read", "sources.write", "sources.rotate", "queries.read", "queries.run", "queries.raw.read", "analyses.read", "analyses.write", "analyses.publish", "analyses.run", "dashboards.read", "dashboards.write", "dashboards.publish", "dashboards.run", "metrics.read", "metrics.write", "metrics.publish", "metrics.run"}...)
	if len(all.Principal.Permissions) != 24 {
		t.Fatal(len(all.Principal.Permissions))
	}
	if _, e = f.s.GetMetric(allCtx, all.Principal.ID, f.project, d.ID); e != nil {
		t.Fatal(e)
	}
	// Exact credential authorization also precedes an existing publication replay.
	if _, e = access.NewMachines(f.s).RevokePrincipal(ctx, f.owner, f.project, publisher.Principal.ID, publisher.Principal.Revision); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.PublishMetric(publisherCtx, publisher.Principal.ID, f.project, d.ID, "machine-publish", metricPublish(d, validation))
	machineForbidden(t, e)
	var credential string
	if e = f.s.Pool.QueryRow(ctx, `SELECT credential_id::text FROM audit_entries WHERE action='metric.published' AND actor_id=$1`, publisher.Principal.ID).Scan(&credential); e != nil || credential != publisher.Credential.ID {
		t.Fatal("publication audit credential", credential, e)
	}
}

func TestMetricMachineRunRequiresQueryGrantAndRawFields(t *testing.T) {
	f, v, in := metricRunFixture(t)
	for _, permissions := range [][]string{{"metrics.run"}, {"queries.run"}, {"metrics.read", "queries.run"}} {
		ctx, m := queryMachine(t, f, "development", permissions...)
		_, e := f.s.EnqueueMetricVersion(ctx, m.Principal.ID, f.project, v.MetricID, 1, "denied", in)
		machineForbidden(t, e)
	}
	ctx, m := queryMachine(t, f, "development", "metrics.run", "queries.run")
	job, e := f.s.EnqueueMetricVersion(ctx, m.Principal.ID, f.project, v.MetricID, 1, "run", in)
	if e != nil {
		t.Fatal(e)
	}
	var credential string
	if e = f.s.Pool.QueryRow(context.Background(), `SELECT credential_id::text FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&credential); e != nil || credential != m.Credential.ID {
		t.Fatal(credential, e)
	}
	raw := in
	raw.GroupBy = []analysis.Field{{Kind: "system", Key: "uid"}}
	_, e = f.s.EnqueueMetricVersion(ctx, m.Principal.ID, f.project, v.MetricID, 1, "raw-denied", raw)
	machineForbidden(t, e)
	rawCtx, rawMachine := queryMachine(t, f, "development", "metrics.run", "queries.run", "queries.raw.read")
	if _, e = f.s.EnqueueMetricVersion(rawCtx, rawMachine.Principal.ID, f.project, v.MetricID, 1, "raw", raw); e != nil {
		t.Fatal(e)
	}
	wrongCtx, wrong := queryMachine(t, f, "production", "metrics.run", "queries.run")
	_, e = f.s.EnqueueMetricVersion(wrongCtx, wrong.Principal.ID, f.project, v.MetricID, 1, "wrong-env", in)
	machineForbidden(t, e)
	if _, e = f.s.Pool.Exec(context.Background(), `UPDATE machine_credentials SET expires_at=created_at+interval '1 microsecond' WHERE id=$1`, m.Credential.ID); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.EnqueueMetricVersion(ctx, m.Principal.ID, f.project, v.MetricID, 1, "run", in)
	machineForbidden(t, e)
}

func TestMetricWorkerRetainsOriginalCredentialAcrossRotation(t *testing.T) {
	for _, boundary := range []string{"claim", "lease", "complete"} {
		t.Run(boundary, func(t *testing.T) {
			f, v, in := metricRunFixture(t)
			ctx, m := queryMachine(t, f, "development", "metrics.run", "queries.run")
			job, e := f.s.EnqueueMetricVersion(ctx, m.Principal.ID, f.project, v.MetricID, 1, "credential", in)
			if e != nil {
				t.Fatal(e)
			}
			var claim *analysis.Claim
			if boundary != "claim" {
				claim, e = f.s.ClaimQuery(context.Background(), "metric-worker", time.Minute)
				if e != nil || claim == nil {
					t.Fatal(claim, e)
				}
				if claim.CredentialID != m.Credential.ID {
					t.Fatal("lost original credential")
				}
			}
			rotated, e := access.NewMachines(f.s).RotateCredential(context.Background(), f.owner, f.project, m.Principal.ID, m.Principal.Revision, "rotate", access.MachineRotate{ExpiresAt: time.Now().UTC().Add(time.Hour), OverlapSeconds: 0})
			if e != nil {
				t.Fatal(e)
			}
			replacement, e := access.NewMachines(f.s).AuthenticateMachine(context.Background(), rotated.SecretOnce)
			if e != nil {
				t.Fatal(e)
			}
			replacementCtx := access.WithIdentity(context.Background(), replacement)
			switch boundary {
			case "claim":
				claim, e = f.s.ClaimQuery(replacementCtx, "metric-worker", time.Minute)
				if e != nil || claim != nil {
					t.Fatal(claim, e)
				}
			case "lease":
				valid, e := f.s.QueryLeaseValid(replacementCtx, *claim)
				if e != nil || valid {
					t.Fatal(valid, e)
				}
			case "complete":
				claim.CredentialID = rotated.Credential.ID
				faultCode(t, f.s.CompleteQuery(replacementCtx, *claim, queryOutcome(job.ID, claim.Request)), "QUERY_LEASE_LOST")
			}
			var state string
			if e = f.s.Pool.QueryRow(context.Background(), `SELECT state FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&state); e != nil || state != "canceled" {
				t.Fatal(state, e)
			}
		})
	}
}

func TestMetricCompiledRequestPreservesFiltersAndUnitValues(t *testing.T) {
	f, d := metricFixture(t)
	ctx := context.Background()
	definition := metricRunDefinition()
	definition.Unit = "millisecond"
	field := &analysis.Field{Kind: "property", EventKey: "lesson_opened", SchemaVersion: 1, Key: "lesson_id"}
	definition.Filter = &analysis.Filter{Op: "gte", Field: field, Value: 7}
	d, e := f.s.SaveMetricDraft(ctx, f.owner, f.project, d.ID, 1, definition)
	if e != nil {
		t.Fatal(e)
	}
	v := metricRunPublish(t, f, d)
	request := queryRequest(t, "trend")
	in := reporting.MetricExecute{TimeRange: request.TimeRange, Timezone: "UTC", Interval: "hour", Filter: &analysis.Filter{Op: "lte", Field: field, Value: 11}}
	job, e := f.s.EnqueueMetricVersion(ctx, f.viewer, f.project, v.MetricID, 1, "filters", in)
	if e != nil {
		t.Fatal(e)
	}
	var data []byte
	var bound analysis.Request
	if e = f.s.Pool.QueryRow(ctx, `SELECT request FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&data); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(data, &bound); e != nil {
		t.Fatal(e)
	}
	if bound.Filter == nil || bound.Filter.Op != "and" || len(bound.Filter.Args) != 2 || bound.Filter.Args[0].Op != "gte" || bound.Filter.Args[1].Op != "lte" || bound.Metrics[0].Op != "sum" || bound.Metrics[0].Field.ResolvedType != "integer" {
		t.Fatal(bound)
	}
	plan, e := analysis.Compile("evia", "event_facts", analysis.Scope{ProjectID: f.project, Environment: "development"}, bound)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(plan.SQL, "sumIf(") || !strings.Contains(plan.SQL, ">=") || !strings.Contains(plan.SQL, "<=") || strings.Contains(plan.SQL, "/1000") {
		t.Fatal("wrong metric plan", plan.SQL)
	}
	// Units annotate the result; changing the annotation cannot scale the query.
	definition.Unit = "second"
	template, e := reporting.MetricTemplate(definition, in)
	if e != nil {
		t.Fatal(e)
	}
	second, e := template.Bind("development", in.TimeRange, in.Timezone)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.s.catalogTx(ctx, f.owner, f.project, false)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = resolveQueryFields(ctx, tx, f.project, &second); e != nil {
		t.Fatal(e)
	}
	other, e := analysis.Compile("evia", "event_facts", analysis.Scope{ProjectID: f.project, Environment: "development"}, second)
	if e != nil || other.SQL != plan.SQL || !savedWireEqual(other.Parameters, plan.Parameters) {
		t.Fatal("unit changed query values", e)
	}
}
