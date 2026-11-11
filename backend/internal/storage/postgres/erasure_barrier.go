package postgres

import (
	"context"

	"github.com/howtomakeaname/Evia/backend/internal/erasure"
)

// ErasedSubjects resolves the durable tombstones for one write batch. The
// write worker treats any lookup error as fail-closed: an unknown barrier
// answer must never let an erased identity back into ClickHouse.
func (s *Store) ErasedSubjects(ctx context.Context, project, environment string, uids, anonymousIDs []string) (erasure.ErasedSet, error) {
	set := erasure.ErasedSet{UIDs: map[string]struct{}{}, AnonymousIDs: map[string]struct{}{}}
	if len(uids) == 0 && len(anonymousIDs) == 0 {
		return set, nil
	}
	rows, e := s.Pool.Query(ctx, `SELECT subject_kind,subject_value FROM erasure_tombstones
 WHERE project_id=$1 AND environment=$2
 AND ((subject_kind='uid' AND cardinality($3::text[])>0 AND subject_value = ANY($3::text[]))
   OR (subject_kind='anonymous_id' AND cardinality($4::text[])>0 AND subject_value = ANY($4::text[])))`,
		project, environment, uids, anonymousIDs)
	if e != nil {
		return set, e
	}
	defer rows.Close()
	for rows.Next() {
		var kind, value string
		if e = rows.Scan(&kind, &value); e != nil {
			return set, e
		}
		if kind == erasure.SubjectUID {
			set.UIDs[value] = struct{}{}
		} else {
			set.AnonymousIDs[value] = struct{}{}
		}
	}
	return set, rows.Err()
}
