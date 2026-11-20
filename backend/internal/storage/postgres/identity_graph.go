package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/analysis"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/erasure"
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

var _ identity.MemberCatalog = (*Store)(nil)

// LatestCompletedRevision reports the scope's newest completed fold.
func (s *Store) LatestCompletedRevision(ctx context.Context, scope identity.Scope) (int64, error) {
	var latest int64
	e := s.Pool.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0) FROM identity_revisions
	 WHERE project_id=$1 AND environment=$2 AND state='completed'`,
		scope.ProjectID, scope.Environment).Scan(&latest)
	return latest, e
}

// ExpansionMembers resolves one person pin to its member set for a query
// plan. Only the latest completed revision is expandable in v1; older pins
// fail closed rather than expanding from state the revision never captured.
func (s *Store) ExpansionMembers(ctx context.Context, scope identity.Scope, revision int64, resolvedPersonID string) ([]identity.Member, error) {
	if revision <= 0 {
		return nil, fault.New(fault.Invalid, "IDENTITY_REVISION_NOT_FOUND", "身份修订编号无效")
	}
	if len(resolvedPersonID) == 0 || len(resolvedPersonID) > 64 {
		return nil, fault.New(fault.Invalid, "IDENTITY_REVISION_NOT_FOUND", "resolved person 标识无效")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.Background())
	var exists int
	if e = tx.QueryRow(ctx, `SELECT 1 FROM identity_revisions
	 WHERE project_id=$1 AND environment=$2 AND revision=$3`,
		scope.ProjectID, scope.Environment, revision).Scan(&exists); errors.Is(e, pgx.ErrNoRows) {
		return nil, fault.New(fault.NotFound, "IDENTITY_REVISION_NOT_FOUND", "身份修订不存在")
	} else if e != nil {
		return nil, e
	}
	var latest int64
	if e = tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0) FROM identity_revisions
	 WHERE project_id=$1 AND environment=$2 AND state='completed'`,
		scope.ProjectID, scope.Environment).Scan(&latest); e != nil {
		return nil, e
	}
	if revision != latest {
		return nil, fault.New(fault.Conflict, "IDENTITY_REVISION_NOT_RESOLVABLE",
			"当前仅支持最新完成修订；历史成员快照尚未提供")
	}
	resolved := resolvedPersonID
	for depth := 0; depth < identity.MaxRedirectDepth; depth++ {
		var superseded *string
		if e = tx.QueryRow(ctx, `SELECT superseded_by::text FROM identity_persons
		 WHERE project_id=$1 AND environment=$2 AND resolved_person_id=$3`,
			scope.ProjectID, scope.Environment, resolved).Scan(&superseded); errors.Is(e, pgx.ErrNoRows) {
			return nil, fault.New(fault.NotFound, "IDENTITY_REVISION_NOT_FOUND", "resolved person 不存在")
		} else if e != nil {
			return nil, e
		}
		if superseded == nil {
			break
		}
		resolved = *superseded
		if depth == identity.MaxRedirectDepth-1 {
			return nil, fault.New(fault.Conflict, "IDENTITY_REDIRECT_TOO_DEEP", "身份重定向链超出可解析深度")
		}
	}
	rows, e := tx.Query(ctx, `SELECT resolved_person_id::text,member_kind,member_value,first_seen_at,joined_revision
	 FROM identity_person_members WHERE project_id=$1 AND environment=$2 AND resolved_person_id=$3`,
		scope.ProjectID, scope.Environment, resolved)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var members []identity.Member
	for rows.Next() {
		var member identity.Member
		if e = rows.Scan(&member.ResolvedPersonID, &member.Kind, &member.Value, &member.FirstSeenAt, &member.JoinedRevision); e != nil {
			return nil, e
		}
		members = append(members, member)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return members, tx.Commit(ctx)
}

