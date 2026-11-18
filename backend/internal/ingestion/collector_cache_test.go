package ingestion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func expirePlan(t *testing.T, c *Collector, revision int64) {
	t.Helper()
	key := fmt.Sprintf("%s:development:%d", testProject, revision)
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.cache[key]
	if !ok {
		t.Fatal("expected compiled plan")
	}
	v.expires = time.Now().Add(-time.Second)
	c.cache[key] = v
}
func TestPlanStatusRefreshReusesImmutableCompilationAndFailsClosed(t *testing.T) {
	c, _, p, _, _ := collectorFixture(t)
	ctx := context.Background()
	first, e := c.plan(ctx, testProject, "development", 1)
	if e != nil {
		t.Fatal(e)
	}
	expirePlan(t, c, 1)
	second, e := c.plan(ctx, testProject, "development", 1)
	if e != nil || p.calls != 2 || first.schemas["lesson_opened"] != second.schemas["lesson_opened"] {
		t.Fatal("status refresh recompiled immutable schema", e, p.calls)
	}
	expirePlan(t, c, 1)
	p.value.Ready = false
	if _, e = c.plan(ctx, testProject, "development", 1); e == nil || fault.Public(e).Code != "EVENT_PLAN_NOT_READY" {
		t.Fatal("expired eligibility survived readiness revocation", e)
	}
	p.value.Ready = true
	p.err = errors.New("metadata unavailable")
	if _, e = c.plan(ctx, testProject, "development", 1); e == nil {
		t.Fatal("expired eligibility served stale data during outage")
	}
	p.err = nil
	third, e := c.plan(ctx, testProject, "development", 1)
	if e != nil || third.schemas["lesson_opened"] != first.schemas["lesson_opened"] {
		t.Fatal("readiness recovery recompiled retained immutable schema", e)
	}
}
func TestImmutableManifestMutationIsNotSilentlyRecompiled(t *testing.T) {
	c, _, p, _, _ := collectorFixture(t)
	if _, e := c.plan(context.Background(), testProject, "development", 1); e != nil {
		t.Fatal(e)
	}
	expirePlan(t, c, 1)
	p.value.Events = map[string]EventRule{"different": {Key: "different", Version: 2, AllowedSourceKinds: []string{SourceKindBrowser}, Schema: map[string]any{"type": "object"}}}
	for i := 0; i < 2; i++ {
		if _, e := c.plan(context.Background(), testProject, "development", 1); e == nil || fault.Public(e).Code != "EVENT_PLAN_INVALID" {
			t.Fatal("changed immutable revision was trusted", e)
		}
	}
	if p.calls != 2 {
		t.Fatal("invalid revision was fetched and recompiled again", p.calls)
	}
}

