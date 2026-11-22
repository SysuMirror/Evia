package postgres

import (
	"context"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/overview"
)

var _ overview.Authority = (*Store)(nil)

// AuthorizeOverview transactionally rechecks active human membership or the
// exact machine credential, queries.read grant, project and environment before
// the caller may touch ClickHouse facts.
func (s *Store) AuthorizeOverview(ctx context.Context, actor, project, environment string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err = s.authorizeOperation(ctx, tx, actor, project, environment, access.PermissionQueriesRead); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
