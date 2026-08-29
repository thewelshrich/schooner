#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
renderer="${repo_root}/scripts/render_homebrew_formula.py"
test_tmp="$(mktemp -d "${TMPDIR:-/tmp}/schooner-formula-test.XXXXXX")"
trap 'rm -rf -- "${test_tmp}"' EXIT

fail() {
  printf 'render_homebrew_formula_test: %s\n' "$*" >&2
  exit 1
}

write_manifest() {
  local path="$1" version="$2" index=0 os arch name
  : > "${path}"
  for os in darwin linux; do
    for arch in amd64 arm64; do
      for suffix in '' '.tar.gz'; do
        index=$((index + 1))
        name="schooner_${version}_${os}_${arch}${suffix}"
        printf '%064x  %s\n' "${index}" "${name}" >> "${path}"
      done
    done
  done
  LC_ALL=C sort -k2 "${path}" -o "${path}"
}

manifest="${test_tmp}/SHA256SUMS"
formula="${test_tmp}/Formula/schooner.rb"
write_manifest "${manifest}" v0.2.0

[[ "$(python3 "${renderer}" --version v0.2.0 --manifest "${manifest}" --output "${formula}")" == updated ]] || fail "initial render did not update"
[[ "$(python3 "${renderer}" --version v0.2.0 --manifest "${manifest}" --output "${formula}")" == unchanged ]] || fail "identical render was not idempotent"
grep -Fq 'version "0.2.0"' "${formula}" || fail "formula version is missing"
grep -Fq 'generate_completions_from_executable(bin/"schooner", "completion")' "${formula}" || fail "completion generation is missing"
grep -Fq 'schooner_v0.2.0_darwin_arm64.tar.gz' "${formula}" || fail "macOS arm64 archive is missing"
grep -Fq 'schooner_v0.2.0_linux_amd64.tar.gz' "${formula}" || fail "Linux amd64 archive is missing"
[[ "$(stat -f '%Lp' "${formula}" 2>/dev/null || stat -c '%a' "${formula}")" == 644 ]] || fail "formula mode is not 0644"

cp "${manifest}" "${test_tmp}/duplicate"
sed -n '1p' "${manifest}" >> "${test_tmp}/duplicate"
if python3 "${renderer}" --version v0.2.0 --manifest "${test_tmp}/duplicate" --output "${test_tmp}/duplicate.rb" 2>/dev/null; then
  fail "duplicate manifest entry was accepted"
fi

awk 'NR != 1' "${manifest}" > "${test_tmp}/missing"
if python3 "${renderer}" --version v0.2.0 --manifest "${test_tmp}/missing" --output "${test_tmp}/missing.rb" 2>/dev/null; then
  fail "missing manifest entry was accepted"
fi

cp "${manifest}" "${test_tmp}/unexpected"
printf '%064d  unexpected.tar.gz\n' 0 >> "${test_tmp}/unexpected"
if python3 "${renderer}" --version v0.2.0 --manifest "${test_tmp}/unexpected" --output "${test_tmp}/unexpected.rb" 2>/dev/null; then
  fail "unexpected manifest entry was accepted"
fi

cp "${manifest}" "${test_tmp}/unsafe"
printf '%064d  ../schooner.tar.gz\n' 0 >> "${test_tmp}/unsafe"
if python3 "${renderer}" --version v0.2.0 --manifest "${test_tmp}/unsafe" --output "${test_tmp}/unsafe.rb" 2>/dev/null; then
  fail "unsafe manifest entry was accepted"
fi

sed '1s/^[0-9a-f]\{64\}/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/' "${manifest}" > "${test_tmp}/uppercase"
if python3 "${renderer}" --version v0.2.0 --manifest "${test_tmp}/uppercase" --output "${test_tmp}/uppercase.rb" 2>/dev/null; then
  fail "uppercase digest was accepted"
fi

if python3 "${renderer}" --version v0.2.0-rc.1 --manifest "${manifest}" --output "${test_tmp}/prerelease.rb" 2>/dev/null; then
  fail "prerelease version was accepted"
fi

sed 's/version "0.2.0"/version "0.3.0"/' "${formula}" > "${test_tmp}/newer.rb"
if python3 "${renderer}" --version v0.2.0 --manifest "${manifest}" --output "${test_tmp}/newer.rb" 2>/dev/null; then
  fail "formula downgrade was accepted"
fi

printf '\n# tampered\n' >> "${formula}"
if python3 "${renderer}" --version v0.2.0 --manifest "${manifest}" --output "${formula}" 2>/dev/null; then
  fail "same-version formula drift was accepted"
fi

printf 'render_homebrew_formula_test: all tests passed\n'
