package contract

import (
	"encoding/json"
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

// TestSystemEventFixtureInvariants pins the reserved sys.* contract that the
// browser SDK producers rely on. The SDK hardcodes these property names and
// closed enum sets, so a fixture edit that drifts from them must fail here
// before it can break ingestion once a system catalog is provisioned.
func TestSystemEventFixtureInvariants(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "contracts", "fixtures", "system-events.json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Format string `json:"format"`
		Events []struct {
			EventKey      string   `json:"event_key"`
			SchemaVersion int      `json:"schema_version"`
			AllowedSource []string `json:"allowed_source_kinds"`
			Schema        struct {
				Additional bool `json:"additionalProperties"`
				Properties map[string]struct {
					Type    string   `json:"type"`
					Enum    []string `json:"enum"`
					Minimum *float64 `json:"minimum"`
					Maximum *float64 `json:"maximum"`
				} `json:"properties"`
				Required []string `json:"required"`
			} `json:"schema"`
		} `json:"events"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Format != "activity.system-events.v1" {
		t.Fatalf("unexpected envelope format %q", envelope.Format)
	}
	wantKeys := map[string]bool{
		"sys.page_view": true, "sys.page_scroll": true, "sys.identity_link": true,
		"sys.session_end": true, "sys.operation_failed": true,
	}
	if len(envelope.Events) != len(wantKeys) {
		t.Fatalf("want %d system events, got %d", len(wantKeys), len(envelope.Events))
	}
	byKey := map[string]int{}
	for i, e := range envelope.Events {
		byKey[e.EventKey] = i
		if !wantKeys[e.EventKey] {
			t.Fatalf("unexpected system event %q", e.EventKey)
		}
		if e.SchemaVersion < 1 {
			t.Fatalf("%s must publish at schema_version >= 1", e.EventKey)
		}
		if len(e.AllowedSource) != 1 || e.AllowedSource[0] != "browser" {
			t.Fatalf("%s must be browser-sourced only, got %v", e.EventKey, e.AllowedSource)
		}
		if e.Schema.Additional {
			t.Fatalf("%s must forbid additional properties", e.EventKey)
		}
	}

	emptyPropertyEvents := []string{"sys.page_view", "sys.identity_link"}
	for _, key := range emptyPropertyEvents {
		if len(envelope.Events[byKey[key]].Schema.Properties) != 0 {
			t.Fatalf("%s must carry empty properties", key)
		}
	}

	sessionEnd := envelope.Events[byKey["sys.session_end"]].Schema
	if got := sessionEnd.Properties["reason"].Enum; len(got) != 3 {
		t.Fatalf("session_end reason enum changed: %v", got)
	}
	for _, want := range []string{"logout", "account_switch", "idle"} {
		if !contains(sessionEnd.Properties["reason"].Enum, want) {
			t.Fatalf("session_end reason enum missing %q", want)
		}
	}

	scroll := envelope.Events[byKey["sys.page_scroll"]].Schema
	for _, ratio := range []string{"from_ratio", "to_ratio", "max_ratio"} {
		prop, ok := scroll.Properties[ratio]
		if !ok || prop.Type != "number" || prop.Minimum == nil || *prop.Minimum != 0 ||
			prop.Maximum == nil || *prop.Maximum != 1 {
			t.Fatalf("scroll %s must be a number bounded to [0,1]", ratio)
		}
	}
	if got := scroll.Properties["direction"].Enum; !equalSet(got, []string{"up", "down", "mixed"}) {
		t.Fatalf("scroll direction enum changed: %v", got)
	}
	if scroll.Properties["duration_ms"].Type != "integer" {
		t.Fatalf("scroll duration_ms must be an integer")
	}

	op := envelope.Events[byKey["sys.operation_failed"]].Schema
	if !equalSet(op.Properties["error_category"].Enum, []string{"validation", "network", "timeout", "http", "business"}) {
		t.Fatalf("operation error_category enum changed: %v", op.Properties["error_category"].Enum)
	}
	if op.Properties["retryable"].Type != "boolean" {
		t.Fatalf("operation retryable must be a boolean")
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func equalSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !contains(got, w) {
			return false
		}
	}
	return true
}
