#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
installer="${repo_root}/scripts/install.sh"
test_tmp="$(mktemp -d "${TMPDIR:-/tmp}/schooner-install-test.XXXXXX")"
trap 'rm -rf -- "${test_tmp}"' EXIT

fail() {
  printf 'install_test: %s\n' "$*" >&2
  exit 1
}

make_release() {
  local root="$1" version="$2" identity_arch="${3:-}"
  mkdir -p "${root}/dist"
  printf 'Apache test license\n' > "${root}/LICENSE"
  printf '# Schooner installer test\n' > "${root}/README.md"
  local os arch reported_arch target
  for os in darwin linux; do
    for arch in amd64 arm64; do
      reported_arch="${arch}"
      if [[ -n "${identity_arch}" && "${os}/${arch}" == "linux/amd64" ]]; then
        reported_arch="${identity_arch}"
      fi
      target="${root}/dist/schooner_${version}_${os}_${arch}"
      cat > "${target}" <<EOF
#!/usr/bin/env bash
set -euo pipefail
if [[ -n "\${TEST_CANDIDATE_MARKER:-}" ]]; then printf 'executed\n' > "\${TEST_CANDIDATE_MARKER}"; fi
if [[ -n "\${TEST_CANDIDATE_READY:-}" ]]; then
  : > "\${TEST_CANDIDATE_READY}"
  while [[ ! -f "\${TEST_CANDIDATE_RELEASE:-}" ]]; do sleep 1; done
fi
if [[ "\${1:-}" == version && "\${2:-}" == --output && "\${3:-}" == json ]]; then
  printf '%s\n' '{"schema_version":"1","version":"${version}","commit":"test","built_at":"2026-08-29T08:00:00Z","go_version":"go-test","os":"${os}","arch":"${reported_arch}"}'
  exit 0
fi
exit 64
EOF
    done
  done
  python3 "${repo_root}/scripts/package_release.py" \
    --version "${version}" \
    --built-at 2026-08-29T08:00:00Z \
    --source-root "${root}" \
    --dist "${root}/dist"
}

mkdir -p "${test_tmp}/fake-bin"
cat > "${test_tmp}/fake-bin/uname" <<'UNAME'
#!/usr/bin/env bash
case "${1:-}" in
  -s) printf '%s\n' "${TEST_UNAME_S:-Linux}" ;;
  -m) printf '%s\n' "${TEST_UNAME_M:-x86_64}" ;;
  -n) printf 'installer-test-host\n' ;;
  *) exit 64 ;;
esac
UNAME
cat > "${test_tmp}/fake-bin/curl" <<'CURL'
#!/usr/bin/env bash
set -euo pipefail
output=""
write_out=""
url=""
while (( $# > 0 )); do
  case "$1" in
    --output|-o|--write-out|-w|--proto|--proto-redir|--connect-timeout|--max-time|--max-filesize)
      key="$1"
      value="$2"
      shift 2
      if [[ "${key}" == "--output" || "${key}" == "-o" ]]; then output="${value}"; fi
      if [[ "${key}" == "--write-out" || "${key}" == "-w" ]]; then write_out="${value}"; fi
      ;;
    --fail|--silent|--show-error|--location|-f|-s|-S|-L) shift ;;
    *) url="$1"; shift ;;
  esac
done
if [[ "${url}" == "https://github.com/thewelshrich/schooner/releases/latest" ]]; then
  [[ "${write_out}" == *url_effective* ]] || exit 64
  printf 'https://github.com/thewelshrich/schooner/releases/tag/%s' "${TEST_LATEST_VERSION}"
  exit 0
fi
prefix="https://github.com/thewelshrich/schooner/releases/download/"
[[ "${url}" == "${prefix}"* && -n "${output}" ]] || exit 64
remainder="${url#${prefix}}"
version="${remainder%%/*}"
name="${remainder#*/}"
cp "${TEST_RELEASE_ROOT}/${version}/dist/${name}" "${output}"
CURL
chmod 0755 "${test_tmp}/fake-bin/uname" "${test_tmp}/fake-bin/curl"

make_release "${test_tmp}/v1.2.3" v1.2.3
make_release "${test_tmp}/v1.3.0-rc.1" v1.3.0-rc.1

