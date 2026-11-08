package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
)

const lookback = 30 * time.Minute // Admission currently rejects events older than one hour.

type batch struct {
	start, count int
	replay       bool
	body         []byte
	request      wire.CollectRequest
}

func stableID(seed, role string, ordinal int) string {
	b := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%d", seed, role, ordinal)))
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func ptr[T any](v T) *T { return &v }

func makeBatch(c Config, o Options, start, count int, replay bool) batch {
	seed := c.RunID + "/" + c.SourceKind
	r := wire.CollectRequest{ProtocolVersion: 1, BatchID: stableID(seed, "batch", start), WriteKey: c.WriteKey, SentAt: c.StartedAt.Format(time.RFC3339Nano)}
	for i := start; i < start+count; i++ {
		e := wire.EventEnvelope{EventID: stableID(seed, "event", i), EventKey: c.EventKey, SchemaVersion: c.SchemaVersion, ReleaseRevision: c.ReleaseRevision}
		e.OccurredAt = c.StartedAt.Add(-lookback).Add(time.Duration(int64(i) * int64(lookback) / int64(o.Events))).Format(time.RFC3339Nano)
		e.Identity.UID = ptr(fmt.Sprintf("bench-%s-%d", c.RunID, i%c.UIDCardinality))
		e.Context.SessionID = ptr(stableID(seed, "session", i%c.UIDCardinality))
		e.Context.TabID = ptr(stableID(seed, "tab", i%c.UIDCardinality))
		e.Context.PageViewID = ptr(stableID(seed, "page", i))
		e.Context.Sequence = ptr(int64(i/c.UIDCardinality + 1))
		e.Context.RouteName, e.Context.RouteTemplate = ptr("Benchmark"), ptr("/page/:id")
		e.SDK.Name, e.SDK.Version = "evia-http-benchmark", "1.0.0"
		e.Properties = map[string]any{"page": fmt.Sprintf("/page/%d", i%1000), "section": fmt.Sprintf("section-%d", i%10), "post_id": fmt.Sprintf("post-%d", i%10000), "duration_ms": i % 1000, "value": i % 100, "tag": fmt.Sprintf("tag-%d", i%20)}
		r.Events = append(r.Events, e)
	}
	body, _ := json.Marshal(r) // Generated wire values contain no unsupported JSON types.
	return batch{start: start, count: count, replay: replay, body: body, request: r}
}

// Splitting at replayEnd permits exact byte-for-byte original batch replay.
func batchCount(start, end, size, replayEnd int) int {
	n := min(size, end-start)
	if start < replayEnd {
		n = min(n, replayEnd-start)
	}
	return n
}

func expectedFacts(events, users int) ExpectedFacts {
	sumModulo := func(modulus int64) int64 {
		n := int64(events)
		rest := n % modulus
		return (n/modulus)*modulus*(modulus-1)/2 + rest*(rest-1)/2
	}
	return ExpectedFacts{Count: events, UIDCardinality: min(events, users), ValueSum: sumModulo(100), DurationMSAverage: float64(sumModulo(1000)) / float64(events), SectionZeroCount: (events + 9) / 10}
}
