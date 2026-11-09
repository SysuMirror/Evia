-- Independent revisions prevent deleted/recreated membership and unrelated
-- settings writes from accidentally accepting stale project governance changes.
ALTER TABLE projects ADD COLUMN membership_revision bigint NOT NULL DEFAULT 1
 CHECK(membership_revision BETWEEN 1 AND 9007199254740991);
ALTER TABLE projects ADD COLUMN settings_revision bigint NOT NULL DEFAULT 1
 CHECK(settings_revision BETWEEN 1 AND 9007199254740991);

-- Human account provenance is deliberate: machines cannot administer governance.
-- Snapshots survive membership deletion and retain exact accepted responses.
CREATE TABLE project_governance_mutations (
 project_id uuid NOT NULL REFERENCES projects(id),
 actor_id uuid NOT NULL REFERENCES accounts(id),
 operation text NOT NULL CHECK(operation IN ('members.add','members.update','members.remove','settings.update')),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(length(request_hash)=64),
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,actor_id,operation,idempotency_key)
);
CREATE INDEX project_governance_mutations_actor ON project_governance_mutations(actor_id,created_at);

CREATE TABLE account_provision_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 actor_kind text NOT NULL CHECK(actor_kind='operator'),
 target_id uuid NOT NULL REFERENCES accounts(id),
 occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX account_provision_events_target_time ON account_provision_events(target_id,occurred_at DESC,id DESC);
