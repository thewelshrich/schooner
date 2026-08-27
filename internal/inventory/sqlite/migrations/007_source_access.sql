CREATE TABLE source_accounts (
    provider TEXT PRIMARY KEY CHECK (provider = 'github'),
    external_account_id TEXT NOT NULL,
    login TEXT NOT NULL,
    credential_key TEXT NOT NULL,
    credential_generation TEXT NOT NULL,
    access_expires_at TEXT NOT NULL,
    refresh_expires_at TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('connected', 'action_required')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE box_source_identities (
    box_identity TEXT NOT NULL,
    box_name TEXT NOT NULL,
    provider TEXT NOT NULL CHECK (provider = 'github'),
    external_account_id TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    remote_key_id INTEGER NOT NULL DEFAULT 0 CHECK (remote_key_id >= 0),
    remote_key_title TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('connecting', 'connected', 'disconnecting', 'cleanup_pending')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (box_identity, provider)
) STRICT;

CREATE UNIQUE INDEX box_source_remote_key
ON box_source_identities(provider, remote_key_id)
WHERE remote_key_id > 0;

CREATE INDEX box_source_name
ON box_source_identities(provider, box_name);
