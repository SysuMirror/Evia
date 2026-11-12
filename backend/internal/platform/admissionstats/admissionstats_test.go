package admissionstats

import (
	"strconv"
	"testing"
	"time"
)

func testRecorder(t *testing.T) *Recorder {
	t.Helper()
	recorder, err := New(Config{URL: "redis://localhost:6379", KeyPrefix: "evia:test:admission", Timeout: time.Second, PoolSize: 4, AllowInsecureLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	return recorder
}

func TestConfigValidation(t *testing.T) {
	cases := map[string]Config{
		"prefix":       {URL: "redis://localhost:6379", KeyPrefix: "Bad-Prefix", Timeout: time.Second, PoolSize: 4, AllowInsecureLocal: true},
		"timeout":      {URL: "redis://localhost:6379", KeyPrefix: "evia:test", Timeout: 6 * time.Second, PoolSize: 4, AllowInsecureLocal: true},
		"pool":         {URL: "redis://localhost:6379", KeyPrefix: "evia:test", Timeout: time.Second, PoolSize: 65, AllowInsecureLocal: true},
		"plaintext":    {URL: "redis://redis.example:6379", KeyPrefix: "evia:test", Timeout: time.Second, PoolSize: 4},
		"scheme":       {URL: "mysql://localhost:6379", KeyPrefix: "evia:test", Timeout: time.Second, PoolSize: 4, AllowInsecureLocal: true},
		"query":        {URL: "redis://localhost:6379?db=1", KeyPrefix: "evia:test", Timeout: time.Second, PoolSize: 4, AllowInsecureLocal: true},
		"missing host": {URL: "rediss:///db", KeyPrefix: "evia:test", Timeout: time.Second, PoolSize: 4},
		"bad port":     {URL: "redis://localhost:99999", KeyPrefix: "evia:test", Timeout: time.Second, PoolSize: 4, AllowInsecureLocal: true},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatal("expected configuration rejection")
			}
		})
	}
}

func TestReasonsVocabularyIsBoundedAndUnique(t *testing.T) {
	if len(Reasons) == 0 || len(Reasons) > 64 {
		t.Fatalf("unexpected reason vocabulary size: %d", len(Reasons))
	}
	seen := map[string]struct{}{}
	for _, reason := range Reasons {
		if reason == "" {
			t.Fatal("empty rejection reason")
		}
		if _, ok := seen[reason]; ok {
			t.Fatalf("duplicate rejection reason: %s", reason)
		}
		seen[reason] = struct{}{}
	}
}

func TestValidScope(t *testing.T) {
	project := "12345678-1234-1234-1234-123456789abc"
	for _, environment := range []string{"development", "staging", "production"} {
		if !validScope(project, environment) {
			t.Fatalf("scope should be valid: %s %s", project, environment)
		}
	}
	for _, scope := range [][2]string{
		{project, "prod"},
		{"not-a-uuid", "production"},
		{"", "production"},
	} {
		if validScope(scope[0], scope[1]) {
			t.Fatalf("scope should be invalid: %v", scope)
		}
	}
}

func TestWindowKeyIsHourTruncatedAndTagged(t *testing.T) {
	recorder := testRecorder(t)
	project := "12345678-1234-1234-1234-123456789abc"
	first := time.Date(2026, 11, 12, 8, 15, 42, 0, time.UTC)
	second := time.Date(2026, 11, 12, 8, 59, 59, 0, time.UTC)
	third := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	keyA := recorder.key(project, "production", first)
	keyB := recorder.key(project, "production", second)
	keyC := recorder.key(project, "production", third)
	if keyA != keyB {
		t.Fatalf("same hourly window must share a key: %q != %q", keyA, keyB)
	}
	if keyA == keyC {
		t.Fatal("different hourly windows must use different keys")
	}
	expected := "evia:test:admission:{" + project + ":production}:" + strconvHour(first.Truncate(time.Hour))
	if keyA != expected {
		t.Fatalf("unexpected key: got %q want %q", keyA, expected)
	}
}

func TestRecordRejectsInvalidInputWithoutRedis(t *testing.T) {
	recorder := testRecorder(t)
	project := "12345678-1234-1234-1234-123456789abc"
	// Invalid scopes and empty or unknown maps must return before any I/O.
	recorder.Record(t.Context(), "bad-uuid", "production", map[string]int{"INVALID_EVENT": 1})
	recorder.Record(t.Context(), project, "production", nil)
	recorder.Record(t.Context(), project, "production", map[string]int{"NOT_A_REASON": 3, "EVENT_TOO_LARGE": 0})
	if got := recorder.DroppedWrites(); got != 0 {
		t.Fatalf("no Redis command should run for invalid input, dropped=%d", got)
	}
}

func TestReadClampsWindowCount(t *testing.T) {
	recorder := testRecorder(t)
	project := "12345678-1234-1234-1234-123456789abc"
	read, err := recorder.Read(t.Context(), project, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Environments) != 3 || len(read.Totals) != 0 {
		t.Fatalf("unexpected empty read: %+v", read)
	}
	if _, err := recorder.Read(t.Context(), "bad-uuid", DefaultWindows); err == nil {
		t.Fatal("invalid project must be rejected")
	}
}

func strconvHour(value time.Time) string {
	return strconv.FormatInt(value.Unix(), 10)
}
