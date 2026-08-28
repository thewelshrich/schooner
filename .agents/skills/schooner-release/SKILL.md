---
name: schooner-release
description: Prepare or publish a Schooner version release from protected main, including a SemVer recommendation, verified narrative release notes, and the repository release script. Use when asked to cut, publish, or draft notes for a Schooner release. Do not use for ordinary development builds or Apple signing setup.
---

# Schooner Release

Keep judgment in this workflow and deterministic release invariants in
`scripts/release`. Read `CONTRIBUTING.md` and `docs/releasing.md` before
preparing a release.

## Decide whether this is a draft or a release

- For a notes-only or planning request, inspect and draft without creating or
  pushing a tag.
- For an actual release, require the exact version, target commit, and complete
  narrative notes to be shown to the user before the tag push. Treat approval
  as valid only after those exact values have been reviewed in the current
  conversation.
- A release tag is an external publication trigger. Do not infer permission to
  push it from earlier development, signing, or merge approval.

## Inspect the release content

Use the last reachable `v*` tag as the comparison base, or the beginning of
history for the first release. Inspect the commits, changed files, actual diff,
documentation, and relevant pull-request descriptions when available. The diff
is the source of truth when commit or PR wording overstates behavior.

Look specifically for:

- user-visible CLI behavior and new workflows;
- fixes, security consequences, and compatibility changes;
- configuration, stored-data, JSON, protocol, or migration impact;
- installation or upgrade actions users must take; and
- README claims that become stale when the first public release exists.

Do not edit `internal/runtime` protocol/schema versions, configuration schema
versions, or version test fixtures merely to cut a release. The release
workflow injects the product version, commit, and build time through linker
flags. For a proposed `v2` or later release, stop for an explicit Go module-path
and compatibility review before recommending the tag.

## Recommend the version

Apply Semantic Versioning to observable behavior:

- patch for backward-compatible fixes and internal/documentation-only work;
- minor for backward-compatible user-facing capability; and
- major for incompatible public behavior after `v1.0.0`.

Before `v1.0.0`, explain any compatibility break instead of silently assuming
which zero-major increment the maintainer wants. Present the recommendation and
reasoning; the maintainer chooses the final version.

## Draft the narrative notes

Write concise, user-facing Markdown with these sections when relevant:

```markdown
## Highlights

- The most important user-visible outcomes.

## Upgrade notes

Any action required, or `No action required.`

## Breaking changes

Specific incompatibilities and migration guidance, or `None.`
```

Do not repeat every commit or fabricate claims from filenames. GitHub appends
its generated, categorized pull-request list and full comparison link after
this narrative. Save the approved narrative to a temporary file outside the
working tree.

## Publish

Release only when the changes are already merged and all of these are true:

- the current branch is `main`;
- the working tree is clean;
- `HEAD` exactly matches `origin/main`; and
- the selected tag does not exist on the remote.

After the exact version, commit, and notes are approved, run:

```bash
scripts/release --yes --notes-file /absolute/path/to/notes.md vX.Y.Z
```

Do not reproduce its Git validation, test, tagging, or push implementation in
ad hoc commands. The script runs build, test, and vet checks, creates an
annotated tag whose message is the approved narrative, and pushes only that
tag. A failed push intentionally leaves a matching local tag that the same
command can safely reuse.

After the push, monitor the Release workflow. Tell the user when the protected
`release` environment needs approval. Completion requires the workflow to
build, sign, notarize, attest, and publish successfully and the GitHub release
to contain the approved narrative plus generated notes. If the remote tag
already exists, inspect the existing workflow/release instead of creating or
moving a tag.
