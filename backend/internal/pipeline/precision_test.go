package pipeline

import (
	"testing"
	"time"
)

func TestFactTimestampNanosecondBoundsAndCanonicalIdentity(t *testing.T) {
	lower := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	upper := time.Unix(0, 1<<63-1).UTC()
	for _, tc := range []struct {
		name  string
		value time.Time
		valid bool
	}{{"minimum", lower, true}, {"below minimum", lower.Add(-time.Nanosecond), false}, {"maximum", upper, true}, {"beyond maximum", upper.Add(time.Nanosecond), false}, {"same maximum different offset", upper.In(time.FixedZone("offset", 3600)), true}} {
		t.Run(tc.name, func(t *testing.T) {
			record := testRecord()
			record.OccurredAt = tc.value
			encoded, e := EncodeRecord(record)
			if (e == nil) != tc.valid {
				t.Fatal("incorrect timestamp bound", tc.value, e)
			}
			if e == nil {
				if _, e = DecodeRecord(encoded); e != nil {
					t.Fatal("queue roundtrip lost supported bound", e)
				}
			}
		})
	}
	first := testRecord()
	first.OccurredAt = time.Date(2026, 11, 7, 10, 11, 12, 123000001, time.UTC)
	one, e := rowFor(first)
	if e != nil {
		t.Fatal(e)
	}
	retry := first
	retry.ReceivedAt = first.ReceivedAt.Add(time.Second)
	two, e := rowFor(retry)
	if e != nil || two.CanonicalHash != one.CanonicalHash || two.EventTimePrecise != one.EventTimePrecise {
		t.Fatal("retry changed immutable event identity", two, e)
	}
	conflict := first
	conflict.OccurredAt = first.OccurredAt.Add(time.Nanosecond)
	three, e := rowFor(conflict)
	if e != nil || three.CanonicalHash == one.CanonicalHash || three.EventTimePrecise == one.EventTimePrecise {
		t.Fatal("nanosecond-distinct event content collapsed", three, e)
	}
	if one.OccurredAtPrecise != "2026-11-07 10:11:12.123000001" || one.EventTime != one.EventTimePrecise || one.OccurredAt != one.OccurredAtPrecise {
		t.Fatal("sink formatting truncated timestamp", one)
	}
}
