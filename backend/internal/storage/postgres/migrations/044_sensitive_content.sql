-- Version 044: sensitive-content worker cursor and decrypted artifact index.
-- Ciphertexts remain in ClickHouse; plaintext chunks live only in the
-- protected content artifact store. PostgreSQL records safe metadata and
-- immutable object hashes for authorized assembly.
CREATE TABLE content_worker_state (
 id smallint PRIMARY KEY CHECK(id=1),
 last_received_at timestamptz NOT NULL DEFAULT 'epoch',
 last_event_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
 lease_generation bigint NOT NULL DEFAULT 0,
 lease_owner text,
 lease_until timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO content_worker_state(id) VALUES(1);

CREATE TABLE content_item_chunks (
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 event_id uuid NOT NULL,
 received_at timestamptz NOT NULL,
 stream_id text NOT NULL CHECK(length(stream_id) BETWEEN 1 AND 128),
 item_id text NOT NULL CHECK(length(item_id) BETWEEN 1 AND 128),
 actor_type text NOT NULL CHECK(length(actor_type) BETWEEN 1 AND 32),
 chunk_index integer NOT NULL CHECK(chunk_index BETWEEN 0 AND 31),
 chunk_count integer NOT NULL CHECK(chunk_count BETWEEN 1 AND 32 AND chunk_index<chunk_count),
 object_key text NOT NULL CHECK(object_key ~ '^[a-f0-9]{64}\.chunk$'),
 sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),
 key_id text NOT NULL CHECK(length(key_id) BETWEEN 1 AND 64),
 content_length integer NOT NULL CHECK(content_length BETWEEN 1 AND 1000000),
 truncated boolean NOT NULL,
 status text CHECK(status IS NULL OR status IN ('done','error','stopped')),
 duration_ms integer CHECK(duration_ms IS NULL OR duration_ms BETWEEN 0 AND 3600000),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,environment,event_id),
 UNIQUE(project_id,environment,item_id,chunk_index)
);
CREATE INDEX content_item_chunks_lookup
 ON content_item_chunks(project_id,environment,item_id,chunk_index);
