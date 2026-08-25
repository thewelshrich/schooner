CREATE TABLE boxes_new (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    acquisition TEXT NOT NULL CHECK (acquisition IN ('adopted', 'provisioned')),
    ssh_destination TEXT NOT NULL,
    identity_file TEXT NOT NULL DEFAULT '',
    remote_identity TEXT NOT NULL UNIQUE,
    worktree_root TEXT NOT NULL,
    provider TEXT,
    provider_resource_id TEXT,
    provider_correlation_id TEXT,
    credential_profile TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (
        (acquisition = 'adopted' AND provider IS NULL AND provider_resource_id IS NULL AND provider_correlation_id IS NULL AND credential_profile IS NULL)
        OR
        (acquisition = 'provisioned' AND provider IS NOT NULL AND provider_resource_id IS NOT NULL AND provider_correlation_id IS NOT NULL AND credential_profile IS NOT NULL)
    )
) STRICT;

INSERT INTO boxes_new (
    id, name, acquisition, ssh_destination, remote_identity, worktree_root,
    created_at, updated_at
)
SELECT id, name, acquisition, ssh_destination, remote_identity, worktree_root,
       created_at, updated_at
FROM boxes;

CREATE TABLE observations_new (
    box_id TEXT PRIMARY KEY REFERENCES boxes_new(id) ON DELETE CASCADE,
    observed_at TEXT NOT NULL,
    os_id TEXT NOT NULL,
    os_version TEXT NOT NULL,
    architecture TEXT NOT NULL,
    home TEXT NOT NULL,
    remote_identity TEXT NOT NULL,
    worktree_root TEXT NOT NULL,
    worktree_root_exists INTEGER NOT NULL,
    git_available INTEGER NOT NULL,
    git_version TEXT NOT NULL,
    tmux_available INTEGER NOT NULL,
    tmux_version TEXT NOT NULL,
    passwordless_sudo INTEGER NOT NULL
) STRICT;

INSERT INTO observations_new SELECT * FROM observations;
DROP TABLE observations;
DROP TABLE boxes;
ALTER TABLE boxes_new RENAME TO boxes;
ALTER TABLE observations_new RENAME TO observations;

CREATE TABLE credential_profiles (
    ref TEXT PRIMARY KEY,
    provider TEXT NOT NULL CHECK (provider = 'digitalocean'),
    name TEXT NOT NULL,
    external_account_id TEXT NOT NULL,
    account_name TEXT NOT NULL,
    account_email TEXT NOT NULL,
    credential_key TEXT NOT NULL,
    is_default INTEGER NOT NULL CHECK (is_default IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(provider, name),
    UNIQUE(provider, external_account_id)
) STRICT;

CREATE UNIQUE INDEX one_default_credential_profile
ON credential_profiles(provider)
WHERE is_default = 1;

CREATE TABLE provision_operations (
    name TEXT PRIMARY KEY,
    correlation_id TEXT NOT NULL UNIQUE,
    credential_profile TEXT NOT NULL,
    region TEXT NOT NULL,
    size TEXT NOT NULL,
    image TEXT NOT NULL,
    network_id TEXT NOT NULL,
    access_key_ids_json TEXT NOT NULL,
    automatic_backups INTEGER NOT NULL,
    ipv6 INTEGER NOT NULL,
    worktree_root TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    ssh_destination TEXT NOT NULL,
    identity_file TEXT NOT NULL,
    checkpoint TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE destroy_operations (
    box_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    provider TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    credential_profile TEXT NOT NULL,
    checkpoint TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
