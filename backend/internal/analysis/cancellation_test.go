package analysis

import (
	"context"
	"errors"
	"fmt"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"strings"
	"testing"
	"time"
)

func TestJourneyUIDUsesUnicodeCharacters(t *testing.T) {
	for _, count := range []int{100, 128, 129} {
		uid := strings.Repeat("映", count)
		raw := strings.Replace(baseRequest, `"kind":"trend"`, `"kind":"journey","subject":"uid","identity_filter":{"uid":"`+uid+`"}`, 1)
		validator, e := contract.New()
		if e != nil {
			t.Fatal(e)
		}
		_, e = Parse([]byte(raw), validator)
		if (e == nil) != (count <= 128) {
			t.Fatal("UID character boundary differs from event contract", count, e)
		}
	}
	for _, uid := range []string{string([]byte{0xff}), "user\x00name"} {
		r := request(t, baseRequest)
		r.Kind = "journey"
		r.Metrics = nil
		r.Subject = "uid"
		r.IdentityFilter = &IdentityFilter{UID: &uid}
		if e := Validate(&r); e == nil {
			t.Fatal("invalid UTF-8/NUL UID accepted")
		}
	}
}

type cancelAfterChecks struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelAfterChecks) Err() error {
	c.checks--
	if c.checks <= 0 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestFunnelCPUWorkHonorsCancellation(t *testing.T) {
	request := Request{Funnel: &Funnel{WindowSeconds: 60, Steps: []Step{{EventKey: "open"}, {EventKey: "finish"}}}}
	rows := make([]map[string]any, 5000)
	for i := range rows {
		rows[i] = map[string]any{"subject_id": "subject", "event_id": fmt.Sprint(i), "event_time_ns": int64(i), "step_0": true, "step_1": false}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupted := &cancelAfterChecks{Context: ctx, cancel: cancel, checks: 30}
	if result, e := evaluateFunnel(interrupted, request, rows); !errors.Is(e, context.Canceled) || result != nil {
		t.Fatal("row ingestion ignored cancellation", e)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	interrupted = &cancelAfterChecks{Context: ctx, cancel: cancel, checks: 20}
	events := make([]funnelEvent, 5000)
	for i := range events {
		events[i].time = int64(len(events) - i)
	}
	if e := sortFunnelEvents(interrupted, events); !errors.Is(e, context.Canceled) {
		t.Fatal("sorting ignored cancellation", e)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(time.Millisecond, cancel)
	if result, e := evaluateFunnel(ctx, request, rows); !errors.Is(e, context.Canceled) || result != nil {
		t.Fatal("candidate computation continued after cancellation", e)
	}
}
