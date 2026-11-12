package propertycatalog

import (
	"encoding/json"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/propertylayout"
)

func derivedMap(t *testing.T, document string) map[string]json.RawMessage {
	t.Helper()
	derived, err := propertylayout.DeriveMap([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	return derived
}

func TestPlanDisabledIsEmpty(t *testing.T) {
	plan := Disabled().Plan("page_opened", 1, derivedMap(t, `{"score":1}`))
	if plan.Marker != 0 || len(plan.Slots) != 0 {
		t.Fatalf("disabled plan must be the zero shape, got %+v", plan)
	}
}

func TestPlanMaterializesMatchingIdentityWithExactToken(t *testing.T) {
	catalog := validConfig(t, 3,
		entry("page_opened", 1, "score", "integer"),
		entry("page_opened", 1, "title", "string"),
	)
	derived := derivedMap(t, `{"score":42,"title":"Home"}`)
	plan := catalog.Plan("page_opened", 1, derived)
	if plan.Marker != 3 {
		t.Fatalf("marker = %d, want catalog revision 3", plan.Marker)
	}
	if len(plan.Slots) != len(catalog.Columns()) {
		t.Fatalf("slot count = %d, want %d columns", len(plan.Slots), len(catalog.Columns()))
	}
	byColumn := map[string]SlotValue{}
	for _, slot := range plan.Slots {
		byColumn[slot.Column] = slot
	}
	score := byColumn[SlotColumn("score")]
	if !score.Materialized || score.Token != "42" {
		t.Fatalf("score slot wrong: %+v", score)
	}
	title := byColumn[SlotColumn("title")]
	if !title.Materialized || title.Token != `"Home"` {
		t.Fatalf("title slot wrong: %+v", title)
	}
}

func TestPlanDistinguishesAbsentPropertyFromEmptyStringToken(t *testing.T) {
	catalog := validConfig(t, 1,
		entry("page_opened", 1, "label", "string"),
		entry("page_opened", 1, "missing", "string"),
	)
	// label is present but an empty JSON string; missing is not in the document.
	derived := derivedMap(t, `{"label":""}`)
	plan := catalog.Plan("page_opened", 1, derived)
	byColumn := map[string]SlotValue{}
	for _, slot := range plan.Slots {
		byColumn[slot.Column] = slot
	}
	label := byColumn[SlotColumn("label")]
	if !label.Materialized || label.Token != `""` || len(label.Token) != 2 {
		t.Fatalf("empty-string property must stay materialized as a two-byte token: %+v", label)
	}
	missing := byColumn[SlotColumn("missing")]
	if missing.Materialized || missing.Token != "" {
		t.Fatalf("absent property must be a zero-byte, unmaterialized slot: %+v", missing)
	}
}

func TestPlanIgnoresEntriesForOtherEvents(t *testing.T) {
	catalog := validConfig(t, 1,
		entry("page_opened", 1, "score", "integer"),
		entry("button_clicked", 1, "score", "integer"),
	)
	// A page_opened row must only read its own entry, even though both events
	// share the score column.
	derived := derivedMap(t, `{"score":7}`)
	plan := catalog.Plan("page_opened", 1, derived)
	if len(plan.Slots) != 1 {
		t.Fatalf("shared column must resolve to one slot, got %d", len(plan.Slots))
	}
	if !plan.Slots[0].Materialized || plan.Slots[0].Token != "7" {
		t.Fatalf("shared column not materialized for the matching event: %+v", plan.Slots[0])
	}
	// A row for an event with no catalog entry keeps the marker but leaves the
	// column unmaterialized, so the reader falls back to the document.
	other := catalog.Plan("unknown_event", 1, derived)
	if other.Marker != 1 || len(other.Slots) != 1 || other.Slots[0].Materialized {
		t.Fatalf("unmatched event must keep marker with unmaterialized slot: %+v", other)
	}
}

func TestPlanRespectsSchemaVersion(t *testing.T) {
	catalog := validConfig(t, 1, entry("page_opened", 2, "score", "integer"))
	derived := derivedMap(t, `{"score":7}`)
	plan := catalog.Plan("page_opened", 1, derived)
	if plan.Slots[0].Materialized {
		t.Fatal("schema version 1 row must not materialize a version-2 catalog entry")
	}
	if plan.Marker != 1 {
		t.Fatal("marker still records the active catalog revision even without a matching entry")
	}
}

func TestPlanDoesNotMutateDerivedMap(t *testing.T) {
	catalog := validConfig(t, 1, entry("page_opened", 1, "score", "integer"))
	derived := derivedMap(t, `{"score":7}`)
	before := string(derived[SlotColumn("score")])
	_ = catalog.Plan("page_opened", 1, derived)
	if string(derived[SlotColumn("score")]) != before {
		t.Fatal("Plan mutated the derived token map")
	}
}

func TestSlotDDLDisabledReturnsNil(t *testing.T) {
	if statements := Disabled().SlotDDL(); statements != nil {
		t.Fatalf("disabled catalog must generate no DDL, got %v", statements)
	}
}

func TestSlotDDLRendersSortedIdempotentAdditiveStatements(t *testing.T) {
	catalog := validConfig(t, 1,
		entry("e", 1, "zebra", "string"),
		entry("e", 1, "alpha", "string"),
	)
	statements := catalog.SlotDDL()
	if len(statements) != 2 {
		t.Fatalf("expected two column statements, got %d: %v", len(statements), statements)
	}
	wantFirst := "ALTER TABLE event_facts_raw ADD COLUMN IF NOT EXISTS " + SlotColumn("alpha") + " String DEFAULT ''"
	wantSecond := "ALTER TABLE event_facts_raw ADD COLUMN IF NOT EXISTS " + SlotColumn("zebra") + " String DEFAULT ''"
	if statements[0] != wantFirst || statements[1] != wantSecond {
		t.Fatalf("DDL not sorted or malformed:\n%v", statements)
	}
}

func TestSlotDDLIsOrderIndependentAndDeterministic(t *testing.T) {
	first := validConfig(t, 1,
		entry("e", 1, "zebra", "string"),
		entry("e", 1, "alpha", "string"),
		entry("e", 1, "middle", "string"),
	)
	second := validConfig(t, 1,
		entry("e", 1, "middle", "string"),
		entry("e", 1, "alpha", "string"),
		entry("e", 1, "zebra", "string"),
	)
	a, b := first.SlotDDL(), second.SlotDDL()
	if len(a) != len(b) {
		t.Fatal("DDL length differs for equivalent catalogs")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("DDL statement %d depends on authoring order", i)
		}
	}
}

func TestSlotDDLUsesEncodedIdentifiers(t *testing.T) {
	catalog := validConfig(t, 1, entry("e", 1, "segment_03", "string"))
	statements := catalog.SlotDDL()
	if len(statements) != 1 || statements[0] !=
		"ALTER TABLE event_facts_raw ADD COLUMN IF NOT EXISTS "+propertylayout.EncodeKey("segment_03")+" String DEFAULT ''" {
		t.Fatalf("DDL must use the shared encoded column name: %v", statements)
	}
}
