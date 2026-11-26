package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/experiments"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ experiments.Store = (*Store)(nil)

const experimentColumns = `id::text,project_id::text,environment,experiment_key,name,description,status,salt,control_variant,exposure_event_key,variants_json,overrides_json,version,started_at,ended_at,created_by::text,created_at,updated_at`

func scanExperiment(row rowScanner) (experiments.Experiment, error) {
	var out experiments.Experiment
	var variantsRaw, overridesRaw []byte
	err := row.Scan(
		&out.ID,
		&out.ProjectID,
		&out.Environment,
		&out.ExperimentKey,
		&out.Name,
		&out.Description,
		&out.Status,
		&out.Salt,
		&out.ControlVariant,
		&out.ExposureEventKey,
		&variantsRaw,
		&overridesRaw,
		&out.Version,
		&out.StartedAt,
		&out.EndedAt,
		&out.CreatedBy,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return experiments.Experiment{}, fault.New(fault.NotFound, "EXPERIMENT_NOT_FOUND", "实验不存在")
	}
	if err != nil {
		return experiments.Experiment{}, err
	}
	out.Variants = []experiments.Variant{}
	if len(variantsRaw) > 0 {
		if err = json.Unmarshal(variantsRaw, &out.Variants); err != nil {
			return experiments.Experiment{}, err
		}
	}
	out.Overrides = map[string]string{}
	if len(overridesRaw) > 0 {
		if err = json.Unmarshal(overridesRaw, &out.Overrides); err != nil {
			return experiments.Experiment{}, err
		}
	}
	out.CreatedAt = out.CreatedAt.UTC()
	out.UpdatedAt = out.UpdatedAt.UTC()
	if out.StartedAt != nil {
		t := out.StartedAt.UTC()
		out.StartedAt = &t
	}
	if out.EndedAt != nil {
		t := out.EndedAt.UTC()
		out.EndedAt = &t
	}
	return out, nil
}

