package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

// ResolvePlan performs one bounded manifest fetch for a request/cache miss,
// never a query for every event. The consumer owns the independent plan shape.
func (s *Store) ResolvePlan(ctx context.Context, project, environment string, revision int64) (ingestion.Plan, error) {
	var p ingestion.Plan
	var data []byte
	var validMicros int64
	e := s.Pool.QueryRow(ctx, `SELECT r.project_id::text,r.environment,r.revision,(d.ingestion_ready AND COALESCE(rd.ready_until>clock_timestamp(),false)),COALESCE((env.active_revision=r.revision OR a.accept_until>clock_timestamp()) AND a.release_id IS NOT NULL,false),r.manifest,COALESCE(LEAST(300000000,GREATEST(0,floor(EXTRACT(EPOCH FROM (LEAST(rd.ready_until,CASE WHEN env.active_revision=r.revision THEN rd.ready_until ELSE a.accept_until END)-clock_timestamp()))*1000000)))::bigint,0) FROM catalog_releases r JOIN catalog_release_delivery d ON d.release_id=r.id AND d.project_id=r.project_id AND d.environment=r.environment LEFT JOIN catalog_release_readiness rd ON rd.release_id=r.id AND rd.manifest_hash=r.manifest_hash LEFT JOIN catalog_release_activations a ON a.release_id=r.id AND a.project_id=r.project_id AND a.environment=r.environment AND a.revision=r.revision LEFT JOIN catalog_environments env ON env.project_id=r.project_id AND env.environment=r.environment WHERE r.project_id=$1 AND r.environment=$2 AND r.revision=$3`, project, environment, revision).Scan(&p.ProjectID, &p.Environment, &p.Revision, &p.Ready, &p.Active, &data, &validMicros)
	if errors.Is(e, pgx.ErrNoRows) {
		return p, fault.New(fault.Invalid, "SCHEMA_NOT_PUBLISHED", "事件规则尚未发布")
	}
	if e != nil {
		return p, e
	}
	if p.Ready && p.Active && validMicros > 0 {
		p.ValidFor = time.Duration(validMicros) * time.Microsecond
	}
	var manifest struct {
		Events []struct {
			Key     string         `json:"event_key"`
			Version int64          `json:"schema_version"`
			Kinds   []string       `json:"allowed_source_kinds"`
			Schema  map[string]any `json:"schema"`
		} `json:"events"`
	}
	if e = json.Unmarshal(data, &manifest); e != nil {
		return p, e
	}
	p.Events = map[string]ingestion.EventRule{}
	for _, v := range manifest.Events {
		p.Events[v.Key] = ingestion.EventRule{Key: v.Key, Version: v.Version, AllowedSourceKinds: v.Kinds, Schema: v.Schema}
	}
	return p, nil
}