invoke() {
  TEST_RELEASE_ROOT="${test_tmp}" \
  TEST_LATEST_VERSION="${TEST_LATEST_VERSION:-v1.2.3}" \
  TEST_UNAME_S="${TEST_UNAME_S:-Linux}" \
  TEST_UNAME_M="${TEST_UNAME_M:-x86_64}" \
  PATH="${test_tmp}/fake-bin:${PATH}" \
    bash "${installer}" --yes "$@"
}

expect_failure() {
  local expected="$1"
  shift
  local output
  if output="$(invoke "$@" 2>&1)"; then
    fail "installer unexpectedly succeeded: $*"
  fi
  [[ "${output}" == *"${expected}"* ]] || { printf '%s\n' "${output}" >&2; fail "failure did not contain: ${expected}"; }
}

invoke_from_root() {
  local release_root="$1"
  shift
  TEST_RELEASE_ROOT="${release_root}" \
  TEST_LATEST_VERSION=v1.2.3 \
  TEST_UNAME_S=Linux \
  TEST_UNAME_M=x86_64 \
  PATH="${test_tmp}/fake-bin:${PATH}" \
    bash "${installer}" --yes "$@"
}

expect_root_failure() {
  local release_root="$1" expected="$2"
  shift 2
  local output
  if output="$(invoke_from_root "${release_root}" "$@" 2>&1)"; then
    fail "installer unexpectedly accepted invalid release fixture"
  fi
  [[ "${output}" == *"${expected}"* ]] || { printf '%s\n' "${output}" >&2; fail "failure did not contain: ${expected}"; }
}

help="$(bash "${installer}" --help)"
[[ "${help}" == *"--install-dir"* && "${help}" == *"--version"* && "${help}" == *"--yes"* ]] || fail "help is incomplete"

unset_shell_dir="${test_tmp}/unset-shell"
output="$(env -u SHELL \
  TEST_RELEASE_ROOT="${test_tmp}" \
  TEST_LATEST_VERSION=v1.2.3 \
  TEST_UNAME_S=Linux \
  TEST_UNAME_M=x86_64 \
  PATH="${test_tmp}/fake-bin:${PATH}" \
  bash "${installer}" --version v1.2.3 --install-dir "${unset_shell_dir}" </dev/null)"
[[ -x "${unset_shell_dir}/schooner" && "${output}" == *"is not on PATH"* ]] || fail "unset SHELL did not fall back to manual PATH guidance"

grep -Fq 'elif [[ -f "${HOME}/.bash_profile" ]]' "${installer}" || fail "existing .bash_profile is not preferred before .profile"
grep -Fq 'elif [[ -f "${HOME}/.bash_login" ]]' "${installer}" || fail "existing .bash_login is not preferred before .profile"

install_dir="${test_tmp}/installed"
output="$(invoke --install-dir "${install_dir}")"
[[ "${output}" == *$'\nschooner\n'* && "${output}" == *"▁▂▄▆▆▄▂▁"* && "${output}" == *"Install Schooner"* && "${output}" == *"Version"* && "${output}" == *"v1.2.3"* && "${output}" == *"Installed Schooner v1.2.3"* && "${output}" == *"Downloaded and verified archive"* && "${output}" == *'export PATH='* ]] || fail "latest installation output is incomplete"
[[ -x "${install_dir}/schooner" && -f "${install_dir}/.schooner-install-receipt.json" ]] || fail "installation files are missing"
[[ "$("${install_dir}/schooner" version --output json)" == *'"version":"v1.2.3"'* ]] || fail "installed candidate is invalid"

RECEIPT="${install_dir}/.schooner-install-receipt.json" TARGET="${install_dir}/schooner" python3 - <<'PY'
import hashlib
import json
import os
from pathlib import Path

receipt_path = Path(os.environ["RECEIPT"])
target = Path(os.environ["TARGET"])
receipt = json.loads(receipt_path.read_text())
assert receipt["schema_version"] == "1"
assert receipt["installation_method"] == "direct"
assert receipt["executable_path"] == str(target.resolve())
assert receipt["version"] == "v1.2.3"
assert receipt["executable_sha256"] == hashlib.sha256(target.read_bytes()).hexdigest()
assert receipt["release_asset_kind"] == "archive"
assert receipt["release_asset_name"] == "schooner_v1.2.3_linux_amd64.tar.gz"
assert len(receipt["release_asset_sha256"]) == 64
PY

