-- Version 025: managed analysis reports.
-- A report is an ordered, version-pinned compilation of already-published
-- saved analyses. It is deliberately distinct from a dashboard: dashboards are
-- spatial live surfaces, while a report is a reading-order document with no
-- layout coordinates. Every section foreign-keys a concrete published analysis
-- version, so a report can never reference a draft or a missing analysis.
CREATE TABLE reports (
 project_id uuid NOT NULL REFERENCES projects(id),
 report_id uuid NOT NULL,
 environment text NOT NULL CHECK(environment IN ('development','staging','production')),
 title text NOT NULL CHECK(length(title) BETWEEN 1 AND 128 AND title=btrim(title)),
 description text NOT NULL DEFAULT '' CHECK(length(description)<=2000),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision BETWEEN 1 AND 9007199254740991),
 definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id, report_id),
 UNIQUE(project_id, environment, report_id)
);
CREATE INDEX reports_scope ON reports(project_id, environment, report_id);

-- Sections are stored relationally so the published-analysis reference is a
-- real foreign key rather than an unchecked JSON pointer. reading_order keeps
-- the compilation sequence stable and unique within one report.
CREATE TABLE report_sections (
 project_id uuid NOT NULL,
 report_id uuid NOT NULL,
 environment text NOT NULL,
 section_id uuid NOT NULL,
 reading_order integer NOT NULL CHECK(reading_order BETWEEN 1 AND 50),
 section_title text NOT NULL CHECK(length(section_title) BETWEEN 1 AND 128),
 analysis_id uuid NOT NULL,
 analysis_version bigint NOT NULL CHECK(analysis_version BETWEEN 1 AND 9007199254740991),
 PRIMARY KEY(project_id, report_id, section_id),
 UNIQUE(project_id, report_id, reading_order),
 FOREIGN KEY(project_id, environment, report_id) REFERENCES reports(project_id, environment, report_id) ON DELETE CASCADE,
 FOREIGN KEY(project_id, environment, analysis_id, analysis_version)
  REFERENCES saved_analysis_versions(project_id, environment, analysis_id, version)
);
CREATE INDEX report_sections_report ON report_sections(project_id, report_id, reading_order);

-- Owner/editor human writes with the same replay semantics as the other
-- governance resources. A reused key with a different body conflicts; an
-- identical replay returns the originally accepted snapshot.
CREATE TABLE report_mutations (
 project_id uuid NOT NULL REFERENCES projects(id),
 actor_id uuid NOT NULL REFERENCES accounts(id),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(length(request_hash)=64),
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id, actor_id, idempotency_key)
);
CREATE INDEX report_mutations_actor ON report_mutations(actor_id, created_at);
