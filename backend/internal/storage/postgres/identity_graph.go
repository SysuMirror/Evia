package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/identity"
	"github.com/howtomakeaname/Evia/backend/internal/platform/env"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

var _ identity.Store = (*Store)(nil)

const identityLeaseLost = "IDENTITY_LEASE_LOST"

func identityInvalid(message string) error {
	return fault.New(fault.Invalid, "INVALID_IDENTITY_INPUT", message)
}

func identityNotFound(message string) error {
	return fault.New(fault.NotFound, "IDENTITY_REVISION_NOT_FOUND", message)
}

// Erased serves the fold barrier from the same tombstone predicate as the
// write-side ErasedSubjects; fold callers treat errors as fail-closed.
func (s *Store) Erased(ctx context.Context, scope identity.Scope, uids, anonymousIDs []string) (identity.ErasedSet, error) {
	set := identity.ErasedSet{UIDs: map[string]struct{}{}, AnonymousIDs: map[string]struct{}{}}
	kinds, values, e := erasedSubjectRows(ctx, s.Pool.Query, scope.ProjectID, scope.Environment, uids, anonymousIDs)
	if e != nil {
		return set, e
	}
	for i := range kinds {
		if kinds[i] == identity.MemberUID {
			set.UIDs[values[i]] = struct{}{}
		} else {
			set.AnonymousIDs[values[i]] = struct{}{}
		}
	}
	return set, nil
}

func (s *Store) FoldScopes(ctx context.Context) ([]identity.Scope, error) {
	rows, e := s.Pool.Query(ctx, `SELECT DISTINCT project_id::text,environment FROM catalog_release_activations`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var scopes []identity.Scope
	for rows.Next() {
		var scope identity.Scope
		if e = rows.Scan(&scope.ProjectID, &scope.Environment); e != nil {
			return nil, e
		}
		scopes = append(scopes, scope)
	}
	return scopes, rows.Err()
}

// identityLeaseGuard re-validates the fold lease inside a mutation
// transaction; losing it aborts the fold without partial writes.
func identityLeaseGuard(ctx context.Context, tx pgx.Tx, claim *identity.FoldClaim) error {
	tag, e := tx.Exec(ctx, `UPDATE identity_fold_state SET updated_at=now()
	 WHERE project_id=$1 AND environment=$2 AND lease_owner=$3 AND lease_generation=$4
	 AND lease_until>clock_timestamp()`,
		claim.ProjectID, claim.Environment, claim.Worker, claim.Generation)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return fault.New(fault.Conflict, identityLeaseLost, "身份折叠租约已过期或被其他实例持有")
	}
	return nil
}

