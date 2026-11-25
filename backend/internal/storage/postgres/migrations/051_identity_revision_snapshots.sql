-- Version 051: immutable per-revision identity member snapshots and revision
-- pruning watermark. Each completed fold snapshots active person membership
-- under its revision number so historical revision pins (resolve and
-- resolved_person queries) answer deterministically from the exact graph state
-- captured by that revision. Deleting an expired revision cascades its member
-- snapshot rows, while pruned_through_revision lets pinned lookups distinguish
-- retention-pruned revisions (IDENTITY_REVISION_PRUNED) from unknown ones.
ALTER TABLE identity_revisions
 ADD COLUMN snapshot_available boolean NOT NULL DEFAULT false;

ALTER TABLE identity_fold_state
 ADD COLUMN pruned_through_revision bigint NOT NULL DEFAULT 0
 CHECK(pruned_through_revision >= 0);

CREATE TABLE identity_revision_members (
 project_id uuid NOT NULL,
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 revision bigint NOT NULL CHECK(revision >= 1),
 resolved_person_id uuid NOT NULL,
 member_kind text NOT NULL CHECK(member_kind IN ('uid','anonymous_id')),
 member_value text NOT NULL CHECK(length(member_value) BETWEEN 1 AND 128),
 first_seen_at timestamptz NOT NULL,
 joined_revision bigint NOT NULL CHECK(joined_revision >= 1 AND joined_revision <= revision),
 PRIMARY KEY(project_id,environment,revision,resolved_person_id,member_kind,member_value),
 UNIQUE(project_id,environment,revision,member_kind,member_value),
 FOREIGN KEY(project_id,environment,revision)
   REFERENCES identity_revisions(project_id,environment,revision)
   ON DELETE CASCADE
);

CREATE INDEX identity_revision_members_subject
 ON identity_revision_members(project_id,environment,member_kind,member_value,revision);

-- Backfill each scope's latest completed revision from the live member table
-- so upgraded scopes remain immediately resolvable after migrating. Older
-- pre-migration revisions with members stay snapshot_available = false and
-- continue failing closed.
INSERT INTO identity_revision_members
 (project_id,environment,revision,resolved_person_id,member_kind,member_value,first_seen_at,joined_revision)
SELECT m.project_id, m.environment, latest.revision, m.resolved_person_id,
       m.member_kind, m.member_value, m.first_seen_at, m.joined_revision
FROM identity_person_members m
JOIN (
 SELECT project_id, environment, MAX(revision) AS revision
 FROM identity_revisions
 WHERE state = 'completed'
 GROUP BY project_id, environment
) latest
  ON latest.project_id = m.project_id AND latest.environment = m.environment
WHERE m.joined_revision <= latest.revision
ON CONFLICT DO NOTHING;

UPDATE identity_revisions r
SET snapshot_available = true
WHERE r.state = 'completed'
  AND (
    r.member_count = 0
    OR r.revision = (
      SELECT MAX(r2.revision)
      FROM identity_revisions r2
      WHERE r2.project_id = r.project_id
        AND r2.environment = r.environment
        AND r2.state = 'completed'
    )
  );