invoke --version v1.2.3 --install-dir "${install_dir}" >/dev/null

interrupted_dir="${test_tmp}/interrupted"
invoke --version v1.2.3 --install-dir "${interrupted_dir}" >/dev/null
tar -xOzf "${test_tmp}/v1.3.0-rc.1/dist/schooner_v1.3.0-rc.1_linux_amd64.tar.gz" schooner > "${interrupted_dir}/schooner"
chmod 0755 "${interrupted_dir}/schooner"
invoke --version v1.3.0-rc.1 --install-dir "${interrupted_dir}" >/dev/null
grep -Fq '  "version": "v1.3.0-rc.1",' "${interrupted_dir}/.schooner-install-receipt.json" || fail "interrupted receipt promotion was not repaired"

prerelease_dir="${test_tmp}/prerelease"
invoke --version v1.3.0-rc.1 --install-dir "${prerelease_dir}" >/dev/null
[[ "$("${prerelease_dir}/schooner" version --output json)" == *'"version":"v1.3.0-rc.1"'* ]] || fail "explicit prerelease was not installed"

TEST_LATEST_VERSION=v1.3.0-rc.1 expect_failure "unexpectedly resolved to a prerelease" --install-dir "${test_tmp}/latest-prerelease"
TEST_UNAME_M=mips64 expect_failure "architecture mips64 is not supported" --install-dir "${test_tmp}/unsupported"

arm_dir="${test_tmp}/arm"
TEST_UNAME_M=aarch64 invoke --version v1.2.3 --install-dir "${arm_dir}" >/dev/null
[[ "$("${arm_dir}/schooner" version --output json)" == *'"arch":"arm64"'* ]] || fail "aarch64 was not normalized"

unknown_dir="${test_tmp}/unknown"
mkdir -p "${unknown_dir}"
printf 'user-owned\n' > "${unknown_dir}/schooner"
expect_failure "source-built, manual, or has a stale receipt" --version v1.2.3 --install-dir "${unknown_dir}"
[[ "$(<"${unknown_dir}/schooner")" == "user-owned" ]] || fail "unknown target was replaced"

symlink_dir="${test_tmp}/symlink"
mkdir -p "${symlink_dir}"
ln -s /bin/false "${symlink_dir}/schooner"
expect_failure "package-managed or is a symlink" --version v1.2.3 --install-dir "${symlink_dir}"
[[ -L "${symlink_dir}/schooner" ]] || fail "symlink target was replaced"

repair_dir="${test_tmp}/repair"
mkdir -p "${repair_dir}"
tar -xOzf "${test_tmp}/v1.2.3/dist/schooner_v1.2.3_linux_amd64.tar.gz" schooner > "${repair_dir}/schooner"
chmod 0755 "${repair_dir}/schooner"
cp "${repair_dir}/schooner" "${test_tmp}/repair-before"
invoke --version v1.2.3 --install-dir "${repair_dir}" >/dev/null
cmp "${test_tmp}/repair-before" "${repair_dir}/schooner" >/dev/null || fail "receipt repair replaced identical executable bytes"
[[ -f "${repair_dir}/.schooner-install-receipt.json" ]] || fail "identical-byte receipt repair failed"

printf 'changed\n' >> "${install_dir}/schooner"
expect_failure "stale receipt" --version v1.2.3 --install-dir "${install_dir}"

insecure_receipt_dir="${test_tmp}/insecure-receipt"
invoke --version v1.2.3 --install-dir "${insecure_receipt_dir}" >/dev/null
chmod 0666 "${insecure_receipt_dir}/.schooner-install-receipt.json"
expect_failure "stale receipt" --version v1.2.3 --install-dir "${insecure_receipt_dir}"

busy_dir="${test_tmp}/busy"
mkdir -p "${busy_dir}/.schooner-install.lock"
busy_dir="$(cd "${busy_dir}" && pwd -P)"
host="$(hostname 2>/dev/null || uname -n)"
cat > "${busy_dir}/.schooner-install.lock/owner" <<EOF
schema_version=1
host=${host}
pid=$$
target=${busy_dir}/schooner
token=busy.test
EOF
chmod 0600 "${busy_dir}/.schooner-install.lock/owner"
expect_failure "another Schooner installation is active" --version v1.2.3 --install-dir "${busy_dir}"

