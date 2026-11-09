//go:build integration

package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func identityQuery(t *testing.T, client *ClickHouse, sql string, parameters map[string]string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, err := client.request(ctx, sql, "evia-identity-test-"+ids.New(), parameters, true)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil || response.StatusCode != http.StatusOK || len(data) > 4*1024*1024 {
		t.Fatalf("identity query failed: status=%d err=%v body=%s", response.StatusCode, err, data)
	}
	return data
}

func identityPlanRows(t *testing.T, client *ClickHouse, r Request, table string, disabled bool) []map[string]any {
	t.Helper()
	plan, err := Compile(client.database, table, Scope{ProjectID: "project-one", Environment: r.Environment}, r)
	if err != nil {
		t.Fatal(err)
	}
	if disabled {
		plan.SQL += " SETTINGS optimize_use_projections=0"
	}
	decoder := json.NewDecoder(bytes.NewReader(identityQuery(t, client, plan.SQL+" FORMAT JSONEachRow", plan.Parameters)))
	decoder.UseNumber()
	rows := []map[string]any{}
	for {
		var row map[string]any
		if err = decoder.Decode(&row); err == io.EOF {
			return rows
		} else if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
}

func addIdentityProjections(t *testing.T, client *ClickHouse) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "clickhouse", "005_identity_projections.sql"))
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			lines = append(lines, line)
		}
	}
	for _, statement := range strings.Split(strings.Join(lines, "\n"), ";") {
		if strings.TrimSpace(statement) != "" {
			chExec(t, client, statement)
		}
	}
}

func TestClickHouseIdentityCandidatesPreserveFullWinner(t *testing.T) {
	client := clickHouseDatabase(t)
	chExec(t, client, "CREATE VIEW event_facts_reference AS SELECT * FROM event_facts")
	chExec(t, client, "ALTER TABLE event_facts_raw DROP PROJECTION identity_uid, DROP PROJECTION identity_anonymous_id")
	winner := fact("conflict", "opened", "winner", "anon-winner", "source-one", 1, nil)
	loser := fact("conflict", "closed", "loser", "anon-loser", "source-one", 20, nil)
	otherSource := fact("conflict", "opened", "loser", "anon-loser", "source-two", 2, nil)
	otherProject := cloneTrendFact(otherSource)
	otherProject["project_id"] = "project-two"
	otherEnvironment := cloneTrendFact(otherSource)
	otherEnvironment["environment"] = "production"
	nullWinner := fact("null", "opened", "", "", "source-one", 3, nil)
	nullLoser := fact("null", "opened", "late", "anon-late", "source-one", 21, nil)
	legacy := fact("precision", "opened", "legacy", "anon-legacy", "source-one", 4, nil)
	legacy["occurred_at_precise"], legacy["event_time_precise"] = nil, nil
	precise := cloneTrendFact(legacy)
	precise["uid"], precise["anonymous_id"] = "precise", "anon-precise"
	precise["occurred_at_precise"], precise["event_time_precise"] = "2026-11-07 10:00:04.000000001", "2026-11-07 10:00:04.000000001"
	seedRows(t, client, []map[string]any{loser, otherSource, otherProject, otherEnvironment, nullLoser, precise, winner, nullWinner, legacy})
	for _, phase := range []string{"absent", "unmaterialized", "materialized", "disabled"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "unmaterialized" {
				addIdentityProjections(t, client)
				// The candidate now exists in a projection-covered new part while
				// its earlier winner remains in a historical fallback part.
				coveredLoser := fact("conflict", "closed", "covered-loser", "covered-anon", "source-one", 22, nil)
				seedRows(t, client, []map[string]any{coveredLoser})
				addIdentityProjections(t, client) // Reapplying populated DDL is safe.
			} else if phase == "materialized" {
				chExec(t, client, "ALTER TABLE event_facts_raw MATERIALIZE PROJECTION identity_uid SETTINGS mutations_sync=2")
				chExec(t, client, "ALTER TABLE event_facts_raw MATERIALIZE PROJECTION identity_anonymous_id SETTINGS mutations_sync=2")
			}
			for _, tc := range []struct {
				name string
				edit func(*Request)
				want int
			}{
				{"UID across sources", func(r *Request) { r.Filter = identityEquality("uid", "loser") }, 1},
				{"anonymous across sources", func(r *Request) { r.Filter = identityEquality("anonymous_id", "anon-loser") }, 1},
				{"new-part losing identity", func(r *Request) { r.Filter = identityEquality("uid", "covered-loser") }, 0},
				{"losing time", func(r *Request) {
					r.Filter = identityEquality("uid", "loser")
					r.TimeRange.From = time.Date(2026, 11, 7, 10, 0, 20, 0, time.UTC)
				}, 0},
				{"null winner", func(r *Request) { uid := "late"; r.IdentityFilter = &IdentityFilter{UID: &uid} }, 0},
				{"precision losing UID", func(r *Request) { r.Filter = identityEquality("uid", "precise") }, 0},
				{"old writer", func(r *Request) { uid := "legacy"; r.IdentityFilter = &IdentityFilter{UID: &uid} }, 1},
				{"explicit anonymous", func(r *Request) { anon := "anon-winner"; r.IdentityFilter = &IdentityFilter{AnonymousID: &anon} }, 1},
				{"UID and anonymous", func(r *Request) {
					r.Filter = &Filter{Op: "and", Args: []*Filter{identityEquality("uid", "loser"), identityEquality("anonymous_id", "anon-winner")}}
				}, 0},
				{"nested AND", func(r *Request) {
					r.Filter = &Filter{Op: "and", Args: []*Filter{identityEquality("event_key", "opened"), {Op: "and", Args: []*Filter{identityEquality("uid", "winner")}}}}
				}, 1},
				{"OR fallback", func(r *Request) {
					r.Filter = &Filter{Op: "or", Args: []*Filter{identityEquality("uid", "winner"), identityEquality("uid", "loser")}}
				}, 2},
			} {
				for _, kind := range []string{"events", "journey", "trend", "funnel"} {
					t.Run(tc.name+"/"+kind, func(t *testing.T) {
						r := request(t, baseRequest)
						r.Kind = kind
						if kind == "funnel" {
							r.Subject = "uid"
							r.Funnel = &Funnel{Steps: []Step{{EventKey: "opened", SchemaVersion: 1}, {EventKey: "closed", SchemaVersion: 1}}, WindowSeconds: 60, Mode: "ordered"}
						}
						tc.edit(&r)
						got := identityPlanRows(t, client, r, "event_facts", phase == "disabled")
						reference := identityPlanRows(t, client, r, "event_facts_reference", phase == "disabled")
						if !reflect.DeepEqual(got, reference) {
							t.Fatalf("candidate query disagrees with full winner: got=%v reference=%v", got, reference)
						}
						if kind == "trend" {
							if len(got) != 1 || got[0]["metric_0"] != json.Number(strconv.Itoa(tc.want)) {
								t.Fatal("unexpected winner count", got, tc.want)
							}
						} else if len(got) != tc.want {
							t.Fatal("unexpected winner rows", got, tc.want)
						}
					})
				}
			}
		})
	}
}

