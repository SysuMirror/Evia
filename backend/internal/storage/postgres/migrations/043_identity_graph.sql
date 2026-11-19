-- Version 043: identity graph edges, resolved persons, immutable revisions and
-- the fold watermark. The graph folds sys.identity_link facts (browser SDK
-- identify()) into bipartite uid/anonymous_id edges scoped to one project and
-- environment; connected components become resolved persons whose member sets
-- only grow. Merges retire the younger component and leave a redirect chain on
-- the person registry, so pinned historical person ids stay explainable. The
-- fold worker is the single writer and serializes per scope through the lease
-- columns on identity_fold_state; revisions are allocated as max(revision)+1
-- inside that lease and are never rewritten after completion.
CREATE TABLE identity_edges (
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 uid text NOT NULL CHECK(length(uid) BETWEEN 1 AND 128),
 anonymous_id text NOT NULL CHECK(length(anonymous_id) BETWEEN 1 AND 128),
 first_seen_at timestamptz NOT NULL,
 last_seen_at timestamptz NOT NULL,
 first_revision bigint NOT NULL CHECK(first_revision >= 1),
 last_revision bigint NOT NULL CHECK(last_revision >= first_revision),
 PRIMARY KEY(project_id,environment,uid,anonymous_id),
 CHECK(first_seen_at <= last_seen_at)
);
CREATE INDEX identity_edges_scope_seen
 ON identity_edges(project_id,environment,last_seen_at);

CREATE TABLE identity_persons (
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 resolved_person_id uuid NOT NULL,
 state text NOT NULL CHECK(state IN ('active','retired')),
 -- active persons never redirect; retired ones point at the surviving
 -- component except after erasure retirement, which leaves no successor.
 superseded_by uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 create_revision bigint NOT NULL CHECK(create_revision >= 1),
 PRIMARY KEY(project_id,environment,resolved_person_id),
 CHECK(state <> 'active' OR superseded_by IS NULL),
 CHECK(superseded_by IS NULL OR superseded_by <> resolved_person_id)
);

CREATE TABLE identity_person_members (
 project_id uuid NOT NULL,
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 resolved_person_id uuid NOT NULL,
 member_kind text NOT NULL CHECK(member_kind IN ('uid','anonymous_id')),
 member_value text NOT NULL CHECK(length(member_value) BETWEEN 1 AND 128),
 first_seen_at timestamptz NOT NULL,
 joined_revision bigint NOT NULL CHECK(joined_revision >= 1),
 PRIMARY KEY(project_id,environment,resolved_person_id,member_kind,member_value),
 -- Membership moves wholesale on merge, so one raw identity belongs to at
 -- most one person row within a scope at any time.
 UNIQUE(project_id,environment,member_kind,member_value),
 FOREIGN KEY(project_id,environment,resolved_person_id)
   REFERENCES identity_persons(project_id,environment,resolved_person_id)
);

CREATE TABLE identity_revisions (
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 revision bigint NOT NULL CHECK(revision >= 1),
 watermark_received_at timestamptz,
 edge_count bigint NOT NULL CHECK(edge_count >= 0),
 person_count bigint NOT NULL CHECK(person_count >= 0),
 member_count bigint NOT NULL CHECK(member_count >= 0),
 state text NOT NULL CHECK(state IN ('building','completed','failed')),
 built_at timestamptz,
 error_code text,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,environment,revision),
 CHECK(state = 'building' OR built_at IS NOT NULL),
 CHECK((state = 'failed') = (error_code IS NOT NULL)),
 CHECK(state <> 'completed' OR watermark_received_at IS NOT NULL)
);
CREATE INDEX identity_revisions_scope_time
 ON identity_revisions(project_id,environment,created_at,revision);

CREATE TABLE identity_fold_state (
 project_id uuid NOT NULL REFERENCES projects(id),
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 last_completed_revision bigint NOT NULL DEFAULT 0 CHECK(last_completed_revision >= 0),
 scan_watermark timestamptz,
 -- NULL means the scope has never completed a fold scan.
 lease_generation bigint NOT NULL DEFAULT 0,
 lease_owner text,
 lease_until timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,environment)
);
