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
