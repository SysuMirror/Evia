package propertylayout

import (
	"encoding/json"
	"strings"
	"testing"
)

func hexKey(t *testing.T, key string) string {
	t.Helper()
	encoded := EncodeKey(key)
	if !strings.HasPrefix(encoded, KeyPrefix) {
		t.Fatalf("encoded key %q missing prefix", encoded)
	}
	decoded, err := DecodeKey(encoded)
	if err != nil || decoded != key {
		t.Fatalf("key round trip failed: %q -> %q (err %v)", key, decoded, err)
	}
	return encoded
}

func TestEncodeKeyIsReversibleAndCollisionFree(t *testing.T) {
	first := hexKey(t, "page.title")
	second := hexKey(t, "page_title")
	if first == second {
		t.Fatal("dotted and underscored keys must encode differently")
	}
	for _, char := range []string{".", " ", "中文", "a-b", "x.y.z"} {
		hexKey(t, char)
	}
	if _, err := DecodeKey("not-prefixed"); err == nil {
		t.Fatal("expected decode rejection without prefix")
	}
	if _, err := DecodeKey(KeyPrefix + "zz"); err == nil {
		t.Fatal("expected decode rejection for non-hex body")
	}
}

func TestDeriveMapPreservesLexicalTokens(t *testing.T) {
	// Every value must survive as its exact input token; numeric text must not
	// be normalized through a Go float or ClickHouse JSON type.
	document := []byte(`{
		"exp": 1e2,
		"decimal": 1.00,
		"long": 0.1234567890123456789,
		"negative_zero": -0.0,
		"big_int": 9007199254740993,
		"text": "1e2",
		"flag": false,
		"zero": 0,
		"nullish": null,
		"array": [-0.0, 1, "1.00"],
		"object": {"nested": "1.00"}
	}`)
	derived, err := DeriveMap(document)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"exp":           "1e2",
		"decimal":       "1.00",
		"long":          "0.1234567890123456789",
		"negative_zero": "-0.0",
		"big_int":       "9007199254740993",
		"text":          `"1e2"`,
		"flag":          "false",
		"zero":          "0",
		"nullish":       "null",
		"array":         `[-0.0, 1, "1.00"]`,
		"object":        `{"nested": "1.00"}`,
	}
	for key, want := range cases {
		got := strings.TrimSpace(string(derived[hexKey(t, key)]))
		if got != want {
			t.Fatalf("property %q token changed: got %s want %s", key, got, want)
		}
	}
}

func TestDeriveMapRoundTripsThroughRawJSON(t *testing.T) {
	document := []byte(`{"ratio":1e2,"name":"home"}`)
	derived, err := DeriveMap(document)
	if err != nil {
		t.Fatal(err)
	}
	// Re-marshalling a RawMessage emits its stored bytes unchanged.
	for _, token := range derived {
		var again json.RawMessage
		if err := json.Unmarshal(token, &again); err != nil {
			t.Fatal(err)
		}
		if string(again) != string(token) {
			t.Fatalf("raw token not stable: %s != %s", again, token)
		}
	}
}

func TestDeriveMapRejectsInvalidShapes(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":          ``,
		"not object":     `[1,2,3]`,
		"scalar":         `"x"`,
		"duplicate keys": `{"a":1,"a":2}`,
		"trailing data":  `{"a":1}{"b":2}`,
		"unterminated":   `{"a":1`,
		"bad json":       `{"a":}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DeriveMap([]byte(raw)); err == nil {
				t.Fatal("expected rejection for", name)
			}
		})
	}
}

func TestDeriveMapHandlesEmptyObjectAndCopiesTokens(t *testing.T) {
	derived, err := DeriveMap([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(derived) != 0 {
		t.Fatalf("empty object should derive no entries, got %d", len(derived))
	}

	document := []byte(`{"a":1}`)
	first, err := DeriveMap(document)
	if err != nil {
		t.Fatal(err)
	}
	token := first[hexKey(t, "a")]
	// Mutating the returned token must not corrupt a second derivation backed
	// by a different array.
	token[0] = '9'
	second, err := DeriveMap(document)
	if err != nil {
		t.Fatal(err)
	}
	if string(second[hexKey(t, "a")]) != "1" {
		t.Fatal("derived token aliases the input buffer")
	}
}

func TestDeriveMapRejectsExcessiveProperties(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("{")
	for i := 0; i <= MaxTopLevelProperties; i++ {
		if i > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(`"k`)
		builder.WriteString(strings.Repeat("x", i))
		builder.WriteString(`":1`)
	}
	builder.WriteString("}")
	if _, err := DeriveMap([]byte(builder.String())); err == nil {
		t.Fatal("expected property count limit rejection")
	}
}
