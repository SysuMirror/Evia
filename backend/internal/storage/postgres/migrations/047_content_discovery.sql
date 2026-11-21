-- Version 047: bounded generic content discovery by stream and item order.
-- Decrypted bytes remain in the protected artifact store; this index covers
-- only authorization-scoped metadata needed to find complete content items.
CREATE INDEX content_item_chunks_stream_time
 ON content_item_chunks(project_id,environment,stream_id,received_at,item_id,chunk_index);
