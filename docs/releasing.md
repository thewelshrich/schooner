# Releasing Schooner

Pushing a `v`-prefixed semantic-version tag builds four raw executables on
native GitHub-hosted runners:

```text
schooner_<version>_darwin_amd64
schooner_<version>_darwin_arm64
schooner_<version>_linux_amd64
schooner_<version>_linux_arm64
SHA256SUMS
```

Each binary is built with CGO disabled, reproducible path trimming, and linker
metadata for the version, full commit, and UTC commit timestamp. The workflow
runs each binary natively, publishes a required checksum manifest, and records
GitHub build-provenance attestations.

A manual workflow run performs the same build and verification but uploads a
workflow artifact instead of publishing a release.

Before the first public tag, repository administrators must enable GitHub
Immutable Releases. Homebrew packaging, archives, remote installation,
automatic updates, and Apple signing or notarization remain separate work.

Directly downloaded macOS binaries should be signed with Developer ID,
notarized, and tested with quarantine metadata before they become the primary
installation path. A project-owned Homebrew tap can provide an earlier
source-built installation path without making unsigned downloads the default.
