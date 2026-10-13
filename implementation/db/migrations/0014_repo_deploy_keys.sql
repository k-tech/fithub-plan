-- Demo-only repository SSH deployment credentials.
CREATE TABLE repo_deploy_keys (
    repo_id uuid PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 128),
    private_key text NOT NULL CHECK (octet_length(private_key) BETWEEN 1 AND 16384),
    updated_at timestamptz NOT NULL DEFAULT now()
);