// RetireIdentitySubject erases one identity's derived graph state in the
// erasure rebuilding phase. Edges touching the subject and its membership row
// are deleted; a person that only ever held the subject disappears, otherwise
// the whole person retires without a redirect and its surviving member rows
// are removed so the next fold re-groups them under fresh person ids.
func (s *Store) RetireIdentitySubject(ctx context.Context, claim erasure.Claim) error {
	switch claim.SubjectKind {
	case identity.MemberUID, identity.MemberAnonymousID:
	default:
		return fault.New(fault.Invalid, "INVALID_ERASURE_INPUT", "删除对象类型无效")
	}
	column := "uid"
	if claim.SubjectKind == identity.MemberAnonymousID {
		column = "anonymous_id"
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	var owners []string
	rows, e := tx.Query(ctx, `SELECT resolved_person_id::text FROM identity_person_members
	 WHERE project_id=$1 AND environment=$2 AND member_kind=$3 AND member_value=$4`,
		claim.ProjectID, claim.Environment, claim.SubjectKind, claim.SubjectValue)
	if e != nil {
		return e
	}
	for rows.Next() {
		var owner string
		if e = rows.Scan(&owner); e != nil {
			rows.Close()
			return e
		}
		owners = append(owners, owner)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return e
	}
	rows.Close()
	for _, owner := range owners {
		var total int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM identity_person_members
		 WHERE project_id=$1 AND environment=$2 AND resolved_person_id=$3`,
			claim.ProjectID, claim.Environment, owner).Scan(&total); e != nil {
			return e
		}
		if total <= 1 {
			// The subject was this person's only member: remove the registry
			// row entirely so the id cannot be resolved later.
			if _, e = tx.Exec(ctx, `DELETE FROM identity_persons
			 WHERE project_id=$1 AND environment=$2 AND resolved_person_id=$3`,
				claim.ProjectID, claim.Environment, owner); e != nil {
				return e
			}
			continue
		}
		// The component shared the erased identity: retire the person and drop
		// every remaining member row; the fold rebuilds survivors under new
		// ids because it never treats retired persons as owners.
		if _, e = tx.Exec(ctx, `DELETE FROM identity_person_members
		 WHERE project_id=$1 AND environment=$2 AND resolved_person_id=$3`,
			claim.ProjectID, claim.Environment, owner); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE identity_persons
			 SET state='retired'
			 WHERE project_id=$1 AND environment=$2 AND resolved_person_id=$3`,
			claim.ProjectID, claim.Environment, owner); e != nil {
			return e
		}
	}
	// Edges that touched the erased identity cannot survive it.
	if _, e = tx.Exec(ctx, `DELETE FROM identity_edges
	 WHERE project_id=$1 AND environment=$2 AND `+column+`=$3`,
		claim.ProjectID, claim.Environment, claim.SubjectValue); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_entries(project_id,actor_id,action,resource_key)
	 VALUES($1,$2,'identity.graph_retired',$3)`,
		claim.ProjectID, claim.ActorID, claim.JobID); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// materializeIdentityExpansion resolves a resolved_person query against the
// identity graph inside the enqueue transaction: the revision is pinned (or
// defaulted to the latest completed one), the member mapping is loaded under
// the pin, and the durable request carries it so workers and result metadata
// never re-read the graph. Budget violations fail closed.
func (s *Store) materializeIdentityExpansion(ctx context.Context, tx pgx.Tx, project string, request *analysis.Request) error {
	if request.Subject != analysis.SubjectResolvedPerson && request.IdentityRevision == nil {
		return nil
	}
	scope := identity.Scope{ProjectID: project, Environment: request.Environment}
	latest, e := latestCompletedRevisionTx(ctx, tx, scope)
	if e != nil {
		return e
	}
	pinned := request.IdentityRevision
	if pinned == nil {
		if latest == 0 {
			return fault.New(fault.NotFound, "IDENTITY_REVISION_NOT_FOUND", "该作用域尚无已完成的身份修订")
		}
		revision := latest
		pinned = &revision
	} else {
		var exists int
		if e = tx.QueryRow(ctx, `SELECT 1 FROM identity_revisions
		 WHERE project_id=$1 AND environment=$2 AND revision=$3`,
			project, request.Environment, *pinned).Scan(&exists); errors.Is(e, pgx.ErrNoRows) {
			return fault.New(fault.NotFound, "IDENTITY_REVISION_NOT_FOUND", "身份修订不存在")
		} else if e != nil {
			return e
		}
		if *pinned != latest {
			return fault.New(fault.Conflict, "IDENTITY_REVISION_NOT_RESOLVABLE",
				"当前仅支持最新完成修订；历史成员快照尚未提供")
		}
	}
	expansion := &analysis.IdentityExpansion{Revision: *pinned}
	if request.IdentityFilter != nil && request.IdentityFilter.ResolvedPersonID != nil {
		expansion.PersonID = *request.IdentityFilter.ResolvedPersonID
		members, e := expansionMembersTx(ctx, tx, scope, *pinned, expansion.PersonID)
		if e != nil {
			return e
		}
		if len(members) == 0 {
			return fault.New(fault.NotFound, "IDENTITY_REVISION_NOT_FOUND", "resolved person 不在当前修订中")
		}
		if len(members) > identity.MaxExpansionMembers {
			return fault.New(fault.Invalid, "IDENTITY_EXPANSION_EXCEEDED",
				"resolved person 成员数超出展开预算")
		}
		for _, member := range members {
			if member.Kind == identity.MemberUID {
				expansion.PinnedUIDs = append(expansion.PinnedUIDs, member.Value)
			} else {
				expansion.PinnedAnonymousIDs = append(expansion.PinnedAnonymousIDs, member.Value)
			}
		}
		// A pinned query maps only its own person; other rows must not
		// group, so the mapping covers exactly the pinned member sets.
		expansion.Mapping = []analysis.PersonMapping{{
			PersonID:     expansion.PersonID,
			UIDs:         expansion.PinnedUIDs,
			AnonymousIDs: expansion.PinnedAnonymousIDs,
		}}
		request.IdentityExpansion = expansion
		return nil
	}
	persons, members, e := scopeMappingTx(ctx, tx, scope, *pinned)
	if e != nil {
		return e
	}
	if len(members) > identity.MaxScopeExpansionMembers {
		return fault.New(fault.Invalid, "IDENTITY_EXPANSION_EXCEEDED",
			"身份图成员总数超出查询展开预算")
	}
	byPerson := make(map[string]*analysis.PersonMapping, len(persons))
	order := make([]string, 0, len(persons))
	for _, person := range persons {
		byPerson[person] = &analysis.PersonMapping{PersonID: person}
		order = append(order, person)
	}
	for _, member := range members {
		mapping, ok := byPerson[member.resolved]
		if !ok {
			continue
		}
		if member.kind == identity.MemberUID {
			mapping.UIDs = append(mapping.UIDs, member.value)
		} else {
			mapping.AnonymousIDs = append(mapping.AnonymousIDs, member.value)
		}
	}
	for _, person := range order {
		expansion.Mapping = append(expansion.Mapping, *byPerson[person])
	}
	request.IdentityExpansion = expansion
	return nil
}

func latestCompletedRevisionTx(ctx context.Context, tx pgx.Tx, scope identity.Scope) (int64, error) {
	var latest int64
	e := tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0) FROM identity_revisions
	 WHERE project_id=$1 AND environment=$2 AND state='completed'`,
		scope.ProjectID, scope.Environment).Scan(&latest)
	return latest, e
}

