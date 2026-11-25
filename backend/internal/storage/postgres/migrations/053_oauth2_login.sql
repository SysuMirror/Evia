CREATE TABLE oauth_identities (
 provider text NOT NULL CHECK (length(provider) BETWEEN 1 AND 64),
 subject text NOT NULL CHECK (length(subject) BETWEEN 1 AND 255),
 user_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 external_username text NOT NULL DEFAULT '',
 email text NOT NULL DEFAULT '',
 display_name text NOT NULL DEFAULT '',
 linked_at timestamptz NOT NULL DEFAULT now(),
 last_login_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (provider, subject)
);
CREATE INDEX oauth_identities_user ON oauth_identities (user_id);

CREATE TABLE oauth_auth_requests (
 state_hash text PRIMARY KEY,
 binding_hash text NOT NULL,
 provider text NOT NULL CHECK (length(provider) BETWEEN 1 AND 64),
 code_verifier text NOT NULL,
 redirect_uri text NOT NULL,
 next_path text NOT NULL DEFAULT '/projects',
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX oauth_auth_requests_expiry ON oauth_auth_requests (expires_at);
