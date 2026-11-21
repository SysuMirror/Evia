-- Version 046: remove product-specific constraints left by the original
-- sensitive-message migration. Platform content semantics are generic; product
-- integrations own actor and status vocabularies within bounded strings.
ALTER TABLE content_item_chunks
 DROP CONSTRAINT IF EXISTS content_message_chunks_role_check,
 DROP CONSTRAINT IF EXISTS content_message_chunks_response_status_check,
 DROP CONSTRAINT IF EXISTS content_item_chunks_actor_type_check,
 DROP CONSTRAINT IF EXISTS content_item_chunks_status_check;

ALTER TABLE content_item_chunks
 ADD CONSTRAINT content_item_chunks_actor_type_check
  CHECK(length(actor_type) BETWEEN 1 AND 32),
 ADD CONSTRAINT content_item_chunks_status_check
  CHECK(status IS NULL OR length(status) BETWEEN 1 AND 32);
