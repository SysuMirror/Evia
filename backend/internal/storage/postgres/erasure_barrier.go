package postgres

import (
	"context"

	"github.com/howtomakeaname/Evia/backend/internal/erasure"
	"github.com/jackc/pgx/v5"
)

// ErasedSubjects resolves the durable tombstones for one write batch. The
// write worker treats any lookup error as fail-closed: an unknown barrier
// answer must never let an erased identity back into ClickHouse.
func (s *Store) ErasedSubjects(ctx context.Context, project, environment string, uids, anonymousIDs []string) (erasure.ErasedSet, error) {
	set := erasure.ErasedSet{UIDs: map[string]struct{}{}, AnonymousIDs: map[string]struct{}{}}
	if len(uids) == 0 && len(anonymousIDs) == 0 {
		return set, nil
	}
	kinds, values, e := erasedSubjectRows(ctx, s.Pool.Query, project, environment, uids, anonymousIDs)
	if e != nil {
		return set, e
	}
	for i := range kinds {
		if kinds[i] == erasure.SubjectUID {
			set.UIDs[values[i]] = struct{}{}
		} else {
			set.AnonymousIDs[values[i]] = struct{}{}
		}
	}
	return set, nil
}

// erasedSubjectRows is the single tombstone predicate shared by the write-side
// barrier and the identity fold; both must answer from the same rows or the
// barrier would admit identities the other half already blocked.
type queryFunc func(ctx context.Context, sql string, args ...any) (pgx.Rows, error)

func erasedSubjectRows(ctx context.Context, query queryFunc, project, environment string, uids, anonymousIDs []string) ([]string, []string, error) {
	rows, e := query(ctx, `SELECT subject_kind,subject_value FROM erasure_tombstones
 WHERE project_id=$1 AND environment=$2
 AND ((subject_kind='uid' AND cardinality($3::text[])>0 AND subject_value = ANY($3::text[]))
   OR (subject_kind='anonymous_id' AND cardinality($4::text[])>0 AND subject_value = ANY($4::text[])))`,
		project, environment, uids, anonymousIDs)
	if e != nil {
		return nil, nil, e
	}
	defer rows.Close()
	var kinds, values []string
	for rows.Next() {
		var kind, value string
		if e = rows.Scan(&kind, &value); e != nil {
			return nil, nil, e
		}
		kinds = append(kinds, kind)
		values = append(values, value)
	}
	return kinds, values, rows.Err()
}
