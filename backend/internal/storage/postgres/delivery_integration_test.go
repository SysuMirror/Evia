//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
)

// This port fixture proves PostgreSQL policy and atomicity only. Actual broker,
// worker capability and ClickHouse readback are tested in pipeline integration.
type policyProbe struct {
	calls   atomic.Int32
	mu      sync.Mutex
	last    delivery.Proof
	failure error
}

func (p *policyProbe) Verify(_ context.Context, r delivery.ProbeRequest) (delivery.Proof, error) {
	p.calls.Add(1)
	proof := delivery.Proof{ProbeRequest: r, Capability: delivery.Capability, ObservedAt: time.Now().UTC()}
	p.mu.Lock()
	p.last = proof
	p.mu.Unlock()
	return proof, p.failure
}
func deliveryService(t *testing.T, f catalogFixture, p delivery.Probe) *delivery.Service {
	t.Helper()
	v, e := contract.New()
	if e != nil {
		t.Fatal(e)
	}
	s, e := delivery.New(f.s, p, v, delivery.Options{})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func activationInput(r catalog.Release, expected int64) delivery.ActivationInput {
	return delivery.ActivationInput{Environment: r.Environment, ReleaseID: r.ID, ExpectedActiveRevision: expected}
}
func TestDeliveryActivationCASIdempotencyCompatibilityAndLeases(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	d := f.event(t, "lesson_opened")
	r, e := f.svc.CreateRelease(ctx, f.owner, f.project, "release-1", releaseInput("lesson_opened", d.Revision))
	if e != nil {
		t.Fatal(e)
	}
	probe := &policyProbe{}
	s := deliveryService(t, f, probe)
	_, e = s.Activate(ctx, f.viewer, f.project, "denied", activationInput(r, 0))
	faultCode(t, e, "PROJECT_FORBIDDEN")
	_, e = s.Activate(ctx, f.outsider, f.project, "denied", activationInput(r, 0))
	faultCode(t, e, "PROJECT_NOT_FOUND")
	_, e = s.Activate(ctx, f.owner, f.otherProject, "denied", activationInput(r, 0))
	faultCode(t, e, "RELEASE_NOT_FOUND")
	if probe.calls.Load() != 0 {
		t.Fatal("unauthorized activation issued pipeline probe")
	}
	// Newly built releases can become ready independently of activation.
	candidates, e := f.s.ListReadiness(ctx, 32)
	if e != nil || len(candidates) != 1 {
		t.Fatal(candidates, e)
	}
	if e = s.Renew(ctx, candidates[0]); e != nil {
		t.Fatal(e)
	}
	plan, e := f.s.ResolvePlan(ctx, f.project, "development", r.Revision)
	if e != nil || !plan.Ready || plan.Active {
		t.Fatal("readiness alone activated release", plan, e)
	}
	activated, e := s.Activate(ctx, f.owner, f.project, "activate-1", activationInput(r, 0))
	if e != nil || activated.ActiveReleaseRevision != 1 {
		t.Fatal(activated, e)
	}
	calls := probe.calls.Load()
	replay, e := s.Activate(ctx, f.owner, f.project, "activate-1", activationInput(r, 0))
	if e != nil || replay != activated || probe.calls.Load() != calls {
		t.Fatal("idempotent replay repeated side effects", replay, e)
	}
	_, e = s.Activate(ctx, f.owner, f.project, "activate-1", activationInput(r, 1))
	faultCode(t, e, "IDEMPOTENCY_CONFLICT")
	plan, e = f.s.ResolvePlan(ctx, f.project, "development", 1)
	if e != nil || !plan.Ready || !plan.Active {
		t.Fatal(plan, e)
	}
	var originalNonce string
	if e = f.s.Pool.QueryRow(ctx, `SELECT nonce::text FROM catalog_release_readiness WHERE release_id=$1`, r.ID).Scan(&originalNonce); e != nil {
		t.Fatal(e)
	}
	probe.mu.Lock()
	used := probe.last
	probe.mu.Unlock()
	candidates, e = f.s.ListReadiness(ctx, 32)
	if e != nil {
		t.Fatal(e)
	}
	candidate, e := candidateScan(f.s.Pool.QueryRow(ctx, `SELECT `+deliveryCandidateColumns+` FROM `+deliveryCandidateTables+` WHERE r.id=$1`, r.ID))
	if e != nil {
		t.Fatal(e)
	}
	e = f.s.RenewReadiness(ctx, candidate, used, time.Minute)
	faultCode(t, e, "PIPELINE_PROOF_REPLAYED")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE catalog_release_readiness SET confirmed_at=clock_timestamp()-interval '1 hour',ready_until=clock_timestamp()-interval '1 second' WHERE release_id=$1`, r.ID); e != nil {
		t.Fatal(e)
	}
	plan, e = f.s.ResolvePlan(ctx, f.project, "development", 1)
	if e != nil || plan.Ready || !plan.Active {
		t.Fatal("stored flag bypassed expired readiness", plan, e)
	}
	if e = s.Renew(ctx, candidate); e != nil {
		t.Fatal(e)
	}
	input := releaseInput("lesson_opened", d.Revision)
	input.ExpectedActiveRevision = 1
	newer, e := f.svc.CreateRelease(ctx, f.owner, f.project, "release-2", input)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Activate(ctx, f.owner, f.project, "stale", activationInput(newer, 0))
	faultCode(t, e, "ACTIVE_REVISION_CONFLICT")
	if _, e = s.Activate(ctx, f.owner, f.project, "activate-2", activationInput(newer, 1)); e != nil {
		t.Fatal(e)
	}
	plan, e = f.s.ResolvePlan(ctx, f.project, "development", 1)
	if e != nil || !plan.Active || !plan.Ready {
		t.Fatal("previously activated compatible revision not accepted", plan, e)
	}
	_, e = s.Activate(ctx, f.owner, f.project, "rollback", activationInput(r, 2))
	faultCode(t, e, "RELEASE_REVISION_NOT_NEWER")
	if _, e = f.s.Pool.Exec(ctx, `UPDATE catalog_release_activations SET accept_until=clock_timestamp()-interval '1 second' WHERE release_id=$1`, r.ID); e != nil {
		t.Fatal(e)
	}
	plan, e = f.s.ResolvePlan(ctx, f.project, "development", 1)
	if e != nil || plan.Active {
		t.Fatal("expired compatibility revision admitted", plan, e)
	}
	var audits int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE action='catalog.release_activated'`).Scan(&audits); e != nil || audits != 2 {
		t.Fatal("non-atomic activation audit", audits, e)
	}
}
func TestDeliveryFailedProbeAndAuditRollbackLeaveInactive(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	d := f.event(t, "lesson_opened")
	r, e := f.svc.CreateRelease(ctx, f.owner, f.project, "release", releaseInput("lesson_opened", d.Revision))
	if e != nil {
		t.Fatal(e)
	}
	probe := &policyProbe{failure: errors.New("synthetic pipeline outage")}
	s := deliveryService(t, f, probe)
	_, e = s.Activate(ctx, f.owner, f.project, "activate", activationInput(r, 0))
	faultCode(t, e, "PIPELINE_NOT_READY")
	var active int64
	if e = f.s.Pool.QueryRow(ctx, `SELECT active_revision FROM catalog_environments WHERE project_id=$1 AND environment='development'`, f.project).Scan(&active); e != nil || active != 0 {
		t.Fatal(active, e)
	}
	probe.failure = nil
	if _, e = f.s.Pool.Exec(ctx, `CREATE FUNCTION reject_activation_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END $$; CREATE TRIGGER reject_activation_audit BEFORE INSERT ON audit_entries FOR EACH ROW WHEN (NEW.action='catalog.release_activated') EXECUTE FUNCTION reject_activation_audit()`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Activate(ctx, f.owner, f.project, "activate", activationInput(r, 0)); e == nil {
		t.Fatal("audit failure ignored")
	}
	for _, table := range []string{"catalog_release_readiness", "catalog_release_activations", "delivery_probe_consumptions"} {
		var n int
		if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); e != nil || n != 0 {
			t.Fatal("failed activation left operational state", table, n, e)
		}
	}
	var ready bool
	if e = f.s.Pool.QueryRow(ctx, `SELECT ingestion_ready FROM catalog_release_delivery WHERE release_id=$1`, r.ID).Scan(&ready); e != nil || ready {
		t.Fatal("failed activation reported ready", ready, e)
	}
}
func TestDeliveryConcurrentActivationHasSingleCASWinner(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	d := f.event(t, "lesson_opened")
	r, e := f.svc.CreateRelease(ctx, f.owner, f.project, "release", releaseInput("lesson_opened", d.Revision))
	if e != nil {
		t.Fatal(e)
	}
	s := deliveryService(t, f, &policyProbe{})
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			_, e := s.Activate(ctx, f.owner, f.project, "activate-"+string(rune('a'+i)), activationInput(r, 0))
			if e == nil {
				wins.Add(1)
			} else {
				faultCode(t, e, "ACTIVE_REVISION_CONFLICT")
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("concurrent stale activations won", wins.Load())
	}
}
func TestDeliveryPublicConfigurationAuthFreshnessAndOldRevision(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	d := f.event(t, "lesson_opened")
	first, e := f.svc.CreateRelease(ctx, f.owner, f.project, "release-1", releaseInput("lesson_opened", d.Revision))
	if e != nil {
		t.Fatal(e)
	}
	s := deliveryService(t, f, &policyProbe{})
	if _, e = s.Activate(ctx, f.owner, f.project, "activate-1", activationInput(first, 0)); e != nil {
		t.Fatal(e)
	}
	source, e := sourceService(t, f).CreateSource(ctx, f.owner, f.project, sourceInput(ingestion.SourceKindBrowser))
	if e != nil {
		t.Fatal(e)
	}
	auth := delivery.Authentication{Origin: "https://website.example"}
	out, e := s.PublicConfig(ctx, source.Source.WriteKey, auth, 0)
	if e != nil || out.Config.Revision != 1 || len(out.Config.Events) != 1 {
		t.Fatal(out, e)
	}
	changed := publishable()
	changed.Schema["properties"].(map[string]any)["lesson_id"] = map[string]any{"type": "string"}
	changed.ExampleProperties = map[string]any{"lesson_id": "synthetic"}
	d, e = f.svc.SaveDraft(ctx, f.owner, f.project, "lesson_opened", d.Revision, changed)
	if e != nil {
		t.Fatal(e)
	}
	in := releaseInput("lesson_opened", d.Revision)
	in.ExpectedActiveRevision = 1
	second, e := f.svc.CreateRelease(ctx, f.owner, f.project, "release-2", in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Activate(ctx, f.owner, f.project, "activate-2", activationInput(second, 1)); e != nil {
		t.Fatal(e)
	}
	out, e = s.PublicConfig(ctx, source.Source.WriteKey, auth, 1)
	if e != nil || out.Config.Revision != 1 {
		t.Fatal("incompatible schema auto-upgraded", out, e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE catalog_release_activations SET accept_until=clock_timestamp()-interval '1 second' WHERE release_id=$1`, first.ID); e != nil {
		t.Fatal(e)
	}
	_, e = s.PublicConfig(ctx, source.Source.WriteKey, auth, 1)
	faultCode(t, e, "CONFIG_COMPATIBILITY_EXPIRED")
	if _, e = sourceService(t, f).UpdateSource(ctx, f.owner, f.project, source.Source.ID, 1, ingestion.SourceUpdate{Status: sourceString(ingestion.SourceStatusDisabled)}); e != nil {
		t.Fatal(e)
	}
	_, e = s.PublicConfig(ctx, source.Source.WriteKey, auth, 0)
	faultCode(t, e, "SOURCE_DISABLED")
}

func TestDeliveryReadinessScanDoesNotStarveLaterBuilds(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	d := f.event(t, "lesson_opened")
	for _, key := range []string{"one", "two"} {
		if _, e := f.svc.CreateRelease(ctx, f.owner, f.project, key, releaseInput("lesson_opened", d.Revision)); e != nil {
			t.Fatal(e)
		}
	}
	first, e := f.s.ListReadiness(ctx, 1)
	if e != nil || len(first) != 1 {
		t.Fatal(first, e)
	}
	second, e := f.s.ListReadiness(ctx, 1)
	if e != nil || len(second) != 1 || first[0].ReleaseID == second[0].ReleaseID {
		t.Fatal("failed or in-progress oldest build starved later readiness work", first, second, e)
	}
	if remaining, e := f.s.ListReadiness(ctx, 1); e != nil || len(remaining) != 0 {
		t.Fatal("unbounded repeated probe admission", remaining, e)
	}
}
