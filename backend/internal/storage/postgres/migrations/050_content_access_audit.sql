ALTER TABLE audit_entries
 ADD COLUMN environment text CHECK(environment IN ('development','staging','production')),
 ADD COLUMN outcome text NOT NULL DEFAULT 'success' CHECK(outcome IN ('success','failure'));

CREATE INDEX audit_entries_project_action_environment_time
 ON audit_entries(project_id,action,environment,occurred_at DESC,id DESC);
