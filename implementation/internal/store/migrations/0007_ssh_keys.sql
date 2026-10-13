CREATE TABLE ssh_public_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    key_type text NOT NULL CHECK (length(key_type) BETWEEN 1 AND 80),
    public_key text NOT NULL CHECK (length(public_key) BETWEEN 32 AND 8192),
    fingerprint text NOT NULL UNIQUE CHECK (fingerprint ~ '^SHA256:[A-Za-z0-9+/]+$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);
CREATE INDEX ssh_public_keys_user_created_idx ON ssh_public_keys (user_id, created_at DESC, id);