stale_dir="${test_tmp}/stale"
mkdir -p "${stale_dir}/.schooner-install.lock"
stale_dir="$(cd "${stale_dir}" && pwd -P)"
cat > "${stale_dir}/.schooner-install.lock/owner" <<EOF
schema_version=1
host=${host}
pid=99999999
target=${stale_dir}/schooner
token=stale.test
EOF
chmod 0600 "${stale_dir}/.schooner-install.lock/owner"
cp "${stale_dir}/.schooner-install.lock/owner" "${test_tmp}/stale-owner"
# Concurrent callers must both refuse, preserving the same owner record.
expect_failure "stop all installers and updaters" --version v1.2.3 --install-dir "${stale_dir}" &
stale_first=$!
expect_failure "stop all installers and updaters" --version v1.2.3 --install-dir "${stale_dir}" &
stale_second=$!
wait "${stale_first}"
wait "${stale_second}"
cmp "${test_tmp}/stale-owner" "${stale_dir}/.schooner-install.lock/owner" || fail "stale owner changed"
rm "${stale_dir}/.schooner-install.lock/owner"
rmdir "${stale_dir}/.schooner-install.lock"
invoke --version v1.2.3 --install-dir "${stale_dir}" >/dev/null
[[ ! -e "${stale_dir}/.schooner-install.lock" ]] || fail "lock remains after manual recovery"

cancel_dir="${test_tmp}/cancelled"
ready="${test_tmp}/candidate-ready"
release_candidate="${test_tmp}/release-candidate"
TEST_CANDIDATE_READY="${ready}" \
TEST_CANDIDATE_RELEASE="${release_candidate}" \
TEST_RELEASE_ROOT="${test_tmp}" \
TEST_LATEST_VERSION=v1.2.3 \
TEST_UNAME_S=Linux \
TEST_UNAME_M=x86_64 \
PATH="${test_tmp}/fake-bin:${PATH}" \
  bash "${installer}" --yes --version v1.2.3 --install-dir "${cancel_dir}" >"${test_tmp}/cancel.out" 2>&1 &
installer_pid=$!
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
  [[ -e "${ready}" ]] && break
  kill -0 "${installer_pid}" 2>/dev/null || fail "installer exited before cancellation fixture was ready"
  sleep 0.1
done
[[ -e "${ready}" ]] || { touch "${release_candidate}"; fail "candidate did not become ready for cancellation"; }
kill -TERM "${installer_pid}"
if wait "${installer_pid}"; then
  fail "cancelled installer unexpectedly succeeded"
fi
[[ ! -e "${cancel_dir}/schooner" && ! -e "${cancel_dir}/.schooner-install-receipt.json" && ! -e "${cancel_dir}/.schooner-install.lock" ]] || fail "cancelled installer left promoted state or a lock"

corrupt_root="${test_tmp}/corrupt"
cp -R "${test_tmp}/v1.2.3" "${corrupt_root}"
printf 'corrupt\n' >> "${corrupt_root}/dist/schooner_v1.2.3_linux_amd64.tar.gz"
original_root="${test_tmp}"
mkdir -p "${test_tmp}/corrupt-releases/v1.2.3"
cp -R "${corrupt_root}/dist" "${test_tmp}/corrupt-releases/v1.2.3/dist"
if output="$(TEST_RELEASE_ROOT="${test_tmp}/corrupt-releases" TEST_LATEST_VERSION=v1.2.3 PATH="${test_tmp}/fake-bin:${PATH}" bash "${installer}" --yes --version v1.2.3 --install-dir "${test_tmp}/corrupt-install" 2>&1)"; then
  fail "installer accepted a corrupt archive"
fi
[[ "${output}" == *"archive checksum mismatch"* ]] || fail "corrupt archive failure was unclear"