// expansionMembersTx follows redirect chains for one person inside the
// enqueue transaction.
func expansionMembersTx(ctx context.Context, tx pgx.Tx, scope identity.Scope, revision int64, resolvedPersonID string) ([]identity.Member, error) {
	resolved := resolvedPersonID
	for depth := 0; depth < identity.MaxRedirectDepth; depth++ {
		var superseded *string
		if e := tx.QueryRow(ctx, `SELECT superseded_by::text FROM identity_persons
		 WHERE project_id=$1 AND environment=$2 AND resolved_person_id=$3`,
			scope.ProjectID, scope.Environment, resolved).Scan(&superseded); errors.Is(e, pgx.ErrNoRows) {
			return nil, fault.New(fault.NotFound, "IDENTITY_REVISION_NOT_FOUND", "resolved person 不存在")
		} else if e != nil {
			return nil, e
		}
		if superseded == nil {
			break
		}
		resolved = *superseded
		if depth == identity.MaxRedirectDepth-1 {
			return nil, fault.New(fault.Conflict, "IDENTITY_REDIRECT_TOO_DEEP", "身份重定向链超出可解析深度")
		}
	}
	rows, e := tx.Query(ctx, `SELECT resolved_person_id::text,member_kind,member_value
	 FROM identity_person_members WHERE project_id=$1 AND environment=$2 AND resolved_person_id=$3`,
		scope.ProjectID, scope.Environment, resolved)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var members []identity.Member
	for rows.Next() {
		var member identity.Member
		if e = rows.Scan(&member.ResolvedPersonID, &member.Kind, &member.Value); e != nil {
			return nil, e
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

type scopedMember struct {
	resolved, kind, value string
}

// scopeMappingTx loads every active person and member of the pinned revision
// for whole-graph subject mapping.
func scopeMappingTx(ctx context.Context, tx pgx.Tx, scope identity.Scope, revision int64) ([]string, []scopedMember, error) {
	persons, e := tx.Query(ctx, `SELECT resolved_person_id::text FROM identity_persons
	 WHERE project_id=$1 AND environment=$2 AND state='active'`,
		scope.ProjectID, scope.Environment)
	if e != nil {
		return nil, nil, e
	}
	var active []string
	for persons.Next() {
		var person string
		if e = persons.Scan(&person); e != nil {
			persons.Close()
			return nil, nil, e
		}
		active = append(active, person)
	}
	if e = persons.Err(); e != nil {
		persons.Close()
		return nil, nil, e
	}
	persons.Close()
	members, e := tx.Query(ctx, `SELECT resolved_person_id::text,member_kind,member_value
	 FROM identity_person_members WHERE project_id=$1 AND environment=$2`,
		scope.ProjectID, scope.Environment)
	if e != nil {
		return nil, nil, e
	}
	defer members.Close()
	var all []scopedMember
	for members.Next() {
		var member scopedMember
		if e = members.Scan(&member.resolved, &member.kind, &member.value); e != nil {
			return nil, nil, e
		}
		all = append(all, member)
	}
	return active, all, members.Err()
}
