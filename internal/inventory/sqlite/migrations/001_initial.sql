CREATE TABLE boxes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    acquisition TEXT NOT NULL CHECK (acquisition = 'adopted'),
    ssh_destination TEXT NOT NULL,
    remote_identity TEXT NOT NULL UNIQUE,
    workspace_root TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE observations (
    box_id TEXT PRIMARY KEY REFERENCES boxes(id) ON DELETE CASCADE,
    observed_at TEXT NOT NULL,
    os_id TEXT NOT NULL,
    os_version TEXT NOT NULL,
    architecture TEXT NOT NULL,
    home TEXT NOT NULL,
    remote_identity TEXT NOT NULL,
    workspace_root TEXT NOT NULL,
    workspace_root_exists INTEGER NOT NULL,
    git_available INTEGER NOT NULL,
    git_version TEXT NOT NULL,
    tmux_available INTEGER NOT NULL,
    tmux_version TEXT NOT NULL,
    passwordless_sudo INTEGER NOT NULL
) STRICT;

CREATE TABLE add_operations (
    name TEXT PRIMARY KEY,
    ssh_destination TEXT NOT NULL,
    workspace_root TEXT NOT NULL,
    checkpoint TEXT NOT NULL,
    remote_identity TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
