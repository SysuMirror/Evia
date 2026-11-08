package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeRejectsAmbiguousOrUnboundedJSON(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate":        `{"uid":"a","uid":"b"}`,
		"nested duplicate": `{"x":{"n":1,"n":2}}`,
		"trailing":         `{} {}`,
		"deep":             strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34),
		"utf8":             "{\"x\":\"\xff\"}",
		"NUL string":       `{"x":"before\u0000after"}`,
		"NUL object key":   `{"before\u0000after":"value"}`,
		"nested NUL":       `{"x":[{"value":"\u0000"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(raw)); err == nil {
				t.Fatal("accepted invalid JSON")
			}
		})
	}
	value, err := Decode([]byte(`{"flag":false,"count":0,"missing":null}`))
	if err != nil || len(value.(map[string]any)) != 3 {
		t.Fatalf("valid values lost: %v", err)
	}
}

func TestGoldenContracts(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for file, schema := range map[string]string{
		"collect-browser-request.json":  "CollectRequest",
		"collect-server-request.json":   "CollectRequest",
		"collect-partial-response.json": "CollectResponse",
		"learning-site-event-plan.json": "EventPlan",
		"journey-query.json":            "QueryRequest",
		"journey-result.json":           "QueryResult",
	} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("../../../../contracts/fixtures", file))
			if err != nil {
				t.Fatal(err)
			}
			value, err := Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := v.Validate(schema, value); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestContractRejectsUnknownFieldsAndUnsafeValues(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"unknown": `{"name":"forum","timezone":"UTC","is_admin":true}`,
		"missing": `{"name":"forum"}`,
		"empty":   `{"name":"","timezone":"UTC"}`,
	} {
		t.Run(name, func(t *testing.T) {
			value, err := Decode([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if err := v.Validate("ProjectInput", value); err == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
	if _, err := v.Encode("Problem", map[string]any{"code": "X"}); err == nil {
		t.Fatal("accepted invalid response")
	}
}

func TestConsoleReadRevisionAndDetailsContracts(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, schema, raw string
		valid             bool
	}{
		{"no activation", "Activation", `{"environment":"development","active_release_revision":0}`, true},
		{"safe maximum", "Activation", `{"environment":"production","active_release_revision":9007199254740991}`, true},
		{"negative", "Activation", `{"environment":"development","active_release_revision":-1}`, false},
		{"unsafe", "Activation", `{"environment":"development","active_release_revision":9007199254740992}`, false},
		{"fraction", "Activation", `{"environment":"development","active_release_revision":0.5}`, false},
		{"published version", "EventVersion", `{"event_key":"opened","schema_version":1,"draft_revision":2,"definition":{"schema":{"type":"object","properties":{"title":{"type":"string"}}},"field_metadata":{"title":{"label":"Title"}}}}`, true},
		{"unpublished version", "EventVersion", `{"event_key":"opened","schema_version":0,"draft_revision":2,"definition":{"schema":{}}}`, false},
		{"field paths", "Problem", `{"code":"INVALID_EVENT_DEFINITION","message":"无效","request_id":"request-1","details":{"field_paths":["/schema"]}}`, true},
		{"invalid field path type", "Problem", `{"code":"INVALID_EVENT_DEFINITION","message":"无效","request_id":"request-1","details":{"field_paths":[1]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := Decode([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if err = v.Validate(tc.schema, value); (err == nil) != tc.valid {
				t.Fatalf("expected valid=%v, got %v", tc.valid, err)
			}
		})
	}
}
