#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
distribution="${repo_root}/.github/workflows/distribution.yml"
release="${repo_root}/.github/workflows/release.yml"

fail() {
  printf 'distribution_test: %s\n' "$*" >&2
  exit 1
}

require_distribution() {
  grep -Fq -- "$1" "${distribution}" || fail "distribution workflow is missing: $1"
}

require_release() {
  grep -Fq -- "$1" "${release}" || fail "release workflow is missing: $1"
}

require_distribution "repository_dispatch:"
require_distribution "release-published"
require_distribution "workflow_dispatch:"
require_distribution "distribution recovery refuses a non-latest release"
require_distribution '"immutable": release.get("immutable") is True'
require_distribution "scripts/public_release_smoke.sh"
require_distribution "macos-15-intel"
require_distribution "ubuntu-24.04-arm"
require_distribution "environment: tap-update"
require_distribution "actions/create-github-app-token@bcd2ba49218906704ab6c1aa796996da409d3eb1"
require_distribution 'app-id: ${{ vars.HOMEBREW_TAP_APP_ID }}'
require_distribution 'private-key: ${{ secrets.HOMEBREW_TAP_APP_PRIVATE_KEY }}'
require_distribution "scripts/render_homebrew_formula.py"
require_distribution "tap update branch exists without an open pull request"
require_distribution "chore: update schooner formula to"

require_release "Start non-gating distribution rollout"
require_release "client_payload[version]"
require_release "client_payload[release_id]"
require_release "client_payload[tag_commit]"
require_release "::warning::The immutable release succeeded, but distribution rollout dispatch failed"

printf 'distribution_test: all tests passed\n'
