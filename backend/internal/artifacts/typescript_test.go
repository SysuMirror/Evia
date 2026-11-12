package artifacts

import (
	"strings"
	"testing"
)

func samplePlan() ReleasePlan {
	return ReleasePlan{
		ReleaseID:    "11111111-1111-4111-8111-111111111111",
		Revision:     3,
		Environment:  "production",
		ManifestHash: strings.Repeat("a", 64),
		Events: []EventSchema{
			{
				Key:           "lesson_opened",
				SchemaVersion: 2,
				DisplayName:   "Lesson opened",
				Schema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"lesson_id", "channel"},
					"properties": map[string]any{
						"lesson_id": map[string]any{"type": "integer", "minimum": float64(1)},
						"channel": map[string]any{
							"type": "string",
							"enum": []any{"web", "mobile"},
						},
						"score":    map[string]any{"type": "number"},
						"is_first": map[string]any{"type": "boolean"},
						"tags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": float64(20)},
						"note":     map[string]any{"type": "string", "maxLength": float64(1024)},
					},
				},
				AllowedSourceKinds: []string{"browser", "server"},
				FactKind:           "user_intent",
			},
			{
				Key:           "quiz_answered",
				SchemaVersion: 1,
				DisplayName:   "Quiz answered",
				Schema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"quiz_id": map[string]any{"type": "integer"},
					},
				},
			},
		},
	}
}

func TestGenerateIsDeterministicAndComplete(t *testing.T) {
	first, err := Generate(samplePlan())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(samplePlan())
	if err != nil {
		t.Fatal(err)
	}
	if first.Source != second.Source || first.Checksum != second.Checksum {
		t.Fatal("artifact output must be deterministic for the same manifest")
	}
	if first.EventCount != 2 || len(first.Compatibility) != 2 || first.ByteSize != len(first.Source) {
		t.Fatalf("artifact metadata wrong: %+v", first)
	}
	if !strings.Contains(first.Source, "export interface LessonOpenedProperties {") {
		t.Fatalf("missing lesson interface:\n%s", first.Source)
	}
	if !strings.Contains(first.Source, "  lesson_id: number;") {
		t.Fatalf("required integer property wrong:\n%s", first.Source)
	}
	if !strings.Contains(first.Source, "  note?: string;") {
		t.Fatalf("optional string property wrong:\n%s", first.Source)
	}
	if !strings.Contains(first.Source, "  channel: \"mobile\" | \"web\";") {
		t.Fatalf("string enum union wrong:\n%s", first.Source)
	}
	if !strings.Contains(first.Source, "  is_first?: boolean;") {
		t.Fatalf("boolean property wrong:\n%s", first.Source)
	}
	if !strings.Contains(first.Source, "  tags?: (string)[];") {
		t.Fatalf("array property wrong:\n%s", first.Source)
	}
	if !strings.Contains(first.Source, "export type EventKey = (typeof eventKeys)[number];") {
		t.Fatalf("missing event key union:\n%s", first.Source)
	}
	if !strings.Contains(first.Source, "  \"lesson_opened\": LessonOpenedProperties;") {
		t.Fatalf("missing property map entry:\n%s", first.Source)
	}
	// Events are sorted alphabetically and interfaces render before the key constants.
	if strings.Index(first.Source, "LessonOpenedProperties") >= strings.Index(first.Source, "QuizAnsweredProperties") {
		t.Fatal("interfaces must be sorted by event key")
	}
	// No host-local or time-dependent content.
	if strings.Contains(first.Source, "time.Now") || strings.Contains(first.Source, "\t") {
		t.Fatal("artifact must be pure manifest data")
	}
}

func TestGenerateRejectsInvalidManifests(t *testing.T) {
	for _, mutate := range []func(*ReleasePlan){
		func(p *ReleasePlan) { p.Events = nil },
		func(p *ReleasePlan) { p.Events = append(p.Events, p.Events[0]) },
		func(p *ReleasePlan) { p.Events[0].Schema["type"] = "array" },
		func(p *ReleasePlan) {
			p.Events[0].Schema["properties"].(map[string]any)["bad"] = map[string]any{"type": "object"}
		},
	} {
		plan := samplePlan()
		mutate(&plan)
		if _, err := Generate(plan); err == nil {
			t.Fatalf("invalid plan accepted: %+v", plan)
		}
	}
}

func TestInterfaceNamesAreUniqueAndStable(t *testing.T) {
	plan := samplePlan()
	plan.Events = []EventSchema{
		{Key: "a_b", Schema: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Key: "ab", Schema: map[string]any{"type": "object", "properties": map[string]any{}}},
	}
	artifact, err := Generate(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(artifact.Source, "export interface ABProperties {") ||
		!strings.Contains(artifact.Source, "export interface AbProperties {") {
		t.Fatalf("distinct snake keys must map to distinct PascalCase names:\n%s", artifact.Source)
	}
}

func TestEmptyObjectRendersEmptyInterface(t *testing.T) {
	plan := samplePlan()
	plan.Events = []EventSchema{{Key: "empty_event", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}}
	artifact, err := Generate(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(artifact.Source, "export interface EmptyEventProperties {\n}") {
		t.Fatalf("empty interface wrong:\n%s", artifact.Source)
	}
}

func TestNumericAndBooleanEnums(t *testing.T) {
	plan := samplePlan()
	plan.Events = []EventSchema{{
		Key: "scored",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"level": map[string]any{"type": "integer", "enum": []any{float64(1), float64(2), float64(3)}},
				"ok":    map[string]any{"type": "boolean", "enum": []any{true, false}},
			},
		},
	}}
	artifact, err := Generate(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(artifact.Source, "  level?: 1 | 2 | 3;") || !strings.Contains(artifact.Source, "  ok?: false | true;") {
		t.Fatalf("literal unions wrong:\n%s", artifact.Source)
	}
}