func (s *Store) ClaimScope(ctx context.Context, scope identity.Scope, worker string, lease time.Duration) (*identity.FoldClaim, bool, error) {
	if worker == "" {
		return nil, false, identityInvalid("折叠实例标识不能为空")
	}
	if lease <= 0 || lease > time.Hour {
		return nil, false, identityInvalid("折叠租约时长无效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, false, e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `INSERT INTO identity_fold_state(project_id,environment) VALUES($1,$2)
		 ON CONFLICT DO NOTHING`, scope.ProjectID, scope.Environment); e != nil {
		return nil, false, e
	}
	var (
		generation    int64
		watermark     *time.Time
		lastCompleted int64
	)
	e = tx.QueryRow(ctx, `UPDATE identity_fold_state
		 SET lease_owner=$3, lease_until=clock_timestamp()+$4*interval '1 second',
		     lease_generation=lease_generation+1, updated_at=now()
		 WHERE project_id=$1 AND environment=$2
		 AND (lease_until IS NULL OR lease_until<clock_timestamp())
		 RETURNING lease_generation, scan_watermark, last_completed_revision`,
		scope.ProjectID, scope.Environment, worker, int(lease.Seconds())).Scan(&generation, &watermark, &lastCompleted)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	var nextRevision int64
	if e = tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM identity_revisions
		 WHERE project_id=$1 AND environment=$2`, scope.ProjectID, scope.Environment).Scan(&nextRevision); e != nil {
		return nil, false, e
	}
	graph, e := s.loadIdentityGraph(ctx, tx, scope)
	if e != nil {
		return nil, false, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, false, e
	}
	claim := &identity.FoldClaim{
		Scope:                 scope,
		Worker:                worker,
		Generation:            generation,
		Watermark:             derefTime(watermark),
		LastCompletedRevision: lastCompleted,
		NextRevision:          nextRevision,
		Graph:                 graph,
	}
	return claim, true, nil
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func (s *Store) loadIdentityGraph(ctx context.Context, tx pgx.Tx, scope identity.Scope) (*identity.Graph, error) {
	graph := &identity.Graph{}
	edges, e := tx.Query(ctx, `SELECT uid,anonymous_id,first_seen_at,last_seen_at,first_revision,last_revision
		 FROM identity_edges WHERE project_id=$1 AND environment=$2`, scope.ProjectID, scope.Environment)
	if e != nil {
		return nil, e
	}
	defer edges.Close()
	for edges.Next() {
		var edge identity.Edge
		if e = edges.Scan(&edge.UID, &edge.AnonymousID, &edge.FirstSeenAt, &edge.LastSeenAt, &edge.FirstRevision, &edge.LastRevision); e != nil {
			return nil, e
		}
		graph.Edges = append(graph.Edges, edge)
	}
	if e = edges.Err(); e != nil {
		return nil, e
	}
	persons, e := tx.Query(ctx, `SELECT resolved_person_id::text,state,superseded_by::text,created_at,create_revision
		 FROM identity_persons WHERE project_id=$1 AND environment=$2`, scope.ProjectID, scope.Environment)
	if e != nil {
		return nil, e
	}
	defer persons.Close()
	for persons.Next() {
		var person identity.Person
		if e = persons.Scan(&person.ResolvedPersonID, &person.State, &person.SupersededBy, &person.CreatedAt, &person.CreateRevision); e != nil {
			return nil, e
		}
		graph.Persons = append(graph.Persons, person)
	}
	if e = persons.Err(); e != nil {
		return nil, e
	}
	members, e := tx.Query(ctx, `SELECT resolved_person_id::text,member_kind,member_value,first_seen_at,joined_revision
		 FROM identity_person_members WHERE project_id=$1 AND environment=$2`, scope.ProjectID, scope.Environment)
	if e != nil {
		return nil, e
	}
	defer members.Close()
	for members.Next() {
		var member identity.Member
		if e = members.Scan(&member.ResolvedPersonID, &member.Kind, &member.Value, &member.FirstSeenAt, &member.JoinedRevision); e != nil {
			return nil, e
		}
		graph.Members = append(graph.Members, member)
	}
	return graph, members.Err()
}

// identityRelease drops the lease without writing a revision.
func (s *Store) identityRelease(ctx context.Context, claim *identity.FoldClaim) error {
	tag, e := s.Pool.Exec(ctx, `UPDATE identity_fold_state
	 SET lease_owner=NULL, lease_until=NULL, updated_at=now()
	 WHERE project_id=$1 AND environment=$2 AND lease_owner=$3 AND lease_generation=$4`,
		claim.ProjectID, claim.Environment, claim.Worker, claim.Generation)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return fault.New(fault.Conflict, identityLeaseLost, "身份折叠租约已过期或被其他实例持有")
	}
	return nil
}

func (s *Store) ReleaseFold(ctx context.Context, claim *identity.FoldClaim) error {
	return s.identityRelease(ctx, claim)
}

func (s *Store) RecordFoldFailure(ctx context.Context, claim *identity.FoldClaim, code string) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if e = identityLeaseGuard(ctx, tx, claim); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO identity_revisions
		 (project_id,environment,revision,edge_count,person_count,member_count,state,built_at,error_code)
		 VALUES($1,$2,$3,0,0,0,'failed',now(),$4)`,
		claim.ProjectID, claim.Environment, claim.NextRevision, code); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE identity_fold_state
		 SET lease_owner=NULL, lease_until=NULL, updated_at=now()
		 WHERE project_id=$1 AND environment=$2 AND lease_owner=$3 AND lease_generation=$4`,
		claim.ProjectID, claim.Environment, claim.Worker, claim.Generation); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Store) CommitFold(ctx context.Context, claim *identity.FoldClaim, outcome identity.FoldOutcome) error {
	if !outcome.Changed {
		return identityInvalid("未变更的折叠结果不能提交修订")
	}
	if outcome.NextRevision != claim.NextRevision {
		return fault.New(fault.Conflict, identityLeaseLost, "折叠修订编号与租约不一致")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if e = identityLeaseGuard(ctx, tx, claim); e != nil {
		return e
	}
	for _, edge := range outcome.EdgeUpserts {
		if _, e = tx.Exec(ctx, `INSERT INTO identity_edges
			 (project_id,environment,uid,anonymous_id,first_seen_at,last_seen_at,first_revision,last_revision)
			 VALUES($1,$2,$3,$4,$5,$6,$7,$8)
			 ON CONFLICT (project_id,environment,uid,anonymous_id) DO UPDATE
			 SET last_seen_at=EXCLUDED.last_seen_at, last_revision=EXCLUDED.last_revision`,
			claim.ProjectID, claim.Environment, edge.UID, edge.AnonymousID,
			edge.FirstSeenAt, edge.LastSeenAt, edge.FirstRevision, edge.LastRevision); e != nil {
			return e
		}
	}
	for _, person := range outcome.NewPersons {
		if _, e = tx.Exec(ctx, `INSERT INTO identity_persons
			 (project_id,environment,resolved_person_id,state,created_at,create_revision)
			 VALUES($1,$2,$3,'active',$4,$5)`,
			claim.ProjectID, claim.Environment, person.ResolvedPersonID, person.CreatedAt, person.CreateRevision); e != nil {
			return e
		}
	}
	for _, retired := range outcome.Retire {
		tag, e := tx.Exec(ctx, `UPDATE identity_persons
			 SET state='retired', superseded_by=$3
			 WHERE project_id=$1 AND environment=$2 AND resolved_person_id=$4 AND state='active'`,
			claim.ProjectID, claim.Environment, *retired.SupersededBy, retired.ResolvedPersonID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return fault.New(fault.Conflict, identityLeaseLost, "被合并身份已不在预期状态")
		}
		// Membership moves wholesale with original provenance; components are
		// disjoint so the per-scope member uniqueness cannot collide.
		if _, e = tx.Exec(ctx, `UPDATE identity_person_members
			 SET resolved_person_id=$3
			 WHERE project_id=$1 AND environment=$2 AND resolved_person_id=$4`,
			claim.ProjectID, claim.Environment, *retired.SupersededBy, retired.ResolvedPersonID); e != nil {
			return e
		}
	}
	for _, member := range outcome.NewMembers {
		if _, e = tx.Exec(ctx, `INSERT INTO identity_person_members
			 (project_id,environment,resolved_person_id,member_kind,member_value,first_seen_at,joined_revision)
			 VALUES($1,$2,$3,$4,$5,$6,$7)`,
			claim.ProjectID, claim.Environment, member.ResolvedPersonID, member.Kind, member.Value,
			member.FirstSeenAt, member.JoinedRevision); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO identity_revisions
		 (project_id,environment,revision,watermark_received_at,edge_count,person_count,member_count,state,built_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,'completed',now())`,
		claim.ProjectID, claim.Environment, outcome.NextRevision, outcome.Watermark,
		outcome.EdgeCount, outcome.ActivePersonCount, outcome.MemberCount); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE identity_fold_state
		 SET last_completed_revision=$3, scan_watermark=$4, lease_owner=NULL, lease_until=NULL, updated_at=now()
		 WHERE project_id=$1 AND environment=$2 AND lease_owner=$5 AND lease_generation=$6`,
		claim.ProjectID, claim.Environment, outcome.NextRevision, outcome.Watermark,
		claim.Worker, claim.Generation); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

const identityRevisionColumns = `revision,watermark_received_at,edge_count,person_count,member_count,state,built_at,error_code`

func scanIdentityRevision(row pgx.Row) (identity.Revision, error) {
	var r identity.Revision
	e := row.Scan(&r.Revision, &r.Watermark, &r.EdgeCount, &r.ActivePersonCount, &r.MemberCount,
		&r.State, &r.BuiltAt, &r.ErrorCode)
	return r, e
}

func scanIdentityRevisionRows(rows pgx.Rows) ([]identity.Revision, error) {
	defer rows.Close()
	var items []identity.Revision
	for rows.Next() {
		var r identity.Revision
		if e := rows.Scan(&r.Revision, &r.Watermark, &r.EdgeCount, &r.ActivePersonCount, &r.MemberCount,
			&r.State, &r.BuiltAt, &r.ErrorCode); e != nil {
			return nil, e
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// identityReadAuthority enforces queries.read for humans (any active member)
// and machines (explicit grant reloaded inside the transaction).
func (s *Store) identityReadAuthority(ctx context.Context, tx pgx.Tx, actor, project, environment string, raw bool) error {
	permission := access.PermissionQueriesRead
	if raw {
		permission = access.PermissionQueriesRawRead
	}
	return s.authorizeOperation(ctx, tx, actor, project, environment, permission)
}

func validateIdentityRead(actor, project, environment string) error {
	if len(actor) == 0 || len(project) == 0 {
		return identityInvalid("操作主体或项目标识缺失")
	}
	switch environment {
	case env.Development, env.Staging, env.Production:
	default:
		return identityInvalid("环境标识无效")
	}
	return nil
}

func (s *Store) ListRevisions(ctx context.Context, actor, project, environment string, filter identity.ReadFilter) (identity.Page, error) {
	if e := validateIdentityRead(actor, project, environment); e != nil {
		return identity.Page{}, e
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if filter.AfterRevision < 0 {
		return identity.Page{}, identityInvalid("分页游标无效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return identity.Page{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.identityReadAuthority(ctx, tx, actor, project, environment, false); e != nil {
		return identity.Page{}, e
	}
	after := filter.AfterRevision
	if after <= 0 {
		after = 1 << 62
	}
	rows, e := tx.Query(ctx, `SELECT `+identityRevisionColumns+` FROM identity_revisions
		 WHERE project_id=$1 AND environment=$2 AND revision<$3
		 ORDER BY revision DESC LIMIT $4`,
		project, environment, after, limit+1)
	if e != nil {
		return identity.Page{}, e
	}
	items, e := scanIdentityRevisionRows(rows)
	if e != nil {
		return identity.Page{}, e
	}
	page := identity.Page{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextRevision = items[limit-1].Revision
	}
	return page, tx.Commit(ctx)
}

func (s *Store) GetRevision(ctx context.Context, actor, project, environment string, revision int64) (identity.Revision, error) {
	if e := validateIdentityRead(actor, project, environment); e != nil {
		return identity.Revision{}, e
	}
	if revision <= 0 {
		return identity.Revision{}, identityInvalid("修订编号无效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return identity.Revision{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.identityReadAuthority(ctx, tx, actor, project, environment, false); e != nil {
		return identity.Revision{}, e
	}
	item, e := scanIdentityRevision(tx.QueryRow(ctx, `SELECT `+identityRevisionColumns+` FROM identity_revisions
		 WHERE project_id=$1 AND environment=$2 AND revision=$3`, project, environment, revision))
	if errors.Is(e, pgx.ErrNoRows) {
		return identity.Revision{}, identityNotFound("身份修订不存在")
	}
	if e != nil {
		return identity.Revision{}, e
	}
	return item, tx.Commit(ctx)
}

// ResolveMember answers the forward lookup for one raw identity. v1 resolves
// against the current registry, so the requested revision must be the scope's
// latest completed snapshot; historical member snapshots are a documented
// follow-up and older pins fail closed instead of answering from a state the
// revision never captured.
func (s *Store) ResolveMember(ctx context.Context, actor, project, environment string, revision int64, kind, value string) (identity.Resolution, error) {
	if e := validateIdentityRead(actor, project, environment); e != nil {
		return identity.Resolution{}, e
	}
	if revision <= 0 {
		return identity.Resolution{}, identityInvalid("修订编号无效")
	}
	if kind != identity.MemberUID && kind != identity.MemberAnonymousID {
		return identity.Resolution{}, identityInvalid("成员类型无效")
	}
	if len(value) < 1 || len(value) > 128 {
		return identity.Resolution{}, identityInvalid("成员标识长度无效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return identity.Resolution{}, e
	}
	defer tx.Rollback(context.Background())
	if e = s.identityReadAuthority(ctx, tx, actor, project, environment, true); e != nil {
		return identity.Resolution{}, e
	}
	var exists int
	if e = tx.QueryRow(ctx, `SELECT 1 FROM identity_revisions
		 WHERE project_id=$1 AND environment=$2 AND revision=$3`, project, environment, revision).Scan(&exists); errors.Is(e, pgx.ErrNoRows) {
		return identity.Resolution{}, identityNotFound("身份修订不存在")
	} else if e != nil {
		return identity.Resolution{}, e
	}
	var latest int64
	if e = tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0) FROM identity_revisions
		 WHERE project_id=$1 AND environment=$2 AND state='completed'`, project, environment).Scan(&latest); e != nil {
		return identity.Resolution{}, e
	}
	if revision != latest {
		return identity.Resolution{}, fault.New(fault.Conflict, "IDENTITY_REVISION_NOT_RESOLVABLE",
			"当前仅支持解析最新完成修订；历史成员快照尚未提供")
	}
	var personID string
	if e = tx.QueryRow(ctx, `SELECT resolved_person_id::text FROM identity_person_members
		 WHERE project_id=$1 AND environment=$2 AND member_kind=$3 AND member_value=$4`,
		project, environment, kind, value).Scan(&personID); errors.Is(e, pgx.ErrNoRows) {
		return identity.Resolution{}, identityNotFound("该身份尚未进入身份图")
	} else if e != nil {
		return identity.Resolution{}, e
	}
	resolved := personID
	for depth := 0; depth < identity.MaxRedirectDepth; depth++ {
		var superseded *string
		if e = tx.QueryRow(ctx, `SELECT superseded_by::text FROM identity_persons
			 WHERE project_id=$1 AND environment=$2 AND resolved_person_id=$3`,
			project, environment, resolved).Scan(&superseded); e != nil {
			return identity.Resolution{}, e
		}
		if superseded == nil {
			break
		}
		resolved = *superseded
		if depth == identity.MaxRedirectDepth-1 {
			return identity.Resolution{}, fault.New(fault.Conflict, "IDENTITY_REDIRECT_TOO_DEEP", "身份重定向链超出可解析深度")
		}
	}
	members, e := tx.Query(ctx, `SELECT resolved_person_id::text,member_kind,member_value,first_seen_at,joined_revision
		 FROM identity_person_members WHERE project_id=$1 AND environment=$2 AND resolved_person_id=$3`,
		project, environment, resolved)
	if e != nil {
		return identity.Resolution{}, e
	}
	defer members.Close()
	resolution := identity.Resolution{Revision: revision, ResolvedPersonID: resolved}
	for members.Next() {
		var member identity.Member
		if e = members.Scan(&member.ResolvedPersonID, &member.Kind, &member.Value, &member.FirstSeenAt, &member.JoinedRevision); e != nil {
			return identity.Resolution{}, e
		}
		resolution.Members = append(resolution.Members, member)
	}
	if e = members.Err(); e != nil {
		return identity.Resolution{}, e
	}
	return resolution, tx.Commit(ctx)
}
