//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
)

func queryMachine(t *testing.T, f catalogFixture, environment string, permissions ...string) (context.Context, access.MachineIssued) {
	t.Helper()
	machines := access.NewMachines(f.s)
	issued, e := machines.CreatePrincipal(context.Background(), f.owner, f.project, "fixture-"+environment+"-"+time.Now().UTC().Format("150405.000000000"), access.MachineCreate{Name: "Synthetic query machine", Environment: environment, Permissions: permissions, ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	identity, e := machines.AuthenticateMachine(context.Background(), issued.SecretOnce)
	if e != nil {
		t.Fatal(e)
	}
	return access.WithIdentity(context.Background(), identity), issued
}
func machineForbidden(t *testing.T, e error) {
	t.Helper()
	var problem *fault.Error
	if !errors.As(e, &problem) || problem.Kind != fault.Forbidden {
		t.Fatalf("wanted forbidden, got %v", e)
	}
}
func TestMachineQueryGrantsEnvironmentAndRawAccess(t *testing.T) {
	f := catalogDB(t)
	s := f.s
	ctx, issued := queryMachine(t, f, "development", "queries.run", "queries.read")
	actor := issued.Principal.ID
	request := queryRequest(t, "trend")
	job, e := s.EnqueueQuery(ctx, actor, f.project, request)
	if e != nil {
		t.Fatal(e)
	}
	var credential string
	var raw bool
	if e = s.Pool.QueryRow(ctx, `SELECT credential_id::text,requires_raw FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&credential, &raw); e != nil || credential != issued.Credential.ID || raw {
		t.Fatal(credential, raw, e)
	}
	if _, e = s.GetQuery(ctx, actor, f.project, job.ID); e != nil {
		t.Fatal(e)
	}
	cross := request
	cross.Environment = "production"
	_, e = s.EnqueueQuery(ctx, actor, f.project, cross)
	machineForbidden(t, e)
	rawRequest := request
	rawRequest.GroupBy = []analysis.Field{{Kind: "system", Key: "uid"}}
	_, e = s.EnqueueQuery(ctx, actor, f.project, rawRequest)
	machineForbidden(t, e)
	rawRequest = request
	rawRequest.IdentityFilter = &analysis.IdentityFilter{}
	_, e = s.EnqueueQuery(ctx, actor, f.project, rawRequest)
	machineForbidden(t, e)
	_, e = s.ValidateAnalysisQuery(ctx, actor, f.project, rawRequest)
	machineForbidden(t, e)
	readCtx, readMachine := queryMachine(t, f, "development", "queries.read")
	_, e = s.EnqueueQuery(readCtx, readMachine.Principal.ID, f.project, request)
	machineForbidden(t, e)
	runCtx, runMachine := queryMachine(t, f, "development", "queries.run")
	_, e = s.GetQuery(runCtx, runMachine.Principal.ID, f.project, job.ID)
	machineForbidden(t, e)
	own, e := s.EnqueueQuery(runCtx, runMachine.Principal.ID, f.project, request)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CancelQuery(runCtx, runMachine.Principal.ID, f.project, own.ID); e != nil {
		t.Fatal("run grant must allow own cancellation", e)
	}
	// A machine can read a granted project job but cannot cancel another actor's job.
	_, e = s.CancelQuery(ctx, actor, f.project, mustHumanQuery(t, f).ID)
	faultCode(t, e, "QUERY_CANCEL_FORBIDDEN")
	if _, e = s.CancelQuery(ctx, actor, f.project, job.ID); e != nil {
		t.Fatal(e)
	}
}
func mustHumanQuery(t *testing.T, f catalogFixture) analysis.Job {
	t.Helper()
	j, e := f.s.EnqueueQuery(context.Background(), f.owner, f.project, queryRequest(t, "trend"))
	if e != nil {
		t.Fatal(e)
	}
	return j
}
func TestMachineWorkerUsesPersistedCredentialAndRejectsRevocation(t *testing.T) {
	for _, boundary := range []string{"claim", "lease", "complete"} {
		t.Run(boundary, func(t *testing.T) {
			f := catalogDB(t)
			s := f.s
			ctx, issued := queryMachine(t, f, "development", "queries.run", "queries.read")
			request := queryRequest(t, "trend")
			job, e := s.EnqueueQuery(ctx, issued.Principal.ID, f.project, request)
			if e != nil {
				t.Fatal(e)
			}
			var claim *analysis.Claim
			if boundary != "claim" {
				claim, e = s.ClaimQuery(context.Background(), "machine-worker", time.Minute)
				if e != nil || claim == nil {
					t.Fatal(claim, e)
				}
				if claim.CredentialID != issued.Credential.ID {
					t.Fatal("claim lost credential")
				}
			}
			rotated, e := access.NewMachines(s).RotateCredential(context.Background(), f.owner, f.project, issued.Principal.ID, issued.Principal.Revision, "rotate", access.MachineRotate{ExpiresAt: time.Now().UTC().Add(time.Hour), OverlapSeconds: 0})
			if e != nil {
				t.Fatal(e)
			}
			replacement, e := access.NewMachines(s).AuthenticateMachine(context.Background(), rotated.SecretOnce)
			if e != nil {
				t.Fatal(e)
			}
			replacementCtx := access.WithIdentity(context.Background(), replacement)
			if boundary == "claim" {
				claim, e = s.ClaimQuery(replacementCtx, "machine-worker", time.Minute)
				if e != nil || claim != nil {
					t.Fatal(claim, e)
				}
			} else if boundary == "lease" {
				valid, e := s.QueryLeaseValid(replacementCtx, *claim)
				if e != nil || valid {
					t.Fatal(valid, e)
				}
			} else {
				claim.CredentialID = rotated.Credential.ID
				faultCode(t, s.CompleteQuery(replacementCtx, *claim, queryOutcome(job.ID, request)), "QUERY_LEASE_LOST")
			}
			var state string
			var result []byte
			if e = s.Pool.QueryRow(context.Background(), `SELECT state,result_json FROM analysis_query_jobs WHERE id=$1`, job.ID).Scan(&state, &result); e != nil || state != analysis.JobStateCanceled || len(result) != 0 {
				t.Fatal(state, len(result), e)
			}
			_, e = s.GetQuery(ctx, issued.Principal.ID, f.project, job.ID)
			machineForbidden(t, e)
		})
	}
}
func TestMachineQueryCurrentReadAndCompletionAuthorization(t *testing.T) {
	f := catalogDB(t)
	s := f.s
	ctx, issued := queryMachine(t, f, "development", "queries.run", "queries.read", "queries.raw.read")
	request := queryRequest(t, "events")
	job, e := s.EnqueueQuery(ctx, issued.Principal.ID, f.project, request)
	if e != nil {
		t.Fatal(e)
	}
	claim, e := s.ClaimQuery(context.Background(), "machine-worker", time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	if e = s.CompleteQuery(context.Background(), *claim, queryOutcome(job.ID, request)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.QueryResult(ctx, issued.Principal.ID, f.project, job.ID); e != nil {
		t.Fatal(e)
	}
	readCtx, reader := queryMachine(t, f, "development", "queries.read")
	_, e = s.QueryResult(readCtx, reader.Principal.ID, f.project, job.ID)
	machineForbidden(t, e)
	otherCtx, other := queryMachine(t, f, "production", "queries.read", "queries.raw.read")
	_, e = s.GetQuery(otherCtx, other.Principal.ID, f.project, job.ID)
	machineForbidden(t, e)
	if _, e = access.NewMachines(s).RevokePrincipal(context.Background(), f.owner, f.project, issued.Principal.ID, issued.Principal.Revision); e != nil {
		t.Fatal(e)
	}
	_, e = s.QueryResult(ctx, issued.Principal.ID, f.project, job.ID)
	machineForbidden(t, e)
	authorizedCtx, authorized := queryMachine(t, f, "development", "queries.read", "queries.raw.read")
	if _, e = s.QueryResult(authorizedCtx, authorized.Principal.ID, f.project, job.ID); e != nil {
		t.Fatal("completed project data follows current reader grants", e)
	}
}

func TestMachineExpiredCredentialClosesQueuedRunningAndResultAccess(t *testing.T) {
	for _, boundary := range []string{"claim", "lease", "complete", "result"} {
		t.Run(boundary, func(t *testing.T) {
			f := catalogDB(t)
			s := f.s
			ctx, issued := queryMachine(t, f, "development", "queries.run", "queries.read")
			request := queryRequest(t, "trend")
			job, e := s.EnqueueQuery(ctx, issued.Principal.ID, f.project, request)
			if e != nil {
				t.Fatal(e)
			}
			var claim *analysis.Claim
			if boundary != "claim" {
				claim, e = s.ClaimQuery(context.Background(), "expiry-worker", time.Minute)
				if e != nil || claim == nil {
					t.Fatal(claim, e)
				}
			}
			if boundary == "result" {
				if e = s.CompleteQuery(context.Background(), *claim, queryOutcome(job.ID, request)); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = s.Pool.Exec(context.Background(), `UPDATE machine_credentials SET expires_at=created_at+interval '1 microsecond' WHERE id=$1`, issued.Credential.ID); e != nil {
				t.Fatal(e)
			}
			switch boundary {
			case "claim":
				claim, e = s.ClaimQuery(context.Background(), "expiry-worker", time.Minute)
				if e != nil || claim != nil {
					t.Fatal(claim, e)
				}
			case "lease":
				valid, e := s.QueryLeaseValid(ctx, *claim)
				if e != nil || valid {
					t.Fatal(valid, e)
				}
			case "complete":
				faultCode(t, s.CompleteQuery(ctx, *claim, queryOutcome(job.ID, request)), "QUERY_LEASE_LOST")
			case "result":
				_, e = s.QueryResult(ctx, issued.Principal.ID, f.project, job.ID)
				machineForbidden(t, e)
			}
		})
	}
}
func TestMachineSavedRunScopesReplayAndDashboardAtomicFanout(t *testing.T) {
	f, d, in := dashboardFixture(t, 2)
	s := f.s
	version := dashboardPublish(t, f, d)
	ctx, issued := queryMachine(t, f, "development", "dashboards.run", "dashboards.read", "queries.run", "queries.read", "analyses.run")
	actor := issued.Principal.ID
	// Direct saved execution and dashboard fanout share the persisted credential and queue.
	child := version.Definition.Widgets[0]
	savedInput := reporting.SavedAnalysisExecute{TimeRange: in.TimeRange, Timezone: in.Timezone}
	direct, e := s.EnqueueSavedAnalysisVersion(ctx, actor, f.project, child.AnalysisID, child.AnalysisVersion, "saved", savedInput)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.EnqueueSavedAnalysisVersion(ctx, actor, f.project, child.AnalysisID, child.AnalysisVersion, "saved", savedInput)
	if e != nil || again.ID != direct.ID {
		t.Fatal(again, e)
	}
	run, e := s.EnqueueDashboardRun(ctx, actor, f.project, d.ID, version.Version, "run", in)
	if e != nil || len(run.Widgets) != 2 {
		t.Fatal(run, e)
	}
	replay, e := s.EnqueueDashboardRun(ctx, actor, f.project, d.ID, version.Version, "run", in)
	if e != nil || replay.ID != run.ID {
		t.Fatal(replay, e)
	}
	var count int
	if e = s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM analysis_query_jobs WHERE actor_id=$1 AND credential_id=$2`, actor, issued.Credential.ID).Scan(&count); e != nil || count != 3 {
		t.Fatal(count, e)
	}
	if _, e = s.GetDashboardRun(ctx, actor, f.project, run.ID); e != nil {
		t.Fatal(e)
	}
	limitedCtx, limited := queryMachine(t, f, "development", "dashboards.run", "analyses.run")
	_, e = s.EnqueueDashboardRun(limitedCtx, limited.Principal.ID, f.project, d.ID, version.Version, "denied", in)
	machineForbidden(t, e)
	_, e = s.EnqueueSavedAnalysisVersion(limitedCtx, limited.Principal.ID, f.project, child.AnalysisID, child.AnalysisVersion, "denied", savedInput)
	machineForbidden(t, e)
	if e = s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM analysis_query_jobs WHERE actor_id=$1`, limited.Principal.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("partial admission", count, e)
	}
	readCtx, reader := queryMachine(t, f, "development", "dashboards.read", "queries.read")
	if _, e = s.GetDashboardRun(readCtx, reader.Principal.ID, f.project, run.ID); e != nil {
		t.Fatal(e)
	}
	_, e = s.CancelDashboardRun(readCtx, reader.Principal.ID, f.project, run.ID)
	machineForbidden(t, e)
	otherCtx, other := queryMachine(t, f, "development", "dashboards.run", "queries.run")
	_, e = s.CancelDashboardRun(otherCtx, other.Principal.ID, f.project, run.ID)
	faultCode(t, e, "DASHBOARD_RUN_CANCEL_FORBIDDEN")
	if _, e = s.CancelDashboardRun(ctx, actor, f.project, run.ID); e != nil {
		t.Fatal(e)
	}
	// Current authorization precedes both saved and dashboard idempotent replay.
	if _, e = access.NewMachines(s).RevokeCredential(context.Background(), f.owner, f.project, actor, issued.Credential.ID, issued.Principal.Revision); e != nil {
		t.Fatal(e)
	}
	_, e = s.EnqueueSavedAnalysisVersion(ctx, actor, f.project, child.AnalysisID, child.AnalysisVersion, "saved", savedInput)
	machineForbidden(t, e)
	_, e = s.EnqueueDashboardRun(ctx, actor, f.project, d.ID, version.Version, "run", in)
	machineForbidden(t, e)
}
func TestMachineDashboardIdentityFilterDeniedBeforeReplayAndFanout(t *testing.T) {
	f, d, in := dashboardFixture(t, 2)
	s := f.s
	version := dashboardPublish(t, f, d)
	ctx, issued := queryMachine(t, f, "development", "dashboards.run", "queries.run")
	in.WidgetFilters = []reporting.DashboardWidgetFilter{{WidgetID: version.Definition.Widgets[1].ID, Filter: &analysis.Filter{Op: "eq", Field: &analysis.Field{Kind: "system", Key: "uid"}, Value: "synthetic-user"}}}
	_, e := s.EnqueueDashboardRun(ctx, issued.Principal.ID, f.project, d.ID, version.Version, "raw-denied", in)
	machineForbidden(t, e)
	var count int
	for _, table := range []string{"analysis_query_jobs", "dashboard_runs"} {
		if e = s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); e != nil || count != 0 {
			t.Fatal(table, count, e)
		}
	}

	rawCtx, rawMachine := queryMachine(t, f, "development", "dashboards.run", "queries.run", "queries.raw.read")
	run, e := s.EnqueueDashboardRun(rawCtx, rawMachine.Principal.ID, f.project, d.ID, version.Version, "raw-allowed", in)
	if e != nil {
		t.Fatal(e)
	}
	// Normal cleanup nulls child references. Stored identity filters remain raw.
	if _, e = s.Pool.Exec(context.Background(), `DELETE FROM analysis_query_jobs WHERE actor_id=$1`, rawMachine.Principal.ID); e != nil {
		t.Fatal(e)
	}
	readCtx, reader := queryMachine(t, f, "development", "dashboards.read", "queries.read")
	_, e = s.GetDashboardRun(readCtx, reader.Principal.ID, f.project, run.ID)
	machineForbidden(t, e)
}

func TestMachineReportingIndependentReadWritePublishAndEnvironmentGrants(t *testing.T) {
	f, d, in := dashboardFixture(t, 1)
	s := f.s
	writeCtx, writer := queryMachine(t, f, "development", "analyses.write", "dashboards.write")
	saved, e := s.CreateSavedAnalysis(writeCtx, writer.Principal.ID, f.project, "machine-saved", reporting.SavedAnalysisCreate{Environment: "development", Definition: savedRunDefinition()})
	if e != nil {
		t.Fatal(e)
	}
	dash, e := s.CreateDashboard(writeCtx, writer.Principal.ID, f.project, "machine-dashboard", reporting.DashboardCreate{Environment: "development", Definition: d.Definition})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveSavedAnalysisDraft(writeCtx, writer.Principal.ID, f.project, saved.ID, saved.DraftRevision, saved.Definition); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveDashboardDraft(writeCtx, writer.Principal.ID, f.project, dash.ID, dash.DraftRevision, dash.Definition, ""); e != nil {
		t.Fatal(e)
	}
	_, e = s.GetSavedAnalysisDraft(writeCtx, writer.Principal.ID, f.project, saved.ID)
	machineForbidden(t, e)
	_, e = s.GetDashboardDraft(writeCtx, writer.Principal.ID, f.project, dash.ID)
	machineForbidden(t, e)
	// A retry is still scoped to its current environment and write grant.
	_, e = s.CreateSavedAnalysis(writeCtx, writer.Principal.ID, f.project, "machine-saved", reporting.SavedAnalysisCreate{Environment: "production", Definition: savedRunDefinition()})
	machineForbidden(t, e)
	readCtx, reader := queryMachine(t, f, "development", "analyses.read", "dashboards.read", "queries.read")
	if _, e = s.ListSavedAnalyses(readCtx, reader.Principal.ID, f.project, "development", "", 10, false, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetSavedAnalysisDraft(readCtx, reader.Principal.ID, f.project, saved.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ListDashboards(readCtx, reader.Principal.ID, f.project, "development", "", 10, false, "", ""); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetDashboardDraft(readCtx, reader.Principal.ID, f.project, dash.ID); e != nil {
		t.Fatal(e)
	}
	_, e = s.ListDashboards(readCtx, reader.Principal.ID, f.project, "production", "", 10, false, "", "")
	machineForbidden(t, e)
	if e = s.AuthorizeAnalysisDiscovery(readCtx, reader.Principal.ID, f.project); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ListAnalysisCatalog(readCtx, reader.Principal.ID, f.project, "development", "", 0, 10); e != nil {
		t.Fatal(e)
	}
	_, e = s.ListAnalysisCatalog(readCtx, reader.Principal.ID, f.project, "production", "", 0, 10)
	machineForbidden(t, e)
	// Publication needs its own grant, independent of read/write scopes.
	current, e := s.GetSavedAnalysisDraft(readCtx, reader.Principal.ID, f.project, saved.ID)
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := s.SavedAnalysisValidationSnapshot(readCtx, reader.Principal.ID, f.project, saved.ID, reporting.SavedAnalysisValidate{DraftRevision: current.DraftRevision})
	if e != nil {
		t.Fatal(e)
	}
	validation, e := reporting.BuildSavedAnalysisValidation(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	publishInput := reporting.SavedAnalysisPublish{DraftRevision: current.DraftRevision, ValidationHash: validation.ValidationHash}
	_, e = s.PublishSavedAnalysis(writeCtx, writer.Principal.ID, f.project, saved.ID, "publish-denied", publishInput)
	machineForbidden(t, e)
	publishCtx, publisher := queryMachine(t, f, "development", "analyses.publish", "dashboards.publish")
	published, e := s.PublishSavedAnalysis(publishCtx, publisher.Principal.ID, f.project, saved.ID, "publish", publishInput)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetSavedAnalysisVersion(readCtx, reader.Principal.ID, f.project, saved.ID, published.Version); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ListSavedAnalysisVersions(readCtx, reader.Principal.ID, f.project, saved.ID, 0, 10); e != nil {
		t.Fatal(e)
	}
	dashCurrent, e := s.GetDashboardDraft(readCtx, reader.Principal.ID, f.project, dash.ID)
	if e != nil {
		t.Fatal(e)
	}
	dashSnapshot, e := s.DashboardValidationSnapshot(readCtx, reader.Principal.ID, f.project, dash.ID, reporting.DashboardValidate{DraftRevision: dashCurrent.DraftRevision})
	if e != nil {
		t.Fatal(e)
	}
	dashValidation, e := reporting.BuildDashboardValidation(dashSnapshot)
	if e != nil {
		t.Fatal(e)
	}
	dashPublished, e := s.PublishDashboard(publishCtx, publisher.Principal.ID, f.project, dash.ID, "publish", reporting.DashboardPublish{DraftRevision: dashCurrent.DraftRevision, ValidationHash: dashValidation.ValidationHash})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetDashboardVersion(readCtx, reader.Principal.ID, f.project, dash.ID, dashPublished.Version); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ListDashboardVersions(readCtx, reader.Principal.ID, f.project, dash.ID, 0, 10); e != nil {
		t.Fatal(e)
	}
	_, e = s.EnqueueDashboardRun(readCtx, reader.Principal.ID, f.project, dash.ID, dashPublished.Version, "run-denied", in)
	machineForbidden(t, e)
}
