package pipeline

import (
	"context"
	"testing"
)

func TestReadinessReaderValidatesProjectBeforeIO(t *testing.T) {
	reader, err := NewReadinessReader(ClickHouseConfig{URL: "http://127.0.0.1:1", Database: "test_events"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.LatestProofs(context.Background(), "not-a-uuid", 10); err == nil {
		t.Fatal("invalid project id must be rejected before any storage call")
	}
	// Out-of-range limits clamp instead of failing; the call reaches storage and
	// fails on connection, which proves validation did not reject it locally.
	if _, err := reader.LatestProofs(context.Background(), "11111111-1111-4111-8111-111111111111", 999); err != ErrSinkUnavailable {
		t.Fatalf("expected sink unavailability after clamp, got %v", err)
	}
}

func TestNewReadinessReaderRejectsBadConfig(t *testing.T) {
	if _, err := NewReadinessReader(ClickHouseConfig{URL: "http://127.0.0.1:8123", Database: "bad-db-name"}); err == nil {
		t.Fatal("invalid database name must be rejected")
	}
}
