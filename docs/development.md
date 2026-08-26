# Developing Schooner

Read [CONTRIBUTING.md](../CONTRIBUTING.md) first for the project's design,
dependency, command, provider, testing, and compatibility conventions.

## Build and verify

Schooner requires Go 1.27.0. The Go command can download the pinned toolchain
automatically when `GOTOOLCHAIN=auto` is enabled.

```bash
go build ./cmd/schooner
go test ./...
go vet ./...
```

## Development database compatibility

The pre-release `workspace_root` to `worktree_root` hard cut rewrites unreleased
SQLite migration checksums. Developers with an older development database must
reset it explicitly before using this build:

```bash
schooner db destroy --yes
```

Schooner never removes an incompatible database automatically.

## Ubuntu SSH smoke test

After `box setup` or `box update`, verify the same live Git state through the
workstation runtime and directly on the Ubuntu Box:

```bash
# Workstation
schooner clone git@github.com:owner/repository.git --box work-api
schooner worktree add repository repository-feature --branch feature --box work-api
schooner worktree list --box work-api
schooner worktree inspect repository --box work-api
schooner start repository --box work-api
schooner sessions --box work-api

# Directly on the Box
ssh work-api
~/.local/bin/schooner worktree list
~/.local/bin/schooner worktree inspect repository
~/.local/bin/schooner sessions
~/.local/bin/schooner resume repository
~/.local/bin/schooner worktree remove repository-feature
~/.local/bin/schooner worktree prune
```

The two paths must report the same canonical repository relationship, worktree
status, stable session ID, and tmux process state.

## Local remote-runtime artifacts

Remote bootstrap resolves a platform-specific executable by version and
verifies it against a `SHA256SUMS` manifest. From the Schooner source directory,
build verified development runtimes for both supported Linux architectures:

```bash
go run ./cmd/schooner dev artifacts
```

The command publishes `schooner_dev_linux_amd64`,
`schooner_dev_linux_arm64`, and `SHA256SUMS` as one atomic generation in
Schooner's development artifact cache. When `SCHOONER_ARTIFACT_DIR` is set, it
publishes to that override instead. Development builds find the active
directory automatically. Re-run the command after changing code that must run
on a Box.

`SCHOONER_ARTIFACT_DIR` remains available as an explicit override for a custom
artifact directory. Overrides support development and release versions but
never bypass checksum verification. Release artifacts are read from the
verified local cache or downloaded from the matching GitHub Release.

Bootstrap streams the verified bytes through system OpenSSH into a unique file
beside the final runtime. The Box rechecks SHA-256 and validates the staged
executable's identity, platform, and protocol before atomically installing it.
