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
verifies it against a `SHA256SUMS` manifest. To use a locally built executable,
place both files in one directory and set `SCHOONER_ARTIFACT_DIR`:

```bash
artifact_dir="$(mktemp -d)"
remote_arch="arm64" # or amd64, matching the Box
artifact="schooner_dev_linux_${remote_arch}"
CGO_ENABLED=0 GOOS=linux GOARCH="${remote_arch}" go build -trimpath \
  -o "${artifact_dir}/${artifact}" ./cmd/schooner

# Linux:
(cd "${artifact_dir}" && sha256sum "${artifact}" > SHA256SUMS)
# macOS alternative:
# (cd "${artifact_dir}" && shasum -a 256 "${artifact}" > SHA256SUMS)

export SCHOONER_ARTIFACT_DIR="${artifact_dir}"
```

The override supports development and release versions but never bypasses
checksum verification. Without it, release artifacts are read from the
verified local cache or downloaded from the matching GitHub Release.

Bootstrap streams the verified bytes through system OpenSSH into a unique file
beside the final runtime. The Box rechecks SHA-256 and validates the staged
executable's identity, platform, and protocol before atomically installing it.
