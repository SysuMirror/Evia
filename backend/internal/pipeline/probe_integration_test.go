//go:build integration

package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func probeRequest(duration time.Duration) delivery.ProbeRequest {
	now := time.Now().UTC()
	return delivery.ProbeRequest{ProjectID: testProject, Environment: "development", ReleaseID: testEvent, Revision: 1, ManifestHash: strings.Repeat("a", 64), Nonce: ids.New(), IssuedAt: now, ExpiresAt: now.Add(duration)}
}
func TestReadinessRequiresActualConsumerAndFactCapability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	kafka, ch := integrationConfig(t)
	preparePipelineDependencies(t, ctx, kafka, ch)
	client, e := NewProbeClient(kafka, ch)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		bounded, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if e := client.Close(bounded); e != nil {
			t.Error(e)
		}
	}()
	if e = client.Ping(ctx); e != nil {
		t.Fatal(e)
	}
	// A broker acknowledgement alone cannot make a release ready: there is no
	// consumer or ClickHouse receipt yet, so readback must time out.
	missing := probeRequest(time.Second)
	if _, e = client.Verify(ctx, missing); e == nil {
		t.Fatal("broker-only acknowledgement counted as pipeline readiness")
	}
	worker, e := NewProbeWorker(kafka, ch)
	if e != nil {
		t.Fatal(e)
	}
	if e = worker.Ping(ctx); e != nil {
		_ = worker.Close(ctx)

		t.Fatal("actual fact schema/grant capability", e)
	}
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- worker.Run(workerCtx) }()
	request := probeRequest(10 * time.Second)
	proof, e := client.Verify(ctx, request)
	if e != nil {
		stop()
		<-done
		t.Fatal("actual durable readback", e)
	}
	if e = delivery.ValidateProof(request, proof, time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	if n := integrationCount(t, ctx, ch, "event_facts_raw"); n != 0 {
		t.Fatal("readiness polluted business event facts", n)
	}
	forged := request
	forged.ManifestHash = strings.Repeat("b", 64)
	if _, found, e := client.store.read(ctx, forged); e != nil || found {
		t.Fatal("receipt accepted wrong manifest hash", found, e)
	}
	forged = request
	forged.ProjectID = testSource
	if _, found, e := client.store.read(ctx, forged); e != nil || found {
		t.Fatal("receipt crossed project scope", found, e)
	}
	stop()
	if e = <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	// A valid private receipt table cannot hide a missing real fact view.
	if _, e = clickhouseQuery(ctx, ch, "DROP VIEW event_facts"); e != nil {
		t.Fatal(e)
	}
	if e = client.store.write(ctx, []delivery.ProbeRequest{probeRequest(10 * time.Second)}); e == nil {
		t.Fatal("receipt-only writes made a missing fact capability ready")
	}
}
