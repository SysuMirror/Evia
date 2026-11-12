package propertycatalog

import (
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/propertylayout"
)

func TestViewDDLDisabledKeepsFixedTwentyNineSlots(t *testing.T) {
	ddl := Disabled().ViewDDL()
	if !strings.HasPrefix(ddl, "CREATE OR REPLACE VIEW event_facts AS") {
		t.Fatalf("view DDL has wrong header: %s", ddl)
	}
	for fact := 1; fact <= 29; fact++ {
		needle := "fact." + itoa(fact)
		if !strings.Contains(ddl, needle) {
			t.Fatalf("disabled view missing fixed %s", needle)
		}
	}
	if strings.Contains(ddl, "fact.30") {
		t.Fatal("disabled view must not project slot columns")
	}
	if !strings.Contains(ddl, "fact.29 AS dedicated_layout_version") {
		t.Fatal("disabled view must retain the dedicated marker as fact.29")
	}
	if !strings.Contains(ddl, "GROUP BY project_id, environment, source_id, event_id") {
		t.Fatal("view DDL lost the dedup grouping")
	}
}

func TestViewDDLAppendsSlotsAtFactThirty(t *testing.T) {
	catalog := validConfig(t, 4,
		entry("page_opened", 1, "alpha", "string"),
		entry("page_opened", 1, "zebra", "integer"),
	)
	ddl := catalog.ViewDDL()
	alphaColumn := SlotColumn("alpha")
	zebraColumn := SlotColumn("zebra")
	if !strings.Contains(ddl, "fact.30 AS "+alphaColumn) {
		t.Fatalf("first slot must project at fact.30:\n%s", ddl)
	}
	if !strings.Contains(ddl, "fact.31 AS "+zebraColumn) {
		t.Fatalf("second slot must project at fact.31:\n%s", ddl)
	}
	if !strings.Contains(ddl, "dedicated_layout_version, "+alphaColumn+", "+zebraColumn+")") {
		t.Fatal("argMin tuple must carry slot columns after the marker")
	}
	// Encoded identifiers only; raw property text never appears as a column.
	if strings.Contains(ddl, " AS alpha") || strings.Contains(ddl, " AS zebra") {
		t.Fatal("physical columns must use encoded names, not user property text")
	}
}

func TestMigrationSQLContainsRawColumnsAndView(t *testing.T) {
	catalog := validConfig(t, 2, entry("page_opened", 1, "score", "integer"))
	sql := catalog.MigrationSQL()
	column := propertylayout.EncodeKey("score")
	wantDDL := "ALTER TABLE event_facts_raw ADD COLUMN IF NOT EXISTS " + column + " String DEFAULT ''"
	if !strings.Contains(sql, wantDDL) {
		t.Fatalf("migration missing raw slot DDL:\n%s", sql)
	}
	if !strings.Contains(sql, "CREATE OR REPLACE VIEW event_facts AS") {
		t.Fatal("migration must recreate the view")
	}
	if !strings.Contains(sql, "Catalog revision 2, 1 slot column(s).") {
		t.Fatal("migration header must record revision and slot count")
	}
}

func TestMigrationSQLIsOrderIndependentAndDeterministic(t *testing.T) {
	first := validConfig(t, 1,
		entry("e", 1, "zebra", "string"),
		entry("e", 1, "alpha", "string"),
		entry("e", 1, "middle", "string"),
	)
	second := validConfig(t, 1,
		entry("e", 1, "middle", "string"),
		entry("e", 1, "zebra", "string"),
		entry("e", 1, "alpha", "string"),
	)
	if first.MigrationSQL() != second.MigrationSQL() {
		t.Fatal("migration SQL must not depend on catalog entry order")
	}
	if first.ViewDDL() != second.ViewDDL() {
		t.Fatal("view DDL must not depend on catalog entry order")
	}
}

func TestViewDDLUsesSharedEncoding(t *testing.T) {
	catalog := validConfig(t, 1, entry("e", 1, "segment_03", "string"))
	ddl := catalog.ViewDDL()
	encoded := propertylayout.EncodeKey("segment_03")
	if !strings.Contains(ddl, encoded) {
		t.Fatalf("view DDL must use the shared encoded column %s", encoded)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
