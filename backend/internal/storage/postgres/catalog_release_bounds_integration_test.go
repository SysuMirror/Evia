//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

func TestCombinedReleaseBudgetRollsBackEveryPublicationEffect(t *testing.T) {
	f := catalogDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	definition := publishable()
	properties := map[string]any{}
	definition.ExampleProperties = map[string]any{}
	definition.FieldMetadata = map[string]any{}
	for i := range 50 {
		key := fmt.Sprintf("field_%02d", i)
		properties[key] = map[string]any{"type": "string", "maxLength": 1024, "description": strings.Repeat("x", 1200)}
		definition.ExampleProperties[key] = "value"
	}
	for i := range 15 {
		definition.FieldMetadata[fmt.Sprintf("field_%02d", i)] = strings.Repeat("m", 2000)
	}
	definition.Schema = map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if issues := catalog.CheckDefinition(definition, true); len(issues) != 0 {
		t.Fatal("the individual event must be valid", issues)
	}
	data, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	input := catalog.ReleaseInput{Environment: "development"}
	for i := range 200 {
		key := fmt.Sprintf("large_%03d", i)
		// Seed valid independent drafts in a dedicated synthetic schema; release
		// creation itself uses the production authorization/transaction path.
		if _, err = f.s.Pool.Exec(ctx, `INSERT INTO catalog_events(project_id,event_key,display_name,draft_definition) VALUES($1,$2,$2,$3)`, f.project, key, data); err != nil {
			t.Fatal(err)
		}
		input.Events = append(input.Events, catalog.ReleaseEvent{Key: key, DraftRevision: 1})
	}
	_, err = f.svc.CreateRelease(ctx, f.owner, f.project, "combined-budget", input)
	faultCode(t, err, "RELEASE_MANIFEST_TOO_LARGE")
	for _, table := range []string{"catalog_versions", "catalog_releases", "catalog_release_events", "catalog_release_delivery", "catalog_idempotency", "catalog_outbox", "catalog_environments"} {
		var count int
		if err = f.s.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE project_id=$1", f.project).Scan(&count); err != nil || count != 0 {
			t.Fatal("rejected publication left effects", table, count, err)
		}
	}
	var audits int
	if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_entries WHERE project_id=$1 AND action='catalog.release_built'`, f.project).Scan(&audits); err != nil || audits != 0 {
		t.Fatal("rejected publication left an audit success", audits, err)
	}
	input.Events = input.Events[:1]
	release, err := f.svc.CreateRelease(ctx, f.owner, f.project, "combined-budget", input)
	if err != nil || release.Revision != 1 {
		t.Fatal("rejected publication consumed idempotency or revision", release.Revision, err)
	}
}

func TestReleaseReadinessReflectsCurrentDatabaseLease(t *testing.T) {
	f := catalogDB(t)
	ctx := context.Background()
	draft := f.event(t, "lease_checked")
	release, err := f.svc.CreateRelease(ctx, f.owner, f.project, "lease-read", releaseInput("lease_checked", draft.Revision))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE catalog_release_delivery SET ingestion_ready=true WHERE release_id=$1`, release.ID); err != nil {
		t.Fatal(err)
	}
	assertReady := func(want bool) {
		t.Helper()
		got, err := f.svc.GetRelease(ctx, f.owner, f.project, release.ID)
		if err != nil || got.IngestionReady != want {
			t.Fatal("detail readiness does not reflect lease", got.IngestionReady, want, err)
		}
		items, err := f.svc.ListReleases(ctx, f.owner, f.project, "development", "", 10)
		if err != nil || len(items) != 1 || items[0].IngestionReady != want {
			t.Fatal("list readiness does not reflect lease", want, err)
		}
	}
	assertReady(false)
	if _, err = f.s.Pool.Exec(ctx, `INSERT INTO catalog_release_readiness(release_id,project_id,environment,manifest_hash,nonce,capability,confirmed_at,ready_until) VALUES($1,$2,'development',$3,$4,'raw-facts-v1',clock_timestamp(),clock_timestamp()+interval '1 minute')`, release.ID, f.project, release.ManifestHash, ids.New()); err != nil {
		t.Fatal(err)
	}
	assertReady(true)
	if _, err = f.s.Pool.Exec(ctx, `UPDATE catalog_release_readiness SET confirmed_at=clock_timestamp()-interval '2 minutes',ready_until=clock_timestamp()-interval '1 minute' WHERE release_id=$1`, release.ID); err != nil {
		t.Fatal(err)
	}
	assertReady(false)
	if _, err = f.s.Pool.Exec(ctx, `UPDATE catalog_release_readiness SET ready_until=clock_timestamp()+interval '1 minute',manifest_hash=$2 WHERE release_id=$1`, release.ID, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	assertReady(false)
}
