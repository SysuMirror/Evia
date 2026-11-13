//go:build integration

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/retention"
)

const retentionTSLayout = "2006-01-02 15:04:05.000"

// TestRetentionEnforcementCapacity proves the long-retention production path
// on the real ClickHouse stack: a SweepWorker drives the real
// RetentionExecutor through dry-run, enforced deletion and idempotent
// re-sweep across multiple tenants, while physical disk reclamation,
// mutation amplification and scope isolation are measured rather than
// assumed. The policy store is an in-process double because the PostgreSQL
// adapter has its own integration suite and the pipeline package cannot
// import the postgres package (test dependency direction is the reverse).
func TestRetentionEnforcementCapacity(t *testing.T) {
	if os.Getenv("EVIA_TEST_RETENTION_DRILL") != "1" {
		t.Skip("retention drill requires EVIA_TEST_RETENTION_DRILL=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := erasureDatabase(t)
	executor := retentionExecutor(t, c)

	const (
		retentionDays        = 30
		agedRowsPerScope     = 1500
		recentRowsPerScope   = 400
		policyLessAgedRows   = 800
		configuredScopeCount = 6
	)
	now := time.Now().UTC()
	agedTS := now.AddDate(0, 0, -400).Format(retentionTSLayout)
	recentTS := now.AddDate(0, 0, -5).Format(retentionTSLayout)

	projects := []string{
		testProject,
		"44444444-4444-4444-8444-444444444444",
		"55555555-5555-4555-8555-555555555555",
	}
	environments := []string{"development", "production"}
	policyLessProject := "66666666-6666-4666-8666-666666666666"

	type scope struct {
		project string
		env     string
	}
	policyScopes := make([]scope, 0, configuredScopeCount)
	policies := make([]retention.Policy, 0, configuredScopeCount)
	for _, project := range projects {
		for _, env := range environments {
			policyScopes = append(policyScopes, scope{project: project, env: env})
			policies = append(policies, retention.Policy{
				ProjectID:     project,
				Environment:   env,
				RetentionDays: retentionDays,
			})
		}
	}

	// Interleave every scope round-robin so individual parts hold mixed
	// tenants and age bands, which is the worst case for a scoped mutation.
	var rows []factRow
	ordinal := 0
	for i := 0; i < agedRowsPerScope; i++ {
		for _, sc := range policyScopes {
			rows = append(rows, retentionRow(t,
				fmt.Sprintf("ret-aged-%06d", ordinal), sc.project, sc.env, agedTS))
			ordinal++
		}
	}
	for i := 0; i < recentRowsPerScope; i++ {
		for _, sc := range policyScopes {
			rows = append(rows, retentionRow(t,
				fmt.Sprintf("ret-recent-%06d", ordinal), sc.project, sc.env, recentTS))
			ordinal++
		}
	}
	// A seventh scope with aged data but NO policy: a sweep must never touch
	// data that no configured policy covers.
	for i := 0; i < policyLessAgedRows; i++ {
		rows = append(rows, retentionRow(t,
			fmt.Sprintf("ret-orphan-%06d", i), policyLessProject, "development", agedTS))
	}
	totalInserted := len(rows)
	wantDeleted := configuredScopeCount * agedRowsPerScope
	wantRetained := configuredScopeCount*recentRowsPerScope + policyLessAgedRows
	if totalInserted != wantDeleted+wantRetained {
		t.Fatalf("fixture invariant broken: inserted=%d deleted=%d retained=%d",
			totalInserted, wantDeleted, wantRetained)
	}
	for start := 0; start < len(rows); start += 1000 {
		end := start + 1000
		if end > len(rows) {
			end = len(rows)
		}
		postFactRows(t, ctx, c, rows[start:end])
	}

	if got := factCount(t, c, "1=1"); got != uint64(totalInserted) {
		t.Fatalf("landed %d rows, want %d", got, totalInserted)
	}

	store := &memSweepStore{policies: policies}
	observer := &recordingSweepObserver{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Phase 1: dry-run counts every expired scope without mutating facts.
	dryWorker := retention.NewSweepWorker(store, executor, false, time.Hour, logger, observer)
	dryResults, err := dryWorker.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var dryCandidates uint64
	for _, result := range dryResults {
		if result.Err != nil || !result.DryRun {
			t.Fatalf("dry-run scope failed or wrong mode: %+v", result)
		}
		dryCandidates += result.Candidates
	}
	if dryCandidates != uint64(wantDeleted) {
		t.Fatalf("dry-run candidates=%d, want %d", dryCandidates, wantDeleted)
	}
	if got := factCount(t, c, "1=1"); got != uint64(totalInserted) {
		t.Fatalf("dry-run mutated facts: rows=%d", got)
	}

	baselineBytes := stableTableBytes(t, c)
	baselineFree := diskFree(t, c)
	mutationsBefore := mutationCount(t, c)
	if mutationsBefore != 0 {
		t.Fatalf("fresh database already reports %d mutations", mutationsBefore)
	}
	windowStart := time.Now().UTC()

	// Phase 2: enforced sweep with a concurrent poller capturing the physical
	// part coexistence peak while mutations_sync=1 blocks the worker call.
	peak := newBytePeak(c)
	peakDone := peak.start(ctx)
	enforceWorker := retention.NewSweepWorker(store, executor, true, time.Hour, logger, observer)
	enforceResults, err := enforceWorker.RunOnce(ctx)
	peakBytes := peak.stop(peakDone)
	if err != nil {
		t.Fatal(err)
	}
	var enforcedDeleted uint64
	for _, result := range enforceResults {
		if result.Err != nil || result.DryRun {
			t.Fatalf("enforced scope failed or wrong mode: %+v", result)
		}
		enforcedDeleted += result.Candidates
	}
	if enforcedDeleted != uint64(wantDeleted) {
		t.Fatalf("enforced deleted=%d, want %d", enforcedDeleted, wantDeleted)
	}

	afterBytes := stableTableBytes(t, c)
	afterFree := diskFree(t, c)
	mutationsTotal, mutationsDone := mutationStatus(t, c)
	// part_log is buffer-flushed (~7.5s); force a flush so the corroborating
	// mutation events are queryable. It stays soft evidence: system.mutations
	// is the authoritative, durable gate.
	if _, flushErr := clickhouseQuery(ctx, c, "SYSTEM FLUSH LOGS"); flushErr != nil {
		t.Logf("SYSTEM FLUSH LOGS failed (part_log evidence degraded): %v", flushErr)
	}
	partStats := partLogMutationStats(t, c, windowStart)

	// Every configured scope is empty of expired rows while its recent band is
	// byte-exact preserved.
	for _, sc := range policyScopes {
		cutoff := now.AddDate(0, 0, -retentionDays)
		expired, e := executor.CountExpired(ctx, sc.project, sc.env, cutoff)
		if e != nil || expired != 0 {
			t.Fatalf("scope %s/%s expired left=%d err=%v", sc.project, sc.env, expired, e)
		}
		recent := factCount(t, c, fmt.Sprintf(
			"project_id='%s' AND environment='%s' AND event_id LIKE 'ret-recent-%%'",
			sc.project, sc.env))
		if recent != uint64(recentRowsPerScope) {
			t.Fatalf("scope %s/%s recent rows=%d, want %d",
				sc.project, sc.env, recent, recentRowsPerScope)
		}
	}
	// The policy-less scope is fully intact even though its rows are aged.
	orphanRows := factCount(t, c, fmt.Sprintf("project_id='%s'", policyLessProject))
	if orphanRows != policyLessAgedRows {
		t.Fatalf("policy-less scope rows=%d, want %d", orphanRows, policyLessAgedRows)
	}
	if got := factCount(t, c, "1=1"); got != uint64(wantRetained) {
		t.Fatalf("rows after sweep=%d, want %d", got, wantRetained)
	}

	// Phase 3: re-sweeping the same policies is a no-op and issues no mutation.
	resweepWorker := retention.NewSweepWorker(store, executor, true, time.Hour, logger, observer)
	resweepResults, err := resweepWorker.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range resweepResults {
		if result.Err != nil || result.Candidates != 0 {
			t.Fatalf("re-sweep not idempotent: %+v", result)
		}
	}
	resweepTotal, _ := mutationStatus(t, c)
	if resweepTotal != mutationsTotal {
		t.Fatalf("re-sweep created mutations: before=%d after=%d", mutationsTotal, resweepTotal)
	}
	resweepBytes := stableTableBytes(t, c)

	observer.assertShape(t, configuredScopeCount)
	store.assertAuditShape(t, configuredScopeCount)

	if mutationsTotal != configuredScopeCount || mutationsDone != configuredScopeCount {
		t.Fatalf("mutations total=%d done=%d, want %d/%d",
			mutationsTotal, mutationsDone, configuredScopeCount, configuredScopeCount)
	}
	reclaimed := baselineBytes - afterBytes
	if reclaimed <= 0 {
		t.Fatalf("no physical reclamation: before=%d after=%d", baselineBytes, afterBytes)
	}
	if peakBytes < baselineBytes {
		t.Fatalf("measured peak %d below baseline %d", peakBytes, baselineBytes)
	}

	summary, err := json.Marshal(map[string]any{
		"type":                  "retention_enforcement_summary",
		"configured_scopes":     configuredScopeCount,
		"policy_less_scopes":    1,
		"aged_rows_per_scope":   agedRowsPerScope,
		"recent_rows_per_scope": recentRowsPerScope,
		"policy_less_rows":      policyLessAgedRows,
		"total_inserted":        totalInserted,
		"dry_run_candidates":    dryCandidates,
		"enforced_deleted":      enforcedDeleted,
		"rows_after_sweep":      wantRetained,
		"retained_recent_rows":  configuredScopeCount * recentRowsPerScope,
		"mutations_total":       mutationsTotal,
		"mutations_done":        mutationsDone,
		"resweep_mutations":     resweepTotal - mutationsTotal,
		"table_bytes_before":    baselineBytes,
		"table_bytes_peak":      peakBytes,
		"table_bytes_after":     afterBytes,
		"table_bytes_resweep":   resweepBytes,
		"reclaimed_bytes":       reclaimed,
		"peak_amplification":    peakBytes - baselineBytes,
		"disk_free_before":      baselineFree,
		"disk_free_after":       afterFree,
		"part_log":              partStats,
		"audit_records":         store.auditCounts(),
		"isolation_intact":      true,
		"policy_less_intact":    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(string(summary))
}

// postFactRows streams JSONEachRow data in the POST body so large fixtures
// never overflow the ClickHouse HTTP URI limit; the INSERT prefix stays in
// the query string, matching insertReadinessRows and the production sink.
func postFactRows(t *testing.T, ctx context.Context, c ClickHouseConfig, rows []factRow) {
	t.Helper()
	var builder strings.Builder
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	endpoint, err := url.Parse(c.URL)
	if err != nil {
		t.Fatal(err)
	}
	values := endpoint.Query()
	values.Set("database", c.Database)
	values.Set("query", "INSERT INTO event_facts_raw FORMAT JSONEachRow")
	values.Set("wait_end_of_query", "1")
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(),
		bytes.NewReader([]byte(builder.String())))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if c.Username != "" {
		request.SetBasicAuth(c.Username, c.Password)
	}
	response, err := c.Client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		buf := make([]byte, 2048)
		n, _ := response.Body.Read(buf)
		t.Fatalf("fact insert failed: %s %s", response.Status, string(buf[:n]))
	}
}

// memSweepStore is the in-process retention.SweepStore double for the drill.
type memSweepStore struct {
	mu       sync.Mutex
	policies []retention.Policy
	records  []retention.SweepRecord
}

func (m *memSweepStore) ListRetentionPolicies(context.Context) ([]retention.Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]retention.Policy, len(m.policies))
	copy(out, m.policies)
	return out, nil
}

func (m *memSweepStore) RecordSweep(_ context.Context, record retention.SweepRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, record)
	return nil
}

