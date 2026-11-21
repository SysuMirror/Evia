-- Version 045: generic encrypted-content type dimension.
-- Product integrations map their own semantics (AI chat, private message,
-- session replay, search payload) into the platform content stream; the
-- platform indexes only this bounded type name and never hard-codes products.
ALTER TABLE content_item_chunks
 ADD COLUMN content_type text NOT NULL DEFAULT 'legacy_content'
 CHECK(length(content_type) BETWEEN 1 AND 64);
CREATE INDEX content_item_chunks_type
 ON content_item_chunks(project_id,environment,content_type,received_at DESC);