type blockingPlans struct {
	value   Plan
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (p *blockingPlans) ResolvePlan(ctx context.Context, _, _ string, r int64) (Plan, error) {
	p.calls.Add(1)
	p.started <- struct{}{}
	select {
	case <-ctx.Done():
		return Plan{}, ctx.Err()
	case <-p.release:
		out := p.value
		out.Revision = r
		return out, nil
	}
}
func TestPlanRefreshSingleFlightAndCanceledWaiter(t *testing.T) {
	c, _, p, _, _ := collectorFixture(t)
	block := &blockingPlans{value: p.value, started: make(chan struct{}, 32), release: make(chan struct{})}
	c.plans = block
	leader := make(chan error, 1)
	go func() { _, e := c.plan(context.Background(), testProject, "development", 1); leader <- e }()
	<-block.started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, e := c.plan(ctx, testProject, "development", 1); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("singleflight waiter ignored cancellation", e)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			_, e := c.plan(context.Background(), testProject, "development", 1)
			if e != nil {
				failures <- e
			}
		})
	}
	close(block.release)
	if e := <-leader; e != nil {
		t.Fatal(e)
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		t.Error(e)
	}
	if block.calls.Load() != 1 {
		t.Fatal("concurrent requests duplicated manifest fetch and compilation", block.calls.Load())
	}
}
func TestPlanMissConcurrencyIsBoundedAndCancellationReleasesSlots(t *testing.T) {
	c, _, p, _, _ := collectorFixture(t)
	block := &blockingPlans{value: p.value, started: make(chan struct{}, 32), release: make(chan struct{})}
	c.plans = block
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := int64(1); i <= 8; i++ {
		wg.Go(func() { _, _ = c.plan(ctx, testProject, "development", i) })
		<-block.started
	}
	start := time.Now()
	if _, e := c.plan(context.Background(), testProject, "development", 9); e == nil || fault.Public(e).Code != "EVENT_PLAN_BUSY" {
		t.Fatal("excess manifest work was admitted", e)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("excess work queued rather than applying backpressure")
	}
	cancel()
	wg.Wait()
	close(block.release)
	if _, e := c.plan(context.Background(), testProject, "development", 10); e != nil {
		t.Fatal("canceled misses leaked admission slots", e)
	}
}
func TestCompiledPlanEntryEvictionRequiresRecompilation(t *testing.T) {
	c, _, p, _, _ := collectorFixture(t)
	ctx := context.Background()
	first := map[int64]*compiledPlan{}
	for i := int64(1); i <= maxCachedPlans+1; i++ {
		v, e := c.plan(ctx, testProject, "development", i)
		if e != nil {
			t.Fatal(e)
		}
		first[i] = &v
	}
	c.mu.Lock()
	if len(c.cache) > maxCachedPlans {
		t.Fatal("plan cache entry bound exceeded")
	}
	var evicted int64
	for i := range first {
		if _, ok := c.cache[fmt.Sprintf("%s:development:%d", testProject, i)]; !ok {
			evicted = i
			break
		}
	}
	c.mu.Unlock()
	if evicted == 0 {
		t.Fatal("expected bounded cache eviction")
	}
	before := p.calls
	next, e := c.plan(ctx, testProject, "development", evicted)
	if e != nil || p.calls != before+1 || next.schemas["lesson_opened"] == first[evicted].schemas["lesson_opened"] {
		t.Fatal("evicted plan did not rebuild", e)
	}
}
func TestCompiledRuleCountBoundEvictsLargePlans(t *testing.T) {
	c, _, p, _, _ := collectorFixture(t)
	p.value.Events = map[string]EventRule{}
	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("event_%d", i)
		p.value.Events[key] = EventRule{Key: key, Version: 1, AllowedSourceKinds: []string{SourceKindBrowser}, Schema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	}
	for i := int64(1); i <= 22; i++ {
		if _, e := c.plan(context.Background(), testProject, "development", i); e != nil {
			t.Fatal(e)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cacheRules > maxCachedRules || len(c.cache) > 20 {
		t.Fatal("compiled event-rule memory bound exceeded", c.cacheRules, len(c.cache))
	}
}

type horizonPlans struct {
	value   Plan
	until   time.Time
	calls   int
	entered time.Time
	delay   time.Duration
}

func (p *horizonPlans) ResolvePlan(ctx context.Context, _, _ string, r int64) (Plan, error) {
	p.calls++
	p.entered = time.Now()
	if p.delay > 0 {
		timer := time.NewTimer(p.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return Plan{}, ctx.Err()
		case <-timer.C:
		}
	}
	v := p.value
	v.Revision = r
	v.ValidFor = time.Until(p.until)
	v.Ready = v.Ready && v.ValidFor > 0
	return v, nil
}
func TestPlanCacheDoesNotOutliveServerEligibilityHorizon(t *testing.T) {
	c, _, stub, _, _ := collectorFixture(t)
	p := &horizonPlans{value: stub.value, until: time.Now().Add(80 * time.Millisecond), delay: 20 * time.Millisecond}
	c.plans = p
	v, e := c.plan(context.Background(), testProject, "development", 1)
	if e != nil {
		t.Fatal(e)
	}
	if v.expires.After(p.until) || v.expires.After(p.entered.Add(80*time.Millisecond)) {
		t.Fatal("metadata latency extended known eligibility horizon", v.expires, p.until)
	}
	timer := time.NewTimer(time.Until(v.expires.Add(time.Millisecond)))
	defer timer.Stop()
	<-timer.C
	if _, e = c.plan(context.Background(), testProject, "development", 1); e == nil || p.calls != 2 {
		t.Fatal("expired eligibility was served from cache", e, p.calls)
	}
}

type delayedEnvelopeValidator struct {
	delegate SchemaValidator
	calls    int
	until    time.Time
}

func (v *delayedEnvelopeValidator) Validate(name string, value any) error {
	if name == "EventEnvelope" {
		v.calls++
		if v.calls == 2 {
			timer := time.NewTimer(time.Until(v.until.Add(time.Millisecond)))
			defer timer.Stop()
			<-timer.C
		}
	}
	return v.delegate.Validate(name, value)
}
func TestBatchCannotSubmitAfterKnownEligibilityExpires(t *testing.T) {
	c, _, stub, q, _ := collectorFixture(t)
	until := time.Now().Add(100 * time.Millisecond)
	p := &horizonPlans{value: stub.value, until: until}
	c.plans = p
	c.validator = &delayedEnvelopeValidator{delegate: c.validator, until: until}
	out, _, e := c.Collect(context.Background(), batchOf(eventJSON(validEvent()), eventJSON(validEvent())), browserAuth())
	if e == nil || fault.Public(e).Code != "EVENT_PLAN_NOT_READY" || q.calls != 0 || len(out.Results) != 0 || p.calls < 2 {
		t.Fatal("slow batch submitted an expired earlier item", e, q.calls, out, p.calls)
	}
}

func TestPropertyValidationCrossingPlanCacheDeadlineRechecksEligibility(t *testing.T) {
	for _, outcome := range []string{"ready", "unready", "unavailable", "changed manifest"} {
		t.Run(outcome, func(t *testing.T) {
			c, _, plans, queue, _ := collectorFixture(t)
			ctx := context.Background()
			compiled, e := c.plan(ctx, testProject, "development", 1)
			if e != nil {
				t.Fatal(e)
			}
			key := fmt.Sprintf("%s:development:1", testProject)
			c.mu.Lock()
			entry := c.cache[key]
			entry.expires = time.Now().Add(20 * time.Millisecond)
			c.cache[key] = entry
			c.mu.Unlock()
			validations := 0
			// Delay inside property validation, after Collect has selected a valid
			// compiled plan. Read its actual deadline so even a slow test machine
			// that refreshed the cache before validation exercises the same race.
			compiled.schemas["lesson_opened"].Format = &jsonschema.Format{
				Name: "test-cache-expiry-boundary",
				Validate: func(any) error {
					validations++
					if validations != 1 {
						return nil
					}
					c.mu.Lock()
					deadline := c.cache[key].expires
					c.mu.Unlock()
					timer := time.NewTimer(time.Until(deadline.Add(time.Millisecond)))
					defer timer.Stop()
					<-timer.C
					switch outcome {
					case "unready":
						plans.value.Ready = false
					case "unavailable":
						plans.err = errors.New("metadata unavailable")
					case "changed manifest":
						plans.value.Events = map[string]EventRule{}
					}
					return nil
				},
			}
			out, status, e := c.Collect(ctx, batchOf(eventJSON(validEvent()), eventJSON(validEvent())), browserAuth())
			if plans.calls < 2 {
				t.Fatal("expired eligibility was not refreshed", plans.calls)
			}
			if outcome != "ready" {
				if e == nil || fault.Public(e).Code != "EVENT_PLAN_NOT_READY" || queue.calls != 0 {
					t.Fatal("failed eligibility refresh admitted records", status, out, e, queue.calls)
				}
				return
			}
			if e != nil || status != 202 || len(out.Results) != 2 || queue.calls != 1 || len(queue.records) != 2 {
				t.Fatal("cache deadline rejected an eligible item", status, out, e, queue.calls, len(queue.records))
			}
			for _, item := range out.Results {
				if item.Status != "accepted" || item.Error != nil {
					t.Fatal("same revision received a partial cache-expiry rejection", out)
				}
			}
			c.mu.Lock()
			retained := c.cache[key].schemas["lesson_opened"]
			c.mu.Unlock()
			if retained != compiled.schemas["lesson_opened"] {
				t.Fatal("eligibility refresh recompiled immutable schema")
			}
		})
	}
}

type inFlightQueue struct {
	until    time.Time
	admitted time.Time
	records  int
}

func (q *inFlightQueue) Publish(ctx context.Context, records []Record) error {
	q.admitted = time.Now()
	q.records = len(records)
	timer := time.NewTimer(time.Until(q.until.Add(5 * time.Millisecond)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func TestValidAdmissionRetainsDurableAcknowledgementAfterInFlightLeaseExpiry(t *testing.T) {
	c, _, stub, _, _ := collectorFixture(t)
	until := time.Now().Add(80 * time.Millisecond)
	c.plans = &horizonPlans{value: stub.value, until: until}
	q := &inFlightQueue{until: until}
	c.queue = q
	out, status, e := c.Collect(context.Background(), batchOf(eventJSON(validEvent())), browserAuth())
	if e != nil || status != 202 || out.Results[0].Status != "accepted" || q.records != 1 || !q.admitted.Before(until) || time.Now().Before(until) {
		t.Fatal("valid in-flight acknowledgement was spuriously invalidated", status, e, out, q.admitted, until)
	}
}