func (m *memSweepStore) assertAuditShape(t *testing.T, scopeCount int) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var dryRun, enforced, failed int
	for _, record := range m.records {
		if record.Status != retention.SweepSucceeded {
			failed++
		}
		if record.DryRun {
			dryRun++
		} else {
			enforced++
		}
	}
	if failed != 0 || dryRun != scopeCount || enforced != scopeCount*2 {
		t.Fatalf("audit records dry=%d enforced=%d failed=%d, want %d/%d/0",
			dryRun, enforced, failed, scopeCount, scopeCount*2)
	}
}

func (m *memSweepStore) auditCounts() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	var dryRun, enforced int
	for _, record := range m.records {
		if record.DryRun {
			dryRun++
		} else {
			enforced++
		}
	}
	return map[string]int{"dry_run": dryRun, "enforced": enforced}
}

type scopeEvent struct {
	environment string
	mode        string
	succeeded   bool
	candidates  uint64
}

type recordingSweepObserver struct {
	mu     sync.Mutex
	passes []bool
	scopes []scopeEvent
}

func (o *recordingSweepObserver) SweepPassCompleted(succeeded bool, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.passes = append(o.passes, succeeded)
}

func (o *recordingSweepObserver) ScopeSwept(environment, mode string, succeeded bool, candidates uint64, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.scopes = append(o.scopes, scopeEvent{environment, mode, succeeded, candidates})
}

