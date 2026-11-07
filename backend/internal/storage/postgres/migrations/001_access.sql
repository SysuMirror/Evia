CREATE TABLE accounts (
 id uuid PRIMARY KEY,
 username text NOT NULL UNIQUE CHECK (username = lower(username)),
 display_name text NOT NULL,
 password_hash text NOT NULL,
 disabled boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE sessions (
 secret_hash text PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 csrf_token text NOT NULL,
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_expiry ON sessions(expires_at);
CREATE TABLE login_buckets (
 bucket_key text PRIMARY KEY,
 window_start timestamptz NOT NULL,
 attempts integer NOT NULL CHECK(attempts>0)
);
CREATE INDEX login_buckets_window ON login_buckets(window_start);
CREATE TABLE projects (
 id uuid PRIMARY KEY,
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
 timezone text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE project_members (
 project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 user_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 role text NOT NULL CHECK(role IN ('owner','editor','viewer')),
 PRIMARY KEY(project_id,user_id)
);
CREATE INDEX project_members_user ON project_members(user_id,project_id);
CREATE TABLE audit_entries (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 project_id uuid REFERENCES projects(id),
 actor_id uuid NOT NULL REFERENCES accounts(id),
 action text NOT NULL,
 resource_key text NOT NULL,
 occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_entries_project_time ON audit_entries(project_id,occurred_at DESC,id DESC);
