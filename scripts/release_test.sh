#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
script_under_test="${repo_root}/scripts/release"
workflow_under_test="${repo_root}/.github/workflows/release.yml"
test_tmp="$(mktemp -d "${TMPDIR:-/tmp}/schooner-release-test.XXXXXX")"
test_tmp="$(cd "${test_tmp}" && pwd -P)"
trap 'rm -rf -- "${test_tmp}"' EXIT
export GOCACHE="${test_tmp}/go-cache"
export GOMODCACHE="${test_tmp}/go-mod-cache"

fail() {
  printf 'release_test: %s\n' "$*" >&2
  exit 1
}

require_workflow_invariant() {
  local invariant="$1"
  if ! grep -Fq -- "${invariant}" "${workflow_under_test}"; then
    fail "release workflow is missing invariant: ${invariant}"
  fi
}

make_fixture() {
  local name="$1"
  local fixture="${test_tmp}/${name}"
  local remote="${test_tmp}/${name}.git"

  mkdir -p "${fixture}/cmd/schooner" "${fixture}/scripts"
  cp "${script_under_test}" "${fixture}/scripts/release"
  printf 'module github.com/thewelshrich/schooner\n\ngo 1.23\n' > "${fixture}/go.mod"
  printf 'package main\n\nfunc main() {}\n' > "${fixture}/cmd/schooner/main.go"
  git -C "${fixture}" init --initial-branch=main --quiet
  git -C "${fixture}" config user.name "Release Test"
  git -C "${fixture}" config user.email "release-test@example.com"
  git -C "${fixture}" add .
  git -C "${fixture}" commit --quiet -m "feat: initial fixture"
  git init --bare --quiet "${remote}"
  git -C "${fixture}" remote add origin "${remote}"
  git -C "${fixture}" push --quiet --set-upstream origin main
  printf '%s\n' "${fixture}"
}

expect_failure_in_fixture() {
  local expected="$1"
  local fixture="$2"
  shift 2
  local output
  if output="$(cd "${fixture}" && scripts/release "$@" 2>&1)"; then
    fail "command unexpectedly succeeded in ${fixture}: scripts/release $*"
  fi
  if [[ "${output}" != *"${expected}"* ]]; then
    printf '%s\n' "${output}" >&2
    fail "failure did not contain: ${expected}"
  fi
}

notes_file="${test_tmp}/notes.md"
printf '## Highlights\n\n- First releasable behavior.\n' > "${notes_file}"

happy_fixture="$(make_fixture happy)"
(
  cd "${happy_fixture}"
  scripts/release --dry-run --notes-file "${notes_file}" v0.1.0 >/dev/null
)
if git -C "${happy_fixture}" show-ref --verify --quiet refs/tags/v0.1.0; then
  fail "dry run created a tag"
fi
(
  cd "${happy_fixture}"
  scripts/release --yes --notes-file "${notes_file}" v0.1.0 >/dev/null
)
[[ "$(git -C "${happy_fixture}" cat-file -t refs/tags/v0.1.0)" == "tag" ]] || fail "release did not create an annotated tag"
[[ "$(git -C "${happy_fixture}" for-each-ref --format='%(contents)' refs/tags/v0.1.0)" == "$(<"${notes_file}")" ]] || fail "release tag did not preserve the notes verbatim"
[[ -n "$(git --git-dir="${test_tmp}/happy.git" show-ref refs/tags/v0.1.0)" ]] || fail "release did not push the tag"

retry_fixture="$(make_fixture retry)"
git -C "${retry_fixture}" tag --annotate --cleanup=verbatim --file "${notes_file}" v0.1.0
(
  cd "${retry_fixture}"
  scripts/release --yes --notes-file "${notes_file}" v0.1.0 >/dev/null
)
[[ -n "$(git --git-dir="${test_tmp}/retry.git" show-ref refs/tags/v0.1.0)" ]] || fail "release did not push a matching existing tag"

expect_failure_in_fixture "v-prefixed semantic version" "${happy_fixture}" \
  --dry-run --notes-file "${notes_file}" 0.1.0

branch_fixture="$(make_fixture branch)"
git -C "${branch_fixture}" switch --quiet -c feature/test
expect_failure_in_fixture "releases must run from main" "${branch_fixture}" \
  --dry-run --notes-file "${notes_file}" v0.1.0

dirty_fixture="$(make_fixture dirty)"
printf 'dirty\n' > "${dirty_fixture}/untracked.txt"
expect_failure_in_fixture "working tree must be clean" "${dirty_fixture}" \
  --dry-run --notes-file "${notes_file}" v0.1.0

require_workflow_invariant "publish_existing_tag:"
require_workflow_invariant 'ref: ${{ needs.prepare.outputs.commit }}'
require_workflow_invariant "existing-tag recovery must run from the matching tag ref"
require_workflow_invariant "release tag target does not match the workflow ref"
require_workflow_invariant "release tag must point to a commit contained in main"
require_workflow_invariant "expected_intermediate_sha256="
require_workflow_invariant "grep -Fxq \"\${expected_intermediate_sha256}\""
require_workflow_invariant "if: needs.prepare.outputs.publish == 'true'"
require_workflow_invariant 'ref: ${{ needs.prepare.outputs.version }}'
require_workflow_invariant "existing release is not the expected resumable draft"
require_workflow_invariant 'gh release upload "${VERSION}" dist/* --clobber'
require_workflow_invariant "draft release assets: got"

printf 'release_test: all tests passed\n'