func (o *recordingSweepObserver) assertShape(t *testing.T, scopeCount int) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.passes) != 3 || !o.passes[0] || !o.passes[1] || !o.passes[2] {
		t.Fatalf("pass events=%v, want three succeeded passes", o.passes)
	}
	var dry, enforce int
	for _, event := range o.scopes {
		if !event.succeeded {
			t.Fatalf("scope event failed: %+v", event)
		}
		switch event.mode {
		case retention.ModeDryRun:
			dry++
		case retention.ModeEnforce:
			enforce++
		default:
			t.Fatalf("unknown mode label %q", event.mode)
		}
	}
	if dry != scopeCount || enforce != scopeCount*2 {
		t.Fatalf("scope events dry=%d enforce=%d, want %d/%d",
			dry, enforce, scopeCount, scopeCount*2)
	}
}

type bytePeak struct {
	config ClickHouseConfig
	values chan uint64
}

func newBytePeak(c ClickHouseConfig) *bytePeak {
	return &bytePeak{config: c, values: make(chan uint64, 256)}
}

// start polls total on-disk size across active and inactive parts so the
// transient old/new part coexistence window of a synchronous mutation is
// captured instead of being hidden by the blocking HTTP call.
func (b *bytePeak) start(ctx context.Context) context.CancelFunc {
	pollCtx, stop := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				body, err := clickhouseQuery(pollCtx, b.config,
					"SELECT sum(bytes_on_disk) FROM system.parts WHERE database='"+
						b.config.Database+"' AND table='event_facts_raw' FORMAT TSV")
				if err != nil {
					continue
				}
				value, parseErr := strconv.ParseUint(strings.TrimSpace(string(body)), 10, 64)
				if parseErr == nil {
					b.values <- value
				}
			}
		}
	}()
	return stop
}

