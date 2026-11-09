//go:build integration

package analysis

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestClickHouseTrendScopeConstantsPreserveAuthorizationAndWinners(t *testing.T) {
	client := clickHouseDatabase(t)
	chExec(t, client, "CREATE VIEW event_facts_reference AS SELECT * FROM event_facts")
	winner := fact("shared", "opened", "winner", "", "source-one", 1, nil)
	loser := fact("shared", "closed", "loser", "", "source-one", 20, nil)
	otherSource := fact("shared", "opened", "other-source", "", "source-two", 2, nil)
	nullWinner := fact("null", "opened", "", "", "source-one", 3, nil)
	nullLoser := fact("null", "opened", "late-nonnull", "", "source-one", 21, nil)
	otherProject := cloneTrendFact(winner)
	otherProject["project_id"], otherProject["uid"] = "project-two", "foreign-project"
	otherProject["received_at"] = "2026-11-07 10:00:00.000000000"
	otherEnvironment := cloneTrendFact(winner)
	otherEnvironment["environment"], otherEnvironment["uid"] = "production", "foreign-environment"
	otherEnvironment["received_at"] = "2026-11-07 10:00:00.000000000"
	foreignUnique := cloneTrendFact(otherProject)
	foreignUnique["event_id"] = "foreign-only"
	seedRows(t, client, []map[string]any{loser, otherSource, nullLoser, otherProject, otherEnvironment, foreignUnique, winner, nullWinner})
	for _, tc := range []struct {
		scope Scope
		count json.Number
	}{
		{Scope{ProjectID: "project-one", Environment: "development"}, "3"},
		{Scope{ProjectID: "project-one", Environment: "production"}, "1"},
		{Scope{ProjectID: "project-two", Environment: "development"}, "2"},
	} {
		t.Run(tc.scope.ProjectID+"/"+tc.scope.Environment, func(t *testing.T) {
			for _, variant := range []string{"count", "UID groups", "losing UID", "nullable loser"} {
				t.Run(variant, func(t *testing.T) {
					r := request(t, baseRequest)
					r.Environment = tc.scope.Environment
					switch variant {
					case "UID groups":
						r.GroupBy = []Field{{Kind: "system", Key: "uid", ResolvedType: "string"}}
					case "losing UID":
						r.Filter = &Filter{Op: "eq", Field: &Field{Kind: "system", Key: "uid", ResolvedType: "string"}, Value: "loser"}
					case "nullable loser":
						uid := "late-nonnull"
						r.Subject, r.IdentityFilter = "uid", &IdentityFilter{UID: &uid}
					}
					read := func(table string) []byte {
						t.Helper()
						plan, err := Compile(client.database, table, tc.scope, r)
						if err != nil {
							t.Fatal(err)
						}
						return identityQuery(t, client, plan.SQL+" FORMAT JSONEachRow", plan.Parameters)
					}
					got, want := read("event_facts"), read("event_facts_reference")
					if !bytes.Equal(got, want) {
						t.Fatalf("scope-constant query differs from authorized full winner: got=%s reference=%s", got, want)
					}
					if variant != "UID groups" {
						var result struct {
							Count json.Number `json:"metric_0"`
						}
						if err := json.Unmarshal(got, &result); err != nil {
							t.Fatal(err)
						}
						expected := json.Number("0")
						if variant == "count" {
							expected = tc.count
						}
						if result.Count != expected {
							t.Fatal("wrong authorized winner count", result.Count, expected)
						}
					}
				})
			}
		})
	}
}
