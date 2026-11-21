package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/content"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/jackc/pgx/v5"
)

var _ content.Store = (*Store)(nil)

func (s *Store) ClaimContent(ctx context.Context, worker string, lease time.Duration) (*content.Claim, error) {
	if worker == "" || lease < 30*time.Second || lease > 5*time.Minute {
		return nil, fault.New(fault.Invalid, "INVALID_CONTENT_WORKER", "内容处理实例或租约无效")
	}
	var claim content.Claim
	claim.Worker = worker
	err := s.Pool.QueryRow(ctx, `UPDATE content_worker_state
	 SET lease_owner=$1,lease_until=clock_timestamp()+$2*interval '1 second',
	     lease_generation=lease_generation+1,updated_at=now()
	 WHERE id=1 AND (lease_until IS NULL OR lease_until<clock_timestamp())
	 RETURNING lease_generation,last_received_at,last_event_id::text`, worker, int(lease.Seconds())).
		Scan(&claim.Generation, &claim.Cursor.ReceivedAt, &claim.Cursor.EventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &claim, nil
}

func contentLeaseGuard(ctx context.Context, tx pgx.Tx, claim content.Claim) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM content_worker_state
	 WHERE id=1 AND lease_owner=$1 AND lease_generation=$2 AND lease_until>clock_timestamp() FOR UPDATE`,
		claim.Worker, claim.Generation).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fault.New(fault.Conflict, "CONTENT_LEASE_LOST", "内容处理租约已失效")
	}
	return err
}

func (s *Store) CompleteContent(ctx context.Context, claim content.Claim, records []content.Record, cursor content.Cursor) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err = contentLeaseGuard(ctx, tx, claim); err != nil {
		return err
	}
	for _, record := range records {
		if _, err = tx.Exec(ctx, `INSERT INTO content_message_chunks
		 (project_id,environment,event_id,received_at,conversation_id,message_id,role,chunk_index,chunk_count,
		  object_key,sha256,key_id,content_length,truncated,response_status,duration_ms)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,NULLIF($15,''),NULLIF($16,0))
		 ON CONFLICT(project_id,environment,event_id) DO NOTHING`,
			record.ProjectID, record.Environment, record.EventID, record.ReceivedAt,
			record.ConversationID, record.MessageID, record.Role, record.ChunkIndex, record.ChunkCount,
			record.ObjectKey, record.SHA256, record.KeyID, record.ContentLength, record.Truncated,
			record.ResponseStatus, record.DurationMS); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE content_worker_state SET last_received_at=$1,last_event_id=$2,
	 lease_owner=NULL,lease_until=NULL,updated_at=now()
	 WHERE id=1 AND lease_owner=$3 AND lease_generation=$4`, cursor.ReceivedAt, cursor.EventID, claim.Worker, claim.Generation)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fault.New(fault.Conflict, "CONTENT_LEASE_LOST", "内容处理租约已失效")
	}
	return tx.Commit(ctx)
}

func (s *Store) ReleaseContent(ctx context.Context, claim content.Claim) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE content_worker_state SET lease_owner=NULL,lease_until=NULL,updated_at=now()
	 WHERE id=1 AND lease_owner=$1 AND lease_generation=$2`, claim.Worker, claim.Generation)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fault.New(fault.Conflict, "CONTENT_LEASE_LOST", "内容处理租约已失效")
	}
	return nil
}

func (s *Store) contentReadAuthority(ctx context.Context, tx pgx.Tx, actor, project, environment string) error {
	identity, machine := access.IdentityFromContext(ctx)
	if machine {
		if identity.PrincipalID != actor {
			return machineDenied()
		}
		return s.authorizeOperation(ctx, tx, actor, project, environment, access.PermissionContentRead)
	}
	return memberOwnerTx(ctx, tx, actor, project)
}

func (s *Store) GetContentMessage(ctx context.Context, actor, project, environment, messageID string) ([]content.Record, error) {
	if len(messageID) < 1 || len(messageID) > 128 {
		return nil, fault.New(fault.Invalid, "INVALID_CONTENT_MESSAGE", "内容消息标识无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if err = s.contentReadAuthority(ctx, tx, actor, project, environment); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT project_id::text,environment,event_id::text,received_at,conversation_id,message_id,role,
	 chunk_index,chunk_count,object_key,sha256,key_id,content_length,truncated,COALESCE(response_status,''),COALESCE(duration_ms,0)
	 FROM content_message_chunks WHERE project_id=$1 AND environment=$2 AND message_id=$3 ORDER BY chunk_index`,
		project, environment, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []content.Record
	for rows.Next() {
		var record content.Record
		if err = rows.Scan(&record.ProjectID, &record.Environment, &record.EventID, &record.ReceivedAt,
			&record.ConversationID, &record.MessageID, &record.Role, &record.ChunkIndex, &record.ChunkCount,
			&record.ObjectKey, &record.SHA256, &record.KeyID, &record.ContentLength, &record.Truncated,
			&record.ResponseStatus, &record.DurationMS); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fault.New(fault.NotFound, "CONTENT_MESSAGE_NOT_FOUND", "内容消息不存在或不可见")
	}
	return records, tx.Commit(ctx)
}
