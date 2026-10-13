-- OAuth identities are keyed by Google's stable subject, never by email.
ALTER TABLE users ADD COLUMN google_sub text UNIQUE
    CHECK (google_sub IS NULL OR length(google_sub) BETWEEN 1 AND 255);

CREATE TABLE browser_sessions (
    secret_hash text PRIMARY KEY CHECK (secret_hash ~ '^[0-9a-f]{64}$'),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);
CREATE INDEX browser_sessions_user_idx ON browser_sessions (user_id, created_at DESC);
CREATE INDEX browser_sessions_expiry_idx ON browser_sessions (expires_at) WHERE revoked_at IS NULL;

-- State is hashed; the browser also holds a short-lived, HttpOnly state cookie.
-- The verifier and nonce are server-side so the callback cannot be forged.
CREATE TABLE oidc_login_attempts (
    state_hash text PRIMARY KEY CHECK (state_hash ~ '^[0-9a-f]{64}$'),
    code_verifier text NOT NULL CHECK (length(code_verifier) BETWEEN 43 AND 128),
    nonce text NOT NULL CHECK (length(nonce) BETWEEN 32 AND 128),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);
CREATE INDEX oidc_login_attempts_expiry_idx ON oidc_login_attempts (expires_at)
    WHERE consumed_at IS NULL;
