//go:build integration

package analysis

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// encodedPropertyKey mirrors propertylayout.EncodeKey without making the
// analysis package depend on the pipeline subtree; the production reader
// introduces that shared-kernel dependency deliberately in a later change.
func encodedPropertyKey(key string) string {
	return "p_" + strings.ToUpper(hex.EncodeToString([]byte(key)))
}

type viewLayoutRow struct {
	PropertiesJSON string            `json:"properties_json"`
	LayoutVersion  uint8             `json:"property_layout_version"`
	Tokens         map[string]string `json:"property_tokens"`
}

func readViewLayout(t *testing.T, client *ClickHouse, eventID string) viewLayoutRow {
	t.Helper()
	data := chExec(t, client, "SELECT properties_json, property_layout_version, property_tokens "+
		"FROM event_facts WHERE event_id='"+eventID+"' FORMAT JSONEachRow")
	var row viewLayoutRow
	if err := json.Unmarshal(data, &row); err != nil {
		t.Fatal(err, string(data))
	}
	return row
}

func TestClickHousePropertyLayoutViewColumnsAndWinner(t *testing.T) {
	client := clickHouseDatabase(t)
	// The view must expose the two layout columns with the exact raw types.
	type columnType struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	raw := chExec(t, client, "SELECT name, type FROM system.columns WHERE database=currentDatabase() "+
		"AND table='event_facts' AND name IN ('property_layout_version','property_tokens') ORDER BY name FORMAT JSONEachRow")
	var types []columnType
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var column columnType
		if err := json.Unmarshal([]byte(line), &column); err != nil {
			t.Fatal(err, line)
		}
		types = append(types, column)
	}
	wantTypes := map[string]string{"property_layout_version": "UInt8", "property_tokens": "Map(String, String)"}
	if len(types) != len(wantTypes) {
		t.Fatalf("view layout columns = %v, want %d columns", types, len(wantTypes))
	}
	for _, column := range types {
		if wantTypes[column.Name] != column.Type {
			t.Fatalf("view column %s type = %q, want %q", column.Name, column.Type, wantTypes[column.Name])
		}
	}

	// Identity A: the earliest receipt wins and carries layout version 1.
	idA := "aaaaaaaa-1111-4111-8111-111111111111"
	winnerA := fact(idA, "sys.page_view", "user-one", "", "source-one", 10, map[string]any{"exp": 100, "title": "Home"})
	winnerTokens := map[string]string{
		encodedPropertyKey("exp"):   "1e2",
		encodedPropertyKey("title"): `"Home"`,
	}
	winnerA["property_layout_version"] = 1
	winnerA["property_tokens"] = winnerTokens
	loserA := fact(idA, "sys.page_view", "user-one", "", "source-one", 20, map[string]any{"exp": 200})
	seedRows(t, client, []map[string]any{winnerA, loserA})
	viewA := readViewLayout(t, client, idA)
	if viewA.LayoutVersion != 1 {
		t.Fatalf("identity A view version = %d, want winner version 1", viewA.LayoutVersion)
	}
	if len(viewA.Tokens) != len(winnerTokens) {
		t.Fatalf("identity A view tokens = %v, want winner tokens %v", viewA.Tokens, winnerTokens)
	}
	for key, token := range winnerTokens {
		if viewA.Tokens[key] != token {
			t.Fatalf("identity A view token %s = %q, want %q", key, viewA.Tokens[key], token)
		}
	}
	winnerDoc := winnerA["properties_json"].(string)
	if viewA.PropertiesJSON != winnerDoc {
		t.Fatalf("identity A view document = %s, want winner document %s", viewA.PropertiesJSON, winnerDoc)
	}

	// Identity B: the earliest receipt is a legacy row (version 0); the later
	// version-1 replay must not leak its layout into the winning tuple.
	idB := "bbbbbbbb-2222-4222-8222-222222222222"
	winnerB := fact(idB, "sys.page_view", "user-two", "", "source-one", 10, map[string]any{"legacy": true})
	loserB := fact(idB, "sys.page_view", "user-two", "", "source-one", 20, map[string]any{"legacy": false})
	loserB["property_layout_version"] = 1
	loserB["property_tokens"] = map[string]string{encodedPropertyKey("legacy"): "false"}
	seedRows(t, client, []map[string]any{winnerB, loserB})
	viewB := readViewLayout(t, client, idB)
	if viewB.LayoutVersion != 0 {
		t.Fatalf("identity B view version = %d, want legacy winner version 0", viewB.LayoutVersion)
	}
	if len(viewB.Tokens) != 0 {
		t.Fatalf("identity B view tokens = %v, want empty legacy map", viewB.Tokens)
	}
	legacyDoc := winnerB["properties_json"].(string)
	if viewB.PropertiesJSON != legacyDoc {
		t.Fatalf("identity B view document = %s, want legacy winner document %s", viewB.PropertiesJSON, legacyDoc)
	}

	// The two identities remain independently deduplicated.
	if got := string(chExec(t, client, "SELECT count() FROM event_facts FORMAT TSV")); got != "2\n" {
		t.Fatalf("view fact count = %q, want 2", got)
	}
}
