//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func sourceService(t *testing.T, f catalogFixture) *ingestion.SourceService {
	t.Helper()
	s, e := ingestion.NewSourceService(f.s, bytes.Repeat([]byte{7}, 32), nil)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func sourceInput(kind string) ingestion.SourceInput {
	origins := []string{}
	if kind == "browser" {
		origins = []string{"https://website.example"}
	}
	return ingestion.SourceInput{Name: "Website", Environment: "development", Kind: kind, AllowedOrigins: origins}
}
func TestSourceAuthorizationScopeRevisionsAndAudit(t *testing.T) {
	f := catalogDB(t)
	svc := sourceService(t, f)
	ctx := context.Background()
	created, e := svc.CreateSource(ctx, f.owner, f.project, sourceInput("browser"))
	if e != nil {
		t.Fatal(e)
	}
	if created.SecretOnce != "" || created.Source.Revision != 1 || created.Source.ApplicationState != "pending" {
		t.Fatal("invalid browser source", created)
	}
	_, e = svc.GetSource(ctx, f.outsider, f.project, created.Source.ID)
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = svc.GetSource(ctx, f.owner, f.otherProject, created.Source.ID)
	faultCode(t, e, "SOURCE_NOT_FOUND")
	_, e = svc.CreateSource(ctx, f.viewer, f.project, sourceInput("server"))
	faultCode(t, e, "PROJECT_FORBIDDEN")
	_, e = svc.UpdateSource(ctx, f.viewer, f.project, created.Source.ID, 1, ingestion.SourceUpdate{Status: sourceString("disabled")})
	faultCode(t, e, "PROJECT_FORBIDDEN")
	if rows, e := svc.ListSources(ctx, f.viewer, f.project, "production", "", 10); e != nil || len(rows) != 0 {
		t.Fatal("environment scope leaked", rows, e)
	}
	if rows, e := svc.ListSources(ctx, f.viewer, f.project, "development", "", 10); e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
	if rows, e := svc.ListSources(ctx, f.owner, f.project, "", created.Source.ID, 10); e != nil || len(rows) != 0 {
		t.Fatal("cursor repeated source", rows, e)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			_, e := svc.UpdateSource(ctx, f.owner, f.project, created.Source.ID, 1, ingestion.SourceUpdate{Status: sourceString("disabled")})
			if e == nil {
				successes.Add(1)
			} else {
				faultCode(t, e, "SOURCE_REVISION_CONFLICT")
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("stale writes were admitted", successes.Load())
	}
	source, e := f.s.ResolveSource(ctx, created.Source.WriteKey)
	if e != nil || source.Source.Status != "disabled" {
		t.Fatal("source disable not persisted", source, e)
	}
	var audit int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE resource_key=$1 AND action IN ('source.created','source.updated')`, created.Source.ID).Scan(&audit); e != nil || audit != 2 {
		t.Fatal("audit not atomic", audit, e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE ingestion_sources SET environment='production' WHERE id=$1`, created.Source.ID); e == nil {
		t.Fatal("source environment relocated")
	}
}
func sourceString(s string) *string { return &s }
func TestSourceSecretRotationIdempotencyAndOverlap(t *testing.T) {
	f := catalogDB(t)
	svc := sourceService(t, f)
	ctx := context.Background()
	issued, e := svc.CreateSource(ctx, f.owner, f.project, sourceInput("server"))
	if e != nil {
		t.Fatal(e)
	}
	if len(issued.SecretOnce) != 43 {
		t.Fatal("server secret not issued")
	}
	resolved, e := f.s.ResolveSource(ctx, issued.Source.WriteKey)
	if e != nil {
		t.Fatal(e)
	}
	if resolved.Credential.SecretDigest != ingestion.Digest(issued.SecretOnce) || bytes.Contains(resolved.Credential.SecretCiphertext, []byte(issued.SecretOnce)) {
		t.Fatal("credential not protected at rest")
	}
	plain, e := svc.DecryptCredential(resolved.Credential)
	if e != nil || plain != issued.SecretOnce {
		t.Fatal("AEAD credential cannot be recovered", e)
	}
	tampered := resolved.Credential
	tampered.SourceID = ids.New()
	if _, e = svc.DecryptCredential(tampered); e == nil {
		t.Fatal("ciphertext could be moved to another source")
	}
	rotated, e := svc.RotateSource(ctx, f.owner, f.project, issued.Source.ID, "rotation-1", 0)
	if e != nil {
		t.Fatal(e)
	}
	if rotated.Source.WriteKey == issued.Source.WriteKey || rotated.SecretOnce == issued.SecretOnce || rotated.Source.Revision != 2 {
		t.Fatal("rotation reused credential")
	}
	_, e = f.s.ResolveSource(ctx, issued.Source.WriteKey)
	faultCode(t, e, "SOURCE_CREDENTIAL_INVALID")
	replayed, e := svc.RotateSource(ctx, f.owner, f.project, issued.Source.ID, "rotation-1", 0)
	if e != nil || replayed.SecretOnce != rotated.SecretOnce || replayed.Source.WriteKey != rotated.Source.WriteKey || replayed.Source.Revision != 2 {
		t.Fatal("idempotency failed", e)
	}
	_, e = svc.RotateSource(ctx, f.owner, f.project, issued.Source.ID, "rotation-1", 1)
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	_, e = svc.RotateSource(ctx, f.viewer, f.project, issued.Source.ID, "rotation-denied", 0)
	faultCode(t, e, "PROJECT_FORBIDDEN")
	_, e = svc.RotateSource(ctx, f.owner, f.otherProject, issued.Source.ID, "rotation-denied", 0)
	faultCode(t, e, "SOURCE_NOT_FOUND")
	overlapped, e := svc.RotateSource(ctx, f.owner, f.project, issued.Source.ID, "rotation-2", 60)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.ResolveSource(ctx, rotated.Source.WriteKey); e != nil {
		t.Fatal("overlap lost prior key", e)
	}
	if _, e = f.s.ResolveSource(ctx, overlapped.Source.WriteKey); e != nil {
		t.Fatal("new key missing", e)
	}
	if _, e = f.s.ResolveSource(ctx, issued.Source.WriteKey); e == nil {
		t.Fatal("later overlap revived revoked old key")
	}
	var count int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_credentials WHERE source_id=$1`, issued.Source.ID).Scan(&count); e != nil || count != 3 {
		t.Fatal("idempotent replay created credentials", count, e)
	}
}
func TestConcurrentSourceRotationHasOneIdempotentResult(t *testing.T) {
	f := catalogDB(t)
	svc := sourceService(t, f)
	ctx := context.Background()
	created, e := svc.CreateSource(ctx, f.owner, f.project, sourceInput("server"))
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan ingestion.Issued, 10)
	for i := 0; i < 10; i++ {
		wg.Go(func() {
			r, e := svc.RotateSource(ctx, f.owner, f.project, created.Source.ID, "same-key", 0)
			if e != nil {
				t.Error(e)
				return
			}
			results <- r
		})
	}
	wg.Wait()
	close(results)
	var expected string
	for r := range results {
		if expected == "" {
			expected = r.SecretOnce
		}
		if r.SecretOnce != expected || r.Source.Revision != 2 {
			t.Fatal("concurrent idempotent result differed")
		}
	}
	var count int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_credentials WHERE source_id=$1`, created.Source.ID).Scan(&count); e != nil || count != 2 {
		t.Fatal("concurrent replay created extra credentials", count, e)
	}
}
func TestSourceApplicationStateAndCrossInstanceRevocationBound(t *testing.T) {
	f := catalogDB(t)
	svc := sourceService(t, f)
	ctx := context.Background()
	created, e := svc.CreateSource(ctx, f.owner, f.project, sourceInput("browser"))
	if e != nil {
		t.Fatal(e)
	}
	cache := ingestion.NewCachedSources(f.s)
	if _, e = cache.ResolveSource(ctx, created.Source.WriteKey); e != nil {
		t.Fatal(e)
	}
	updated, e := svc.UpdateSource(ctx, f.owner, f.project, created.Source.ID, 1, ingestion.SourceUpdate{Status: sourceString("disabled")})
	if e != nil || updated.ApplicationState != "pending" {
		t.Fatal(updated, e)
	}
	observed, e := svc.GetSource(ctx, f.owner, f.project, created.Source.ID)
	if e != nil || observed.ApplicationState != "pending" {
		t.Fatal("first postcommit observation must remain pending", observed, e)
	}
	// A separate process has no local invalidation. Its fixed two-second cache
	// must expire before the management read reports the configuration applied.
	timer := time.NewTimer(2100 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	observed, e = svc.GetSource(ctx, f.owner, f.project, created.Source.ID)
	if e != nil || observed.ApplicationState != "applied" {
		t.Fatal("bounded distribution never became applied", observed, e)
	}
	resolved, e := cache.ResolveSource(ctx, created.Source.WriteKey)
	if e != nil || resolved.Source.Status != "disabled" {
		t.Fatal("stale cache outlived applied bound", resolved, e)
	}
}

type sourceQueueEvidence struct{ records []ingestion.Record }

func (q *sourceQueueEvidence) Publish(_ context.Context, r []ingestion.Record) error {
	q.records = append(q.records, r...)
	return nil
}

type sourceBudgetEvidence struct{}

func (sourceBudgetEvidence) ReserveCollection(context.Context, string, string, int) error { return nil }
func TestSourceCollectorUsesCommittedScopeAndReadyActivation(t *testing.T) {
	f := catalogDB(t)
	svc := sourceService(t, f)
	ctx := context.Background()
	created, e := svc.CreateSource(ctx, f.owner, f.project, sourceInput("browser"))
	if e != nil {
		t.Fatal(e)
	}
	d := f.event(t, "lesson_opened")
	release, e := f.svc.CreateRelease(ctx, f.owner, f.project, "source-admission-release", releaseInput("lesson_opened", d.Revision))
	if e != nil {
		t.Fatal(e)
	}
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	queue := &sourceQueueEvidence{}
	collector, e := ingestion.NewCollector(ingestion.NewCachedSources(f.s), f.s, queue, sourceBudgetEvidence{}, v)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(map[string]any{"event_id": ids.New(), "event_key": "lesson_opened", "schema_version": 1, "release_revision": release.Revision, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "identity": map[string]any{"uid": "website-user"}, "context": map[string]any{}, "sdk": map[string]any{"name": "integration-test", "version": "1"}, "properties": map[string]any{"lesson_id": 1}})
	if e != nil {
		t.Fatal(e)
	}
	batch := ingestion.Batch{ID: ids.New(), WriteKey: created.Source.WriteKey, SentAt: time.Now().UTC(), Events: []json.RawMessage{raw}}
	_, _, e = collector.Collect(ctx, batch, ingestion.Authentication{Origin: "https://website.example"})
	faultCode(t, e, "EVENT_PLAN_NOT_READY")
	if len(queue.records) != 0 {
		t.Fatal("metadata build falsely acknowledged collection")
	}
	// Simulate a delivery adapter's completed readiness/activation in this
	// isolated schema. This is admission-policy evidence, not broker evidence.
	if _, e = f.s.Pool.Exec(ctx, `UPDATE catalog_release_delivery SET ingestion_ready=true WHERE release_id=$1`, release.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE catalog_environments SET active_revision=$1 WHERE project_id=$2 AND environment='development'`, release.Revision, f.project); e != nil {
		t.Fatal(e)
	}
	_, status, e := collector.Collect(ctx, batch, ingestion.Authentication{Origin: "https://website.example"})
	if e != nil || status != 202 || len(queue.records) != 1 || queue.records[0].ProjectID != f.project || queue.records[0].Environment != "development" {
		t.Fatal("verified scope not bound to durable record", status, e, queue.records)
	}
	production := sourceInput("browser")
	production.Environment = "production"
	other, e := svc.CreateSource(ctx, f.owner, f.project, production)
	if e != nil {
		t.Fatal(e)
	}
	batch.WriteKey = other.Source.WriteKey
	result, status, e := collector.Collect(ctx, batch, ingestion.Authentication{Origin: "https://website.example"})
	if e != nil || status != 422 || result.Results[0].Error.Code != "SCHEMA_NOT_PUBLISHED" || len(queue.records) != 1 {
		t.Fatal("source crossed environment release boundary", status, e, result)
	}
	server1, e := svc.CreateSource(ctx, f.owner, f.project, sourceInput("server"))
	if e != nil {
		t.Fatal(e)
	}
	server2, e := svc.CreateSource(ctx, f.owner, f.otherProject, sourceInput("server"))
	if e != nil {
		t.Fatal(e)
	}
	batch.WriteKey = server2.Source.WriteKey
	_, _, e = collector.Collect(ctx, batch, ingestion.Authentication{Bearer: server1.SecretOnce})
	faultCode(t, e, "SOURCE_CREDENTIAL_INVALID")
}

func TestSourceMutationRollsBackCredentialAndAuditFailures(t *testing.T) {
	f := catalogDB(t)
	svc := sourceService(t, f)
	ctx := context.Background()
	if _, e := f.s.Pool.Exec(ctx, `CREATE FUNCTION reject_source_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END $$; CREATE TRIGGER reject_source_audit BEFORE INSERT ON audit_entries FOR EACH ROW WHEN (NEW.action='source.created') EXECUTE FUNCTION reject_source_audit()`); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.CreateSource(ctx, f.owner, f.project, sourceInput("server")); e == nil {
		t.Fatal("source creation ignored audit failure")
	}
	for _, table := range []string{"ingestion_sources", "ingestion_credentials"} {
		var count int
		if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); e != nil || count != 0 {
			t.Fatal("failed source transaction left metadata or credential", table, count, e)
		}
	}
}

func TestSourceExpiredCacheDoesNotSurviveDatabaseFailure(t *testing.T) {
	f := catalogDB(t)
	svc := sourceService(t, f)
	ctx := context.Background()
	created, e := svc.CreateSource(ctx, f.owner, f.project, sourceInput("browser"))
	if e != nil {
		t.Fatal(e)
	}
	cache := ingestion.NewCachedSources(f.s)
	if _, e = cache.ResolveSource(ctx, created.Source.WriteKey); e != nil {
		t.Fatal(e)
	}
	f.s.Pool.Close()
	timer := time.NewTimer(2100 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if _, e = cache.ResolveSource(ctx, created.Source.WriteKey); e == nil {
		t.Fatal("expired authorization was served while its database was unavailable")
	}
}
