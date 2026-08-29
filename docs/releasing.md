# Releasing Schooner

Pushing a `v`-prefixed semantic-version tag builds four raw executables on
native GitHub-hosted runners and packages four additional archives:

```text
schooner_<version>_darwin_amd64
schooner_<version>_darwin_arm64
schooner_<version>_linux_amd64
schooner_<version>_linux_arm64
schooner_<version>_darwin_amd64.tar.gz
schooner_<version>_darwin_arm64.tar.gz
schooner_<version>_linux_amd64.tar.gz
schooner_<version>_linux_arm64.tar.gz
SHA256SUMS
```

Each binary is built with CGO disabled, reproducible path trimming, and linker
metadata for the version, full commit, and UTC commit timestamp. The workflow
runs each binary natively. It then signs both macOS executables with Developer
ID Application, submits them together to Apple's notary service, and requires
an accepted response with no reported issues. Checksums and GitHub
build-provenance attestations cover the resulting signed release binaries and
archives.

Each deterministic USTAR/gzip archive contains exactly `LICENSE`, `README.md`,
and `schooner`. Repository documentation has mode `0644`, the executable has
mode `0755`, ownership is numeric root, member timestamps use the release commit
time, and the gzip header carries no filename or timestamp. The archived
executable must have the same SHA-256 as the corresponding raw release asset.
The four raw names remain unchanged because remote Box runtime resolution uses
them directly.

Before publication, the installer consumes the verified bundle on native Linux
and macOS runners for amd64 and arm64. Each job installs, verifies, and reruns
the exact packaged executable. The release draft is published only after all
four jobs pass, and its asset set must contain exactly the eight executable or
archive assets plus `SHA256SUMS`.

A normal manual workflow run performs the same build and verification but
uploads a workflow artifact instead of publishing a release. A guarded recovery
mode can publish an existing annotated tag after its original workflow failed;
it never creates, deletes, or moves the tag.

## Branch and tag policy

`main` is the always-releasable trunk. Develop on short-lived branches, merge
them through pull requests with required CI, and do not maintain a long-lived
`dev` branch. Protect `main` from direct pushes, force pushes, and deletion. A
separate tag ruleset should protect `v*` tags from updates and deletion.

Publishing is deliberately separate from merging. A merge to `main` runs CI;
an annotated `v`-prefixed Semantic Versioning tag triggers the Release
workflow. The workflow rejects lightweight tags, tags whose annotation is
empty, and tags whose target commit is not contained in `main`. Normal manual
signed builds are accepted only from the current `origin/main` commit and never
publish a GitHub release. An existing-tag recovery must run from the matching
tag ref so the protected environment and build-provenance identity remain bound
to that immutable tag and its original commit.

## Release procedure

The project-owned `scripts/release` command is the release interface. Run it
from a clean, up-to-date `main` checkout after all intended changes have merged:

```bash
notes_file="$(mktemp -t schooner-release-notes)"
${EDITOR:-vi} "${notes_file}"
scripts/release --notes-file "${notes_file}" v0.1.0
```

The notes file should live outside the working tree. The command:

1. validates a `v`-prefixed semantic version and non-empty notes;
2. requires clean `main` at exactly `origin/main`;
3. rejects a tag that already exists on the remote;
4. runs `go build`, `go test ./...`, `go vet ./...`, and `git diff --check`;
5. shows the commits and complete notes for final confirmation;
6. creates an annotated tag containing those notes; and
7. pushes only that tag to `origin`.

Use `--dry-run` to run every check without creating or pushing a tag. `--yes`
is intended for an agent only after the maintainer has reviewed the exact
version, target commit, and complete notes. If a network failure leaves a local
tag but no remote tag, rerunning with the same version and notes safely reuses
the matching local annotated tag.

If the remote tag exists but its Release workflow failed before publication,
fix the workflow for a later release, then dispatch the workflow version stored
in the failed tag itself:

```bash
gh workflow run release.yml \
  --ref v0.2.0 \
  -f version=v0.2.0 \
  -f publish_existing_tag=true
```

The ref and version must be the same annotated tag. Recovery requires non-empty
tag notes and a tag target contained in `main`. It rebuilds that original
target, passes through the protected `release` environment again, and keeps the
attestation's source ref and digest aligned with the release tag. If publication
left a matching mutable draft, recovery verifies its title, notes, prerelease
state, and exact asset set before publishing it; an already-published release is
never replaced.

Only tags that already contain this recovery-capable workflow can use this
path, because GitHub executes the workflow definition stored at the dispatched
ref. If an older immutable tag predates the recovery logic, leave it untouched
and publish the fix under a new patch version. Never delete or move an immutable
tag to retry a release.