func (s *Store) ListExperiments(ctx context.Context, actor, project, environment string) ([]experiments.Experiment, error) {
	tx, err := s.catalogTx(ctx, actor, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())

	var rows pgx.Rows
	if environment != "" {
		rows, err = tx.Query(ctx, `SELECT `+experimentColumns+` FROM experiments WHERE project_id=$1 AND environment=$2 ORDER BY updated_at DESC, experiment_key ASC`, project, environment)
	} else {
		rows, err = tx.Query(ctx, `SELECT `+experimentColumns+` FROM experiments WHERE project_id=$1 ORDER BY updated_at DESC, experiment_key ASC`, project)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []experiments.Experiment{}
	for rows.Next() {
		item, scanErr := scanExperiment(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) GetExperiment(ctx context.Context, actor, project, id string) (experiments.Experiment, error) {
	tx, err := s.catalogTx(ctx, actor, project, false)
	if err != nil {
		return experiments.Experiment{}, err
	}
	defer tx.Rollback(context.Background())

	out, err := scanExperiment(tx.QueryRow(ctx, `SELECT `+experimentColumns+` FROM experiments WHERE project_id=$1 AND id=$2`, project, id))
	if err != nil {
		return experiments.Experiment{}, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) CreateExperiment(ctx context.Context, actor, project string, item experiments.Experiment) (experiments.Experiment, error) {
	tx, err := s.catalogTx(ctx, actor, project, true)
	if err != nil {
		return experiments.Experiment{}, err
	}
	defer tx.Rollback(context.Background())

	variantsRaw, err := json.Marshal(item.Variants)
	if err != nil {
		return experiments.Experiment{}, err
	}
	overrides := item.Overrides
	if overrides == nil {
		overrides = map[string]string{}
	}
	overridesRaw, err := json.Marshal(overrides)
	if err != nil {
		return experiments.Experiment{}, err
	}
	out, err := scanExperiment(tx.QueryRow(
		ctx,
		`INSERT INTO experiments(id,project_id,environment,experiment_key,name,description,status,salt,control_variant,exposure_event_key,variants_json,overrides_json,version,created_by)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12::jsonb,1,$13)
		 RETURNING `+experimentColumns,
		item.ID, project, item.Environment, item.ExperimentKey, item.Name, item.Description, item.Status, item.Salt, item.ControlVariant, item.ExposureEventKey, string(variantsRaw), string(overridesRaw), actor,
	))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return experiments.Experiment{}, fault.New(fault.Conflict, "EXPERIMENT_KEY_EXISTS", "该环境下已存在相同的实验键")
		}
		return experiments.Experiment{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'experiment.created',$3)`, project, actor, out.ID); err != nil {
		return experiments.Experiment{}, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) UpdateExperiment(ctx context.Context, actor, project, id string, in experiments.UpdateInput) (experiments.Experiment, error) {
	tx, err := s.catalogTx(ctx, actor, project, true)
	if err != nil {
		return experiments.Experiment{}, err
	}
	defer tx.Rollback(context.Background())

	current, err := scanExperiment(tx.QueryRow(ctx, `SELECT `+experimentColumns+` FROM experiments WHERE project_id=$1 AND id=$2 FOR UPDATE`, project, id))
	if err != nil {
		return experiments.Experiment{}, err
	}
	if current.Status == experiments.StatusCompleted {
		return experiments.Experiment{}, fault.New(fault.Conflict, "EXPERIMENT_COMPLETED", "已结束的实验不可修改")
	}
	salt := in.Salt
	if salt == "" {
		salt = current.Salt
	}
	variantsRaw, err := json.Marshal(in.Variants)
	if err != nil {
		return experiments.Experiment{}, err
	}
	overrides := in.Overrides
	if overrides == nil {
		overrides = map[string]string{}
	}
	overridesRaw, err := json.Marshal(overrides)
	if err != nil {
		return experiments.Experiment{}, err
	}
	out, err := scanExperiment(tx.QueryRow(
		ctx,
		`UPDATE experiments
		 SET name=$3, description=$4, salt=$5, control_variant=$6, exposure_event_key=$7,
		     variants_json=$8::jsonb, overrides_json=$9::jsonb, version=version+1, updated_at=now()
		 WHERE project_id=$1 AND id=$2
		 RETURNING `+experimentColumns,
		project, id, in.Name, in.Description, salt, in.ControlVariant, in.ExposureEventKey, string(variantsRaw), string(overridesRaw),
	))
	if err != nil {
		return experiments.Experiment{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'experiment.updated',$3)`, project, actor, out.ID); err != nil {
		return experiments.Experiment{}, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ChangeExperimentStatus(ctx context.Context, actor, project, id, status string) (experiments.Experiment, error) {
	tx, err := s.catalogTx(ctx, actor, project, true)
	if err != nil {
		return experiments.Experiment{}, err
	}
	defer tx.Rollback(context.Background())

	current, err := scanExperiment(tx.QueryRow(ctx, `SELECT `+experimentColumns+` FROM experiments WHERE project_id=$1 AND id=$2 FOR UPDATE`, project, id))
	if err != nil {
		return experiments.Experiment{}, err
	}
	if !experiments.CanTransition(current.Status, status) {
		return experiments.Experiment{}, fault.New(fault.Conflict, "INVALID_EXPERIMENT_TRANSITION", "当前实验状态不允许切换到目标状态")
	}
	now := time.Now().UTC()
	startedAt := current.StartedAt
	endedAt := current.EndedAt
	if status == experiments.StatusRunning && startedAt == nil {
		startedAt = &now
	}
	if status == experiments.StatusCompleted {
		endedAt = &now
	}
	out, err := scanExperiment(tx.QueryRow(
		ctx,
		`UPDATE experiments
		 SET status=$3, started_at=$4, ended_at=$5, version=version+1, updated_at=now()
		 WHERE project_id=$1 AND id=$2
		 RETURNING `+experimentColumns,
		project, id, status, startedAt, endedAt,
	))
	if err != nil {
		return experiments.Experiment{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'experiment.status_changed',$3)`, project, actor, out.ID); err != nil {
		return experiments.Experiment{}, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) DeleteExperiment(ctx context.Context, actor, project, id string) error {
	tx, err := s.catalogTx(ctx, actor, project, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())

	current, err := scanExperiment(tx.QueryRow(ctx, `SELECT `+experimentColumns+` FROM experiments WHERE project_id=$1 AND id=$2 FOR UPDATE`, project, id))
	if err != nil {
		return err
	}
	if current.Status == experiments.StatusRunning {
		return fault.New(fault.Conflict, "EXPERIMENT_RUNNING", "运行中的实验请先暂停或结束后再删除")
	}
	if _, err = tx.Exec(ctx, `DELETE FROM experiments WHERE project_id=$1 AND id=$2`, project, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key) VALUES($1,$2,'experiment.deleted',$3)`, project, actor, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RunningExperiments loads active experiments for /v1/config delivery in a
// deterministic order so ETag and Ed25519 signatures remain stable.
func (s *Store) RunningExperiments(ctx context.Context, project, environment string) ([]delivery.ConfigExperiment, error) {
	rows, err := s.Pool.Query(
		ctx,
		`SELECT experiment_key, version, salt, control_variant, exposure_event_key, variants_json, overrides_json
		 FROM experiments
		 WHERE project_id=$1 AND environment=$2 AND status='running'
		 ORDER BY experiment_key ASC
		 LIMIT 100`,
		project, environment,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []delivery.ConfigExperiment{}
	for rows.Next() {
		var item delivery.ConfigExperiment
		var variantsRaw, overridesRaw []byte
		if scanErr := rows.Scan(&item.Key, &item.Version, &item.Salt, &item.ControlVariant, &item.ExposureEventKey, &variantsRaw, &overridesRaw); scanErr != nil {
			return nil, scanErr
		}
		var variants []experiments.Variant
		if len(variantsRaw) > 0 {
			if err = json.Unmarshal(variantsRaw, &variants); err != nil {
				return nil, err
			}
		}
		item.Variants = make([]delivery.ConfigExperimentVariant, 0, len(variants))
		for _, v := range variants {
			item.Variants = append(item.Variants, delivery.ConfigExperimentVariant{Key: v.Key, WeightBps: v.WeightBps})
		}
		var overrides map[string]string
		if len(overridesRaw) > 0 {
			if err = json.Unmarshal(overridesRaw, &overrides); err != nil {
				return nil, err
			}
		}
		if len(overrides) > 0 {
			item.Overrides = overrides
		}
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