func TestClickHouseIdentityProjectionsPruneCandidates(t *testing.T) {
	client := clickHouseDatabase(t)
	const total = 131072
	chExec(t, client, "INSERT INTO event_facts_raw (project_id,environment,source_id,event_id,event_key,canonical_hash,uid,anonymous_id) SELECT 'project-one','development','source-one',toString(number),'opened',repeat('a',64),toString(number%16384),toString(number%16384) FROM numbers(131072)")
	for _, key := range []string{"uid", "anonymous_id"} {
		t.Run(key, func(t *testing.T) {
			sql := "SELECT source_id,event_id FROM event_facts_raw WHERE project_id='project-one' AND environment='development' AND " + key + "='123'"
			explain := string(chExec(t, client, "EXPLAIN indexes=1,projections=1 "+sql+" SETTINGS use_query_condition_cache=0,use_skip_indexes_on_data_read=0"))
			if !strings.Contains(explain, "ReadFromMergeTree (identity_"+key+")") || !strings.Contains(explain, key+" in [") {
				t.Fatal("identity projection must participate in primary-key pruning", explain)
			}
			rowsRead := func(disabled bool) uint64 {
				settings := " SETTINGS use_query_condition_cache=0,use_skip_indexes_on_data_read=0"
				if disabled {
					settings += ",optimize_use_projections=0"
				}
				var result struct {
					Rows       int `json:"rows"`
					Statistics struct {
						RowsRead uint64 `json:"rows_read"`
					} `json:"statistics"`
				}
				if err := json.Unmarshal(identityQuery(t, client, sql+settings+" FORMAT JSON", nil), &result); err != nil {
					t.Fatal(err)
				}
				if result.Rows != 8 {
					t.Fatal("identity candidate count changed", result)
				}
				return result.Statistics.RowsRead
			}
			projected, raw := rowsRead(false), rowsRead(true)
			if raw != total || projected >= raw/2 {
				t.Fatalf("projection failed to prune actual reads: projected=%d raw=%d", projected, raw)
			}
			t.Logf("candidate rows read: projection=%d disabled=%d", projected, raw)
		})
	}
}