Codex discovers the repository-scoped `$schooner-release` skill from
`.agents/skills/schooner-release`. It inspects the actual changes since the
previous release, recommends a version, drafts the narrative notes, and asks
for approval before invoking the script. The skill does not replace the
script's checks and does not silently choose a version.

Schooner has no product-version file to bump for a release. Development builds
default to `dev`; the release workflow injects the tag, commit, and build time
with linker flags. Runtime protocol versions, configuration schema versions,
and version test fixtures change only when their own compatibility contracts
change. A `v2` or later release also requires review of Go's major-version
module path rules.

GitHub Immutable Releases must remain enabled. Published tags and releases are
never moved or replaced.

## Release notes

The annotated tag message is the approved, human-facing narrative. The publish
job prepends it to GitHub's automatically generated notes, which provide the
categorized pull-request list, contributors, and full comparison link.
`.github/release.yml` groups labeled pull requests into breaking changes,
features, fixes, and documentation, then puts unmatched work under other
changes. Apply `skip-changelog` only to a pull request that should be omitted
from the generated list.

Keep the narrative focused on outcomes rather than a second commit log:

```markdown
## Highlights

- The most important user-visible changes.

## Upgrade notes

No action required.

## Breaking changes

None.
```

## Apple signing and notarization

Both tagged and manual releases use the protected GitHub Actions environment
named `release`. Configure these as environment secrets rather than repository
secrets so that only the signing job can read them:

| Secret | Value |
| --- | --- |
| `APPLE_DEVELOPER_ID_P12_BASE64` | Base64-encoded, password-protected PKCS #12 export containing the Developer ID Application certificate and private key |
| `APPLE_DEVELOPER_ID_P12_PASSWORD` | Password used when exporting that PKCS #12 file |
| `APPLE_NOTARY_KEY_P8_BASE64` | Base64-encoded App Store Connect API private key |
| `APPLE_NOTARY_KEY_ID` | App Store Connect API key ID |
| `APPLE_NOTARY_ISSUER_ID` | App Store Connect API issuer ID |

Use a team App Store Connect API key dedicated to release notarization. Apple
only offers its `.p8` private key as a one-time download, so keep the original
in an appropriate secrets manager and do not commit either credential file.

Export the Developer ID Application identity from **Keychain Access > My
Certificates** as a password-protected `.p12`. On macOS, encode the two files
and set the secrets with GitHub CLI:

```bash
base64 -i DeveloperIDApplication.p12 | gh secret set APPLE_DEVELOPER_ID_P12_BASE64 --env release
gh secret set APPLE_DEVELOPER_ID_P12_PASSWORD --env release
base64 -i AuthKey_KEYID.p8 | gh secret set APPLE_NOTARY_KEY_P8_BASE64 --env release
gh secret set APPLE_NOTARY_KEY_ID --env release
gh secret set APPLE_NOTARY_ISSUER_ID --env release
```

The commands without piped input prompt for the value without putting it in
shell history. Repository administrators can instead enter the same values
under **Settings > Environments > release > Environment secrets**. Under
**Deployment branches and tags**, select only the protected `main` branch and
protected `v*` tags: manual signed builds use current `main`, while publication
and recovery use the matching version tag. Require approval if the repository's
GitHub plan supports it.

The workflow imports the signing identity into a temporary Keychain, adds that
Keychain to the runner's search list so `codesign` can combine the private key
with Apple's system certificate chain, and restores the original search list
during cleanup. It signs both Mac architectures with the hardened runtime,
identifier `app.schooner.cli`, and a secure timestamp. The workflow verifies the
exact Developer ID requirement for Team ID `LDCWNW7T7K`, then deletes the
temporary credential files. It downloads and
validates the notarization log and retains that log as a private workflow
artifact for 90 days. A failed or delayed notarization prevents bundling and
publication.

Apple cannot staple a notarization ticket directly to a standalone executable
or ZIP archive. Gatekeeper retrieves the ticket from Apple when a downloaded
Schooner binary is first assessed online.

## GitHub source-access registration

Release binaries require the public client ID of the GitHub App registration
used only by the Schooner Go CLI. Configure the registration with device flow
enabled, expiring user authorization tokens enabled, and the account permission
`Git SSH keys: Read and write`. No repository Contents permission or client
secret is used.

Set the public client ID as the repository Actions variable
`SCHOONER_GITHUB_CLIENT_ID`. The release workflow fails before producing a
binary when that variable is empty and links the value into
`main.githubClientID`. Development builds may instead set
`SCHOONER_GITHUB_CLIENT_ID` in the invoking environment; this override is read
at runtime and is intended only for local development.

Homebrew packaging and automatic local updates remain separate work. The first
archive-bearing release must be installed through the public repository script
on fresh macOS and Linux amd64/arm64 systems before direct installation becomes
the primary README path. macOS smoke testing includes quarantine and Gatekeeper
behavior in addition to the installer's exact local signature requirement.