func (b *bytePeak) stop(stop context.CancelFunc) uint64 {
	stop()
	close(b.values)
	var peak uint64
	for value := range b.values {
		if value > peak {
			peak = value
		}
	}
	return peak
}

func stableTableBytes(t *testing.T, c ClickHouseConfig) uint64 {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var previous uint64
	for time.Now().Before(deadline) {
		current := tableBytes(t, c)
		if current == previous {
			return current
		}
		previous = current
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("table bytes did not stabilize: last=%d", previous)
	return 0
}

func tableBytes(t *testing.T, c ClickHouseConfig) uint64 {
	t.Helper()
	body, err := clickhouseQuery(context.Background(), c,
		"SELECT sum(bytes_on_disk) FROM system.parts WHERE database='"+
			c.Database+"' AND table='event_facts_raw' AND active FORMAT TSV")
	if err != nil {
		t.Fatal(err)
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(body)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func diskFree(t *testing.T, c ClickHouseConfig) uint64 {
	t.Helper()
	body, err := clickhouseQuery(context.Background(), c,
		"SELECT free_space FROM system.disks WHERE name='default' FORMAT TSV")
	if err != nil {
		t.Fatal(err)
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(body)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mutationCount(t *testing.T, c ClickHouseConfig) int {
	t.Helper()
	total, _ := mutationStatus(t, c)
	return total
}

func mutationStatus(t *testing.T, c ClickHouseConfig) (total, done int) {
	t.Helper()
	body, err := clickhouseQuery(context.Background(), c,
		"SELECT count(), countIf(is_done=1) FROM system.mutations WHERE database='"+
			c.Database+"' AND table='event_facts_raw' FORMAT TSV")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(strings.TrimSpace(string(body)))
	if len(fields) != 2 {
		t.Fatalf("unexpected mutation status: %q", body)
	}
	total, err = strconv.Atoi(fields[0])
	if err != nil {
		t.Fatal(err)
	}
	done, err = strconv.Atoi(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	return total, done
}

func partLogMutationStats(t *testing.T, c ClickHouseConfig, since time.Time) map[string]map[string]uint64 {
	t.Helper()
	body, err := clickhouseQuery(context.Background(), c,
		"SELECT event_type, count(), sum(size_in_bytes) FROM system.part_log "+
			"WHERE database='"+c.Database+"' AND table='event_facts_raw' AND event_time >= toDateTime('"+
			since.Format(retentionTSLayout)+"') GROUP BY event_type ORDER BY event_type FORMAT TSV")
	if err != nil {
		t.Fatal(err)
	}
	stats := map[string]map[string]uint64{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		count, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		size, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		stats[fields[0]] = map[string]uint64{"parts": count, "bytes": size}
	}
	return stats
}
