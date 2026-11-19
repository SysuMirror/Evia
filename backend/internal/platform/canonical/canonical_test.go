package canonical

import (
	"encoding/json"
	"math"
	"testing"
)

func TestStableJSONNumericIdentity(t *testing.T) {
	value := map[string]any{"z": "标题", "a": []any{json.Number("-0.0"), json.Number("1e0"), json.Number("1.0")}}
	raw, e := JSON(value)
	if e != nil {
		t.Fatal(e)
	}
	if string(raw) != `{"a":[0,1,1],"z":"标题"}` {
		t.Fatal("established canonical bytes changed", string(raw))
	}
	actual, e := Hash(value)
	if e != nil {
		t.Fatal(e)
	}
	expected, e := Hash(json.RawMessage(`{"a":[0,1,1],"z":"标题"}`))
	if e != nil || actual != expected {
		t.Fatal(actual, expected, e)
	}
	again, e := JSON(value)
	if e != nil || string(again) != string(raw) {
		t.Fatal("canonicalization modified input", string(again), e)
	}
}
func TestUnsafeJSONRejected(t *testing.T) {
	for _, value := range []any{json.Number("9007199254740992"), math.Inf(1), math.NaN(), func() {}, json.RawMessage(`{"bad":`)} {
		if _, e := JSON(value); e == nil {
			t.Fatalf("unsupported value accepted: %T", value)
		}
	}
	for _, value := range []any{json.Number("9007199254740991"), json.Number("-9007199254740991")} {
		if _, e := JSON(value); e != nil {
			t.Fatal(e)
		}
	}
}

// TestSafeIntegerBoundPinned pins the JSON safe-integer bound to its
// mathematical value (2^53 - 1) and keeps the typed and untyped exports equal,
// so the single source of truth referenced across the codebase cannot drift.
func TestSafeIntegerBoundPinned(t *testing.T) {
	const want = int64(1<<53 - 1)
	if MaxSafeInteger != want {
		t.Fatalf("MaxSafeInteger=%v must equal 2^53-1=%d", MaxSafeInteger, want)
	}
	if MaxInteger != want {
		t.Fatalf("MaxInteger=%d must equal 2^53-1=%d", MaxInteger, want)
	}
	if int64(MaxSafeInteger) != MaxInteger {
		t.Fatal("untyped MaxSafeInteger and typed MaxInteger diverge")
	}
}
