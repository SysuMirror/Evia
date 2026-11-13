//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const quorumStallTable = "evia_ha_quorum_drill"

// TestQuorumStallBounded drives the production connection pool while the
// drill stops every synchronous standby. A COMMIT that cannot collect its
// quorum waits on IPC/SyncRep inside PostgreSQL. statement_timeout does not
// cover that wait because the commit runs after the statement boundary, so
// the production safety bound is the caller's context deadline: pgx
// cancels the waiting command instead of hanging forever.
//
// Canceling a SyncRep wait is ambiguous: the WAL may already be flushed on
// the primary, so PostgreSQL lets the transaction commit locally while
// returning an error to the client. The test therefore retries the same
// sequence idempotently: a unique_violation (SQLSTATE 23505) proves the
// "failed" insert actually landed and advances the sequence without creating
// a duplicate, mirroring the idempotency keys used by the production write
// paths. Once one standby returns, every committed row is contiguous 1..N.
func TestQuorumStallBounded(t *testing.T) {
	if os.Getenv("EVIA_TEST_PG_QUORUM_STALL") != "1" {
		t.Skip("synchronous quorum stall test requires EVIA_TEST_PG_QUORUM_STALL=1")
	}

	rootCtx := context.Background()
	store, err := Open(rootCtx, failoverDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Pool.Close()

	if _, err = store.Pool.Exec(rootCtx, "DROP TABLE IF EXISTS "+quorumStallTable); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(rootCtx, "CREATE TABLE "+quorumStallTable+
		" (seq bigint PRIMARY KEY, committed_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		t.Fatal(err)
	}

	// Diagnostic anchor: prove which server and which replication GUCs the
	// production pool actually sees before any fault injection.
	var serverAddr, inRecovery, syncCommit, syncNames string
	if err = store.Pool.QueryRow(rootCtx,
		"SELECT COALESCE(inet_server_addr()::text, ''), pg_is_in_recovery()::text, "+
			"current_setting('synchronous_commit'), current_setting('synchronous_standby_names')",
	).Scan(&serverAddr, &inRecovery, &syncCommit, &syncNames); err != nil {
		t.Fatal(err)
	}
	fmt.Printf(`{"event":"session","server":%q,"in_recovery":%q,"synchronous_commit":%q,"synchronous_standby_names":%q}`+"\n",
		serverAddr, inRecovery, syncCommit, syncNames)

	// Baseline commit while the full quorum is healthy.
	if _, err = store.Pool.Exec(rootCtx, "INSERT INTO "+quorumStallTable+" (seq) VALUES (1)"); err != nil {
		t.Fatalf("healthy quorum insert failed: %v", err)
	}

	seq := 2
	var blockedAt time.Time
	var blockedMS int64
	ambiguousCommits := 0
	healthySuccesses := 0
	stallObserved := false
	deadline := time.Now().Add(120 * time.Second)
	eventf := func(format string, args ...any) { fmt.Printf(format+"\n", args...) }

	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(rootCtx, 6*time.Second)
		started := time.Now()
		_, insertErr := store.Pool.Exec(ctx, "INSERT INTO "+quorumStallTable+" (seq) VALUES ($1)", seq)
		elapsed := time.Since(started)
		cancel()

		if insertErr == nil {
			if stallObserved {
				eventf(`{"event":"recovered","seq":%d,"elapsed_ms":%d}`, seq, elapsed.Milliseconds())
				break // quorum recovered and the retried sequence committed once
			}
			healthySuccesses++
			if healthySuccesses%25 == 0 {
				eventf(`{"event":"healthy","seq":%d}`, seq)
			}
			seq++
			time.Sleep(50 * time.Millisecond)
			continue
		}

		var pgErr *pgconn.PgError
		errorCode := ""
		if errors.As(insertErr, &pgErr) {
			errorCode = pgErr.Code
		}
		eventf(`{"event":"error","seq":%d,"elapsed_ms":%d,"sqlstate":%q,"err":%q}`,
			seq, elapsed.Milliseconds(), errorCode, insertErr.Error())
		if errorCode == "23505" {
			// The canceled SyncRep wait had committed locally; the
			// idempotent retry confirms the row instead of duplicating it.
			ambiguousCommits++
			seq++
			continue
		}

		if !stallObserved {
			// A connection refusal returns immediately; a quorum wait is
			// canceled by the six-second client context, so a fast failure
			// means the drill topology did not actually stall commits.
			if elapsed < 5*time.Second || elapsed > 7*time.Second {
				t.Fatalf("expected a bounded SyncRep wait near the context deadline, got %s: %v", elapsed, insertErr)
			}
			stallObserved = true
			blockedAt = started
			blockedMS = elapsed.Milliseconds()
		}
		time.Sleep(500 * time.Millisecond)
	}

	if !stallObserved {
		t.Fatal("no quorum stall was observed while both standbys were stopped")
	}
	if time.Now().After(deadline) {
		t.Fatal("pool did not resume inserts after the quorum became satisfiable")
	}

	var count, distinct, maxSeq, gaps int
	if err = store.Pool.QueryRow(rootCtx,
		"SELECT count(*), count(DISTINCT seq), max(seq) FROM "+quorumStallTable,
	).Scan(&count, &distinct, &maxSeq); err != nil {
		t.Fatal(err)
	}
	if err = store.Pool.QueryRow(rootCtx,
		"SELECT count(*) FROM generate_series(1, $1) AS expected(seq) WHERE expected.seq NOT IN (SELECT seq FROM "+quorumStallTable+")",
		maxSeq,
	).Scan(&gaps); err != nil {
		t.Fatal(err)
	}
	if count != distinct || maxSeq != seq || gaps != 0 {
		t.Fatalf("committed sequence is not contiguous: count=%d distinct=%d max=%d gaps=%d seq=%d",
			count, distinct, maxSeq, gaps, seq)
	}

	summary, err := json.Marshal(map[string]any{
		"type":              "quorum_stall_summary",
		"committed_rows":    count,
		"blocked_ms":        blockedMS,
		"ambiguous_commits": ambiguousCommits,
		"stall_started_at":  blockedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(string(summary))
}