duplicate_root="${test_tmp}/duplicate-releases"
mkdir -p "${duplicate_root}/v1.2.3"
cp -R "${test_tmp}/v1.2.3/dist" "${duplicate_root}/v1.2.3/dist"
sed -n '1p' "${duplicate_root}/v1.2.3/dist/SHA256SUMS" >> "${duplicate_root}/v1.2.3/dist/SHA256SUMS"
expect_root_failure "${duplicate_root}" "duplicate entry" --version v1.2.3 --install-dir "${test_tmp}/duplicate-install"

unsafe_root="${test_tmp}/unsafe-releases"
mkdir -p "${unsafe_root}/v1.2.3"
cp -R "${test_tmp}/v1.2.3/dist" "${unsafe_root}/v1.2.3/dist"
printf '%064d  ../schooner\n' 0 >> "${unsafe_root}/v1.2.3/dist/SHA256SUMS"
expect_root_failure "${unsafe_root}" "unsafe entry" --version v1.2.3 --install-dir "${test_tmp}/unsafe-install"

missing_root="${test_tmp}/missing-releases"
mkdir -p "${missing_root}/v1.2.3"
cp -R "${test_tmp}/v1.2.3/dist" "${missing_root}/v1.2.3/dist"
awk '$2 != "schooner_v1.2.3_linux_amd64"' "${missing_root}/v1.2.3/dist/SHA256SUMS" > "${missing_root}/manifest"
mv "${missing_root}/manifest" "${missing_root}/v1.2.3/dist/SHA256SUMS"
expect_root_failure "${missing_root}" "does not contain the selected raw executable" --version v1.2.3 --install-dir "${test_tmp}/missing-install"

members_root="${test_tmp}/members-releases"
mkdir -p "${members_root}/v1.2.3"
cp -R "${test_tmp}/v1.2.3/dist" "${members_root}/v1.2.3/dist"
MEMBERS_DIST="${members_root}/v1.2.3/dist" python3 - <<'PY'
import hashlib
import io
import os
from pathlib import Path
import tarfile

dist = Path(os.environ["MEMBERS_DIST"])
archive = dist / "schooner_v1.2.3_linux_amd64.tar.gz"
with tarfile.open(archive, "r:gz") as source:
    values = {member.name: source.extractfile(member).read() for member in source.getmembers()}
with tarfile.open(archive, "w:gz") as target:
    for name in ("LICENSE", "README.md", "schooner", "EXTRA"):
        contents = values.get(name, b"unexpected\n")
        member = tarfile.TarInfo(name)
        member.size = len(contents)
        member.mode = 0o755 if name == "schooner" else 0o644
        target.addfile(member, io.BytesIO(contents))
digest = hashlib.sha256(archive.read_bytes()).hexdigest()
manifest = dist / "SHA256SUMS"
lines = manifest.read_text().splitlines()
manifest.write_text("\n".join(
    f"{digest}  {archive.name}" if line.split()[-1] == archive.name else line
    for line in lines
) + "\n")
PY
expect_root_failure "${members_root}" "invalid member set" --version v1.2.3 --install-dir "${test_tmp}/members-install"

mismatch_root="${test_tmp}/mismatch-releases/v1.2.3"
make_release "${mismatch_root}" v1.2.3 arm64
if output="$(TEST_RELEASE_ROOT="${test_tmp}/mismatch-releases" TEST_LATEST_VERSION=v1.2.3 PATH="${test_tmp}/fake-bin:${PATH}" bash "${installer}" --yes --version v1.2.3 --install-dir "${test_tmp}/mismatch-install" 2>&1)"; then
  fail "installer accepted mismatched candidate identity"
fi
[[ "${output}" == *"candidate architecture does not match amd64"* ]] || fail "candidate mismatch failure was unclear"

marker="${test_tmp}/mac-candidate-executed"
if output="$(TEST_CANDIDATE_MARKER="${marker}" TEST_UNAME_S=Darwin TEST_UNAME_M=arm64 TEST_RELEASE_ROOT="${test_tmp}" TEST_LATEST_VERSION=v1.2.3 PATH="${test_tmp}/fake-bin:${PATH}" bash "${installer}" --yes --version v1.2.3 --install-dir "${test_tmp}/unsigned-mac" 2>&1)"; then
  fail "installer accepted an unsigned macOS candidate"
fi
[[ ! -e "${marker}" ]] || fail "unsigned macOS candidate executed before signature verification"

printf 'install_test: all tests passed\n'
