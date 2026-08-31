CREATE TABLE local_links (
    local_worktree TEXT PRIMARY KEY,
    box_id TEXT NOT NULL,
    expected_box_identity TEXT NOT NULL,
    remote_worktree TEXT NOT NULL,
    repository_identity TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX local_links_box
ON local_links(box_id);
