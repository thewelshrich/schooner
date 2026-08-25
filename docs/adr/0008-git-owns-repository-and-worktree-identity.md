# Git owns Repository and Worktree identity

Schooner uses Git's common directory and registered checkout paths as the only
Repository and Worktree identities. The filesystem and live
`git worktree list --porcelain -z` output remain authoritative; Schooner does
not persist alternate IDs, aliases, inventory, ownership flags, or lifecycle
state for them. Schooner orchestrates fixed Git operations and may annotate
only Schooner-owned Sessions and Operations with paths that are revalidated
before use. This avoids recreating Git's repository/worktree model while still
supporting remote discovery, safe selection, persistent sessions, consistent
status, and dirty-worktree protection.
