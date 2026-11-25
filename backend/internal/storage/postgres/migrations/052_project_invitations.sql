CREATE TABLE project_invitations (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 role text NOT NULL CHECK (role IN ('owner', 'editor', 'viewer')),
 token_hash text NOT NULL UNIQUE,
 note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 200),
 created_by uuid NOT NULL REFERENCES accounts(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 accepted_by uuid REFERENCES accounts(id),
 accepted_at timestamptz,
 revoked_by uuid REFERENCES accounts(id),
 revoked_at timestamptz,
 CHECK (NOT (accepted_at IS NOT NULL AND revoked_at IS NOT NULL))
);

CREATE INDEX project_invitations_project_created
 ON project_invitations(project_id, created_at DESC, id DESC);
