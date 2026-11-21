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

var _ content.WorkerStore = (*Store)(nil)
var _ content.ReadStore = (*Store)(nil)

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
		if _, err = tx.Exec(ctx, `INSERT INTO content_item_chunks
		 (project_id,environment,event_id,received_at,content_type,stream_id,item_id,actor_type,chunk_index,chunk_count,
		  object_key,sha256,key_id,content_length,truncated,status,duration_ms)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,NULLIF($16,''),NULLIF($17,0))
		 ON CONFLICT DO NOTHING`,
			record.ProjectID, record.Environment, record.EventID, record.ReceivedAt, record.ContentType,
			record.StreamID, record.ItemID, record.ActorType, record.ChunkIndex, record.ChunkCount,
			record.ObjectKey, record.SHA256, record.KeyID, record.ContentLength, record.Truncated,
			record.Status, record.DurationMS); err != nil {
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

func contentDiscoveryFilter(filter content.DiscoveryFilter) error {
	if filter.Limit < 1 || filter.Limit > 100 || len(filter.ContentTypes) < 1 || len(filter.ContentTypes) > 8 || filter.AsOf.IsZero() {
		return fault.New(fault.Invalid, "INVALID_CONTENT_DISCOVERY", "内容发现条件无效")
	}
	for _, value := range filter.ContentTypes {
		if len(value) < 1 || len(value) > 64 {
			return fault.New(fault.Invalid, "INVALID_CONTENT_DISCOVERY", "内容类型无效")
		}
	}
	if filter.After != nil && (filter.After.ReceivedAt.IsZero() || len(filter.After.ID) < 1 || len(filter.After.ID) > 128) {
		return fault.New(fault.Invalid, "INVALID_CONTENT_DISCOVERY", "内容分页位置无效")
	}
	return nil
}

func (s *Store) ListContentStreams(ctx context.Context, actor, project, environment string, filter content.DiscoveryFilter) (content.StreamPage, error) {
	if err := contentDiscoveryFilter(filter); err != nil {
		return content.StreamPage{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return content.StreamPage{}, err
	}
	defer tx.Rollback(context.Background())
	if err = s.contentReadAuthority(ctx, tx, actor, project, environment); err != nil {
		return content.StreamPage{}, err
	}
	var afterTime any
	afterID := ""
	if filter.After != nil {
		afterTime, afterID = filter.After.ReceivedAt, filter.After.ID
	}
	rows, err := tx.Query(ctx, `WITH complete_items AS (
		SELECT content_type,stream_id,item_id,actor_type,min(received_at) AS received_at,max(chunk_count) AS chunk_count
		FROM content_item_chunks
		WHERE project_id=$1 AND environment=$2 AND content_type=ANY($3::text[]) AND created_at<=$4
		GROUP BY content_type,stream_id,item_id,actor_type
		HAVING min(chunk_count)=max(chunk_count) AND count(*)=max(chunk_count) AND min(chunk_index)=0 AND max(chunk_index)=max(chunk_count)-1
	), streams AS (
		SELECT stream_id,array_agg(DISTINCT content_type ORDER BY content_type) AS content_types,
		 array_agg(DISTINCT actor_type ORDER BY actor_type) AS actor_types,min(received_at) AS first_received_at,
		 max(received_at) AS last_received_at,count(*)::integer AS item_count
		FROM complete_items GROUP BY stream_id
	)
	SELECT stream_id,content_types,actor_types,first_received_at,last_received_at,item_count
	FROM streams WHERE ($5::timestamptz IS NULL OR last_received_at<$5 OR (last_received_at=$5 AND stream_id>$6))
	ORDER BY last_received_at DESC,stream_id ASC LIMIT $7`, project, environment, filter.ContentTypes, filter.AsOf,
		afterTime, afterID, filter.Limit+1)
	if err != nil {
		return content.StreamPage{}, err
	}
	defer rows.Close()
	page := content.StreamPage{Items: make([]content.StreamSummary, 0, filter.Limit)}
	for rows.Next() {
		var item content.StreamSummary
		if err = rows.Scan(&item.StreamID, &item.ContentTypes, &item.ActorTypes, &item.FirstReceivedAt, &item.LastReceivedAt, &item.ItemCount); err != nil {
			return content.StreamPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err = rows.Err(); err != nil {
		return content.StreamPage{}, err
	}
	if len(page.Items) > filter.Limit {
		last := page.Items[filter.Limit-1]
		page.Next = &content.DiscoveryPosition{ReceivedAt: last.LastReceivedAt, ID: last.StreamID}
		page.Items = page.Items[:filter.Limit]
	}
	if err = tx.Commit(ctx); err != nil {
		return content.StreamPage{}, err
	}
	return page, nil
}

func (s *Store) ListContentItems(ctx context.Context, actor, project, environment, streamID string, filter content.DiscoveryFilter) (content.ItemPage, error) {
	if len(streamID) < 1 || len(streamID) > 128 {
		return content.ItemPage{}, fault.New(fault.Invalid, "INVALID_CONTENT_STREAM", "内容流标识无效")
	}
	if err := contentDiscoveryFilter(filter); err != nil {
		return content.ItemPage{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return content.ItemPage{}, err
	}
	defer tx.Rollback(context.Background())
	if err = s.contentReadAuthority(ctx, tx, actor, project, environment); err != nil {
		return content.ItemPage{}, err
	}
	var afterTime any
	afterID := ""
	if filter.After != nil {
		afterTime, afterID = filter.After.ReceivedAt, filter.After.ID
	}
	rows, err := tx.Query(ctx, `WITH complete_items AS (
		SELECT content_type,stream_id,item_id,actor_type,min(received_at) AS received_at,max(chunk_count)::integer AS chunk_count,
		 max(content_length)::integer AS content_length,bool_or(truncated) AS truncated,
		 max(COALESCE(status,'')) AS status,max(COALESCE(duration_ms,0))::integer AS duration_ms
		FROM content_item_chunks
		WHERE project_id=$1 AND environment=$2 AND stream_id=$3 AND content_type=ANY($4::text[]) AND created_at<=$5
		GROUP BY content_type,stream_id,item_id,actor_type
		HAVING min(chunk_count)=max(chunk_count) AND count(*)=max(chunk_count) AND min(chunk_index)=0 AND max(chunk_index)=max(chunk_count)-1
	)
	SELECT content_type,stream_id,item_id,actor_type,received_at,chunk_count,content_length,truncated,status,duration_ms
	FROM complete_items WHERE ($6::timestamptz IS NULL OR received_at>$6 OR (received_at=$6 AND item_id>$7))
	ORDER BY received_at ASC,item_id ASC LIMIT $8`, project, environment, streamID, filter.ContentTypes, filter.AsOf,
		afterTime, afterID, filter.Limit+1)
	if err != nil {
		return content.ItemPage{}, err
	}
	defer rows.Close()
	page := content.ItemPage{Items: make([]content.ItemSummary, 0, filter.Limit)}
	for rows.Next() {
		var item content.ItemSummary
		if err = rows.Scan(&item.ContentType, &item.StreamID, &item.ItemID, &item.ActorType, &item.ReceivedAt,
			&item.ChunkCount, &item.ContentLength, &item.Truncated, &item.Status, &item.DurationMS); err != nil {
			return content.ItemPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err = rows.Err(); err != nil {
		return content.ItemPage{}, err
	}
	if len(page.Items) > filter.Limit {
		last := page.Items[filter.Limit-1]
		page.Next = &content.DiscoveryPosition{ReceivedAt: last.ReceivedAt, ID: last.ItemID}
		page.Items = page.Items[:filter.Limit]
	}
	if err = tx.Commit(ctx); err != nil {
		return content.ItemPage{}, err
	}
	return page, nil
}

func (s *Store) GetContentItem(ctx context.Context, actor, project, environment, itemID string) ([]content.Record, error) {
	if len(itemID) < 1 || len(itemID) > 128 {
		return nil, fault.New(fault.Invalid, "INVALID_CONTENT_ITEM", "内容项标识无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if err = s.contentReadAuthority(ctx, tx, actor, project, environment); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT project_id::text,environment,event_id::text,received_at,content_type,stream_id,item_id,actor_type,
	 chunk_index,chunk_count,object_key,sha256,key_id,content_length,truncated,COALESCE(status,''),COALESCE(duration_ms,0)
	 FROM content_item_chunks WHERE project_id=$1 AND environment=$2 AND item_id=$3 ORDER BY chunk_index`,
		project, environment, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []content.Record
	for rows.Next() {
		var record content.Record
		if err = rows.Scan(&record.ProjectID, &record.Environment, &record.EventID, &record.ReceivedAt, &record.ContentType,
			&record.StreamID, &record.ItemID, &record.ActorType, &record.ChunkIndex, &record.ChunkCount,
			&record.ObjectKey, &record.SHA256, &record.KeyID, &record.ContentLength, &record.Truncated,
			&record.Status, &record.DurationMS); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fault.New(fault.NotFound, "CONTENT_ITEM_NOT_FOUND", "内容项不存在或不可见")
	}
	return records, tx.Commit(ctx)
}
