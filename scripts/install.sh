#!/usr/bin/env bash

set -euo pipefail
umask 077

repository="thewelshrich/schooner"
github_root="https://github.com/${repository}"
release_root="${github_root}/releases"
receipt_name=".schooner-install-receipt.json"
lock_name=".schooner-install.lock"
apple_team_id="LDCWNW7T7K"
apple_identifier="app.schooner.cli"
max_manifest_bytes=1048576
max_archive_bytes=536870912
max_executable_bytes=268435456

temporary_root=""
candidate_path=""
lock_directory=""
lock_token=""
lock_held=false
candidate_child=""
watchdog_child=""

usage() {
  cat <<'USAGE'
Install a verified Schooner release for the current user.

Usage: install.sh [--version VERSION] [--install-dir DIRECTORY]

Options:
  --version VERSION       Install one exact v-prefixed release version
  --install-dir DIRECTORY Install into DIRECTORY instead of ~/.local/bin
  -h, --help              Show this help
USAGE
}

fail() {
  printf 'install.sh: %s\n' "$*" >&2
  exit 1
}

cleanup_lock() {
  if [[ "${lock_held}" != true || -z "${lock_directory}" || -z "${lock_token}" ]]; then
    return
  fi
  local owner="${lock_directory}/owner"
  if [[ -f "${owner}" ]] && grep -Fxq "token=${lock_token}" "${owner}" 2>/dev/null; then
    rm -f "${owner}" 2>/dev/null || true
    rmdir "${lock_directory}" 2>/dev/null || true
  fi
  lock_held=false
}

cleanup() {
  local status=$?
  if [[ -n "${candidate_child}" ]]; then
    kill -TERM "${candidate_child}" 2>/dev/null || true
  fi
  if [[ -n "${watchdog_child}" ]]; then
    kill "${watchdog_child}" 2>/dev/null || true
  fi
  if [[ -n "${candidate_path}" ]]; then
    rm -f "${candidate_path}" 2>/dev/null || true
  fi
  cleanup_lock
  if [[ -n "${temporary_root}" ]]; then
    rm -rf "${temporary_root}" 2>/dev/null || true
  fi
  return "${status}"
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT TERM

validate_version() {
  local value="$1"
  local semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
  [[ "${value}" =~ ${semver} ]] || fail "version must be a v-prefixed semantic version"
  local without_build="${value%%+*}"
  if [[ "${without_build}" == *-* ]]; then
    local prerelease="${without_build#*-}"
    local identifiers=()
    IFS='.' read -r -a identifiers <<< "${prerelease}"
    local identifier
    for identifier in "${identifiers[@]}"; do
      if [[ "${identifier}" =~ ^[0-9]+$ && "${#identifier}" -gt 1 && "${identifier}" == 0* ]]; then
        fail "numeric prerelease identifiers must not contain leading zeroes"
      fi
    done
  fi
}

is_prerelease() {
  local without_build="${1%%+*}"
  [[ "${without_build}" == *-* ]]
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

normalize_os() {
  case "$1" in
    Darwin) printf 'darwin\n' ;;
    Linux) printf 'linux\n' ;;
    *) fail "operating system $1 is not supported" ;;
  esac
}

normalize_arch() {
  case "$1" in
    x86_64|amd64) printf 'amd64\n' ;;
    arm64|aarch64) printf 'arm64\n' ;;
    *) fail "architecture $1 is not supported" ;;
  esac
}

curl_common() {
  curl --fail --silent --show-error --location \
    --proto '=https' --proto-redir '=https' \
    --connect-timeout 10 --max-time 120 "$@"
}

resolve_latest() {
  local effective prefix selected
  effective="$(curl_common --output /dev/null --write-out '%{url_effective}' "${release_root}/latest")" || fail "could not resolve the latest Schooner release"
  prefix="${release_root}/tag/"
  [[ "${effective}" == "${prefix}"* ]] || fail "latest release redirected outside the Schooner release page"
  selected="${effective#${prefix}}"
  [[ -n "${selected}" && "${selected}" != */* && "${selected}" != *\?* && "${selected}" != *\#* ]] || fail "latest release returned an invalid tag"
  validate_version "${selected}"
  ! is_prerelease "${selected}" || fail "latest release unexpectedly resolved to a prerelease"
  printf '%s\n' "${selected}"
}

hash_file() {
  local path="$1" output
  if command -v sha256sum >/dev/null 2>&1; then
    output="$(sha256sum "${path}")" || return
  elif command -v shasum >/dev/null 2>&1; then
    output="$(shasum -a 256 "${path}")" || return
  else
    fail "sha256sum or shasum is required"
  fi
  output="${output%%[[:space:]]*}"
  printf '%s\n' "${output}" | tr '[:upper:]' '[:lower:]'
}

download() {
  local url="$1" destination="$2" maximum_bytes="$3"
  curl_common --max-filesize "${maximum_bytes}" --output "${destination}" "${url}" || fail "download failed: ${url}"
}

parse_manifest() {
  local path="$1" archive="$2" executable="$3"
  LC_ALL=C awk -v archive="${archive}" -v executable="${executable}" '
    function invalid(message) { print "install.sh: " message > "/dev/stderr"; exit 2 }
    NF == 0 { next }
    NF != 2 { invalid("checksum manifest contains a malformed entry") }
    {
      digest = tolower($1)
      name = $2
      sub(/^\*/, "", name)
      if (length(digest) != 64 || digest ~ /[^0-9a-f]/) invalid("checksum manifest contains an invalid digest")
      if (name !~ /^[0-9A-Za-z._+-]+$/ || name == "." || name == "..") invalid("checksum manifest contains an unsafe entry")
      if (seen[name]++) invalid("checksum manifest contains a duplicate entry")
      if (name == archive) archive_digest = digest
      if (name == executable) executable_digest = digest
    }
    END {
      if (!archive_digest) invalid("checksum manifest does not contain the selected archive")
      if (!executable_digest) invalid("checksum manifest does not contain the selected raw executable")
      print archive_digest
      print executable_digest
    }
  ' "${path}"
}

validate_archive() {
  local archive="$1" listing modes
  listing="$(LC_ALL=C tar -tzf "${archive}")" || fail "release archive could not be listed"
  [[ "${listing}" == $'LICENSE\nREADME.md\nschooner' ]] || fail "release archive has an invalid member set"
  modes="$(LC_ALL=C tar -tvzf "${archive}" | awk '{print $1}')" || fail "release archive metadata could not be listed"
  [[ "${modes}" == $'-rw-r--r--\n-rw-r--r--\n-rwxr-xr-x' ]] || fail "release archive has an invalid member type or mode"
}

json_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//$'\t'/\\t}"
  value="${value//$'\r'/\\r}"
  value="${value//$'\n'/\\n}"
  printf '%s' "${value}"
}

portable_stat() {
  local path="$1"
  if stat -f '%u %Lp' /dev/null >/dev/null 2>&1; then
    stat -f '%u %Lp' "${path}"
  else
    stat -c '%u %a' "${path}"
  fi
}

secure_receipt() {
  local path="$1" metadata uid mode group_digit world_digit
  [[ -f "${path}" && ! -L "${path}" ]] || return 1
  metadata="$(portable_stat "${path}")" || return 1
  uid="${metadata%% *}"
  mode="${metadata#* }"
  [[ "${uid}" == "$(id -u)" && "${mode}" =~ ^[0-7]{3,4}$ ]] || return 1
  mode=$((10#${mode}))
  group_digit=$(((mode / 10) % 10))
  world_digit=$((mode % 10))
  (( (group_digit & 2) == 0 && (world_digit & 2) == 0 ))
}

receipt_is_well_formed() {
  local receipt="$1" target="$2" escaped_target line_count line digest version kind asset_name asset_digest installed_at
  secure_receipt "${receipt}" || return 1
  [[ "$(wc -c < "${receipt}" | tr -d ' ')" -le 16384 ]] || return 1
  line_count="$(awk 'END {print NR}' "${receipt}")"
  [[ "${line_count}" == 11 ]] || return 1
  [[ "$(sed -n '1p' "${receipt}")" == "{" ]] || return 1
  [[ "$(sed -n '2p' "${receipt}")" == '  "schema_version": "1",' ]] || return 1
  [[ "$(sed -n '3p' "${receipt}")" == '  "installation_method": "direct",' ]] || return 1
  escaped_target="$(json_escape "${target}")"
  [[ "$(sed -n '4p' "${receipt}")" == "  \"executable_path\": \"${escaped_target}\"," ]] || return 1

  line="$(sed -n '5p' "${receipt}")"
  [[ "${line}" == '  "version": "'*'",' ]] || return 1
  version="${line#*\"version\": \"}"
  version="${version%\",}"
  validate_version_for_receipt "${version}" || return 1

  line="$(sed -n '6p' "${receipt}")"
  [[ "${line}" == '  "executable_sha256": "'*'",' ]] || return 1
  digest="${line#*\"executable_sha256\": \"}"
  digest="${digest%\",}"
  valid_digest "${digest}" || return 1

  line="$(sed -n '7p' "${receipt}")"
  kind="${line#*\"release_asset_kind\": \"}"
  kind="${kind%\",}"
  [[ "${line}" == '  "release_asset_kind": "'*'",' && ( "${kind}" == "archive" || "${kind}" == "raw" ) ]] || return 1

  line="$(sed -n '8p' "${receipt}")"
  asset_name="${line#*\"release_asset_name\": \"}"
  asset_name="${asset_name%\",}"
  [[ "${line}" == '  "release_asset_name": "'*'",' && "${asset_name}" =~ ^[0-9A-Za-z._+-]+$ ]] || return 1

  line="$(sed -n '9p' "${receipt}")"
  asset_digest="${line#*\"release_asset_sha256\": \"}"
  asset_digest="${asset_digest%\",}"
  [[ "${line}" == '  "release_asset_sha256": "'*'",' ]] && valid_digest "${asset_digest}" || return 1

  line="$(sed -n '10p' "${receipt}")"
  installed_at="${line#*\"installed_at\": \"}"
  installed_at="${installed_at%\"}"
  [[ "${line}" == '  "installed_at": "'*'"' && "${installed_at}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || return 1
  [[ "$(sed -n '11p' "${receipt}")" == "}" ]] || return 1
}

receipt_matches_target() {
  local receipt="$1" target="$2" line digest
  receipt_is_well_formed "${receipt}" "${target}" || return 1
  line="$(sed -n '6p' "${receipt}")"
  digest="${line#*\"executable_sha256\": \"}"
  digest="${digest%\",}"
  [[ "$(hash_file "${target}")" == "${digest}" ]]
}

validate_version_for_receipt() {
  local value="$1"
  local semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
  [[ "${value}" =~ ${semver} ]]
}

valid_digest() {
  [[ "$1" =~ ^[0-9a-f]{64}$ ]]
}

package_manager_target() {
  local target="$1" probe index
  [[ -L "${target}" ]] && return 0
  probe="$(dirname "${target}")"
  for index in 1 2 3 4 5; do
    if [[ -f "${probe}/INSTALL_RECEIPT.json" && "${probe}" == */schooner/* ]]; then
      return 0
    fi
    [[ "${probe}" != "/" ]] || break
    probe="$(dirname "${probe}")"
  done
  return 1
}

file_fingerprint() {
  local path="$1"
  if [[ -L "${path}" ]]; then
    printf 'symlink\n'
  elif [[ -f "${path}" ]]; then
    printf 'regular:%s\n' "$(hash_file "${path}")"
  elif [[ -e "${path}" ]]; then
    printf 'other\n'
  else
    printf 'absent\n'
  fi
}

read_lock_value() {
  local owner="$1" key="$2"
  sed -n "s/^${key}=//p" "${owner}"
}

acquire_lock() {
  local install_directory="$1" target="$2" owner host pid stale token owner_count
  lock_directory="${install_directory}/${lock_name}"
  host="$(hostname 2>/dev/null || uname -n)"
  [[ "${host}" =~ ^[0-9A-Za-z._-]+$ ]] || fail "local hostname is unsafe for installation locking"
  lock_token="$$.${RANDOM}.$(date -u '+%s')"
  if ! mkdir "${lock_directory}" 2>/dev/null; then
    [[ -d "${lock_directory}" && ! -L "${lock_directory}" ]] || fail "installation lock is invalid: ${lock_directory}"
    owner="${lock_directory}/owner"
    [[ -f "${owner}" && ! -L "${owner}" ]] || fail "installation is locked by an ambiguous previous operation: ${lock_directory}"
    owner_count="$(find "${lock_directory}" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')"
    [[ "${owner_count}" == 1 ]] || fail "installation is locked by an ambiguous previous operation: ${lock_directory}"
    [[ "$(sed -n '1p' "${owner}")" == "schema_version=1" ]] || fail "installation is locked by an unsupported previous operation"
    [[ "$(awk 'END {print NR}' "${owner}")" == 5 ]] || fail "installation is locked by an ambiguous previous operation"
    local owner_host owner_pid owner_target owner_token
    owner_host="$(read_lock_value "${owner}" host)"
    owner_pid="$(read_lock_value "${owner}" pid)"
    owner_target="$(read_lock_value "${owner}" target)"
    owner_token="$(read_lock_value "${owner}" token)"
    [[ "${owner_host}" == "${host}" && "${owner_pid}" =~ ^[1-9][0-9]*$ && "${owner_target}" == "${target}" && "${owner_token}" =~ ^[0-9A-Za-z._-]+$ ]] || fail "installation is locked by an ambiguous or foreign-host operation: ${lock_directory}"
    if kill -0 "${owner_pid}" 2>/dev/null; then
      fail "another Schooner installation is active for ${target}"
    fi
    stale="${lock_directory}.stale.${lock_token}"
    mv "${lock_directory}" "${stale}" 2>/dev/null || fail "the stale installation lock changed; retry"
    rm -f "${stale}/owner"
    rmdir "${stale}" || fail "could not retire the stale installation lock"
    mkdir "${lock_directory}" || fail "could not acquire the installation lock"
  fi
  owner="${lock_directory}/owner"
  token="${lock_token}"
  {
    printf 'schema_version=1\n'
    printf 'host=%s\n' "${host}"
    printf 'pid=%s\n' "$$"
    printf 'target=%s\n' "${target}"
    printf 'token=%s\n' "${token}"
  } > "${owner}"
  chmod 0600 "${owner}"
  lock_held=true
}

verify_macos_signature() {
  local candidate="$1"
  [[ -x /usr/bin/codesign ]] || fail "/usr/bin/codesign is required on macOS"
  local requirement
  requirement="anchor apple generic and identifier \"${apple_identifier}\" and certificate leaf[subject.OU] = \"${apple_team_id}\" and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists"
  /usr/bin/codesign --verify --strict --verbose=2 -R="${requirement}" "${candidate}" || fail "candidate has an invalid Schooner Developer ID signature"
}

verify_candidate_identity() {
  local candidate="$1" expected_version="$2" expected_os="$3" expected_arch="$4"
  local output="${temporary_root}/version.json" errors="${temporary_root}/version.stderr" child watchdog status size
  (
    ulimit -f 128
    exec "${candidate}" version --output json
  ) > "${output}" 2> "${errors}" &
  child=$!
  candidate_child="${child}"
  (sleep 10; kill -TERM "${child}" 2>/dev/null || true; sleep 2; kill -KILL "${child}" 2>/dev/null || true) &
  watchdog=$!
  watchdog_child="${watchdog}"
  if wait "${child}"; then status=0; else status=$?; fi
  candidate_child=""
  kill "${watchdog}" 2>/dev/null || true
  wait "${watchdog}" 2>/dev/null || true
  watchdog_child=""
  (( status == 0 )) || fail "candidate version check failed"
  size="$(wc -c < "${output}" | tr -d ' ')"
  (( size > 0 && size <= 65536 )) || fail "candidate version output is invalid"
  [[ "$(awk 'END {print NR}' "${output}")" == 1 ]] || fail "candidate version output must be one JSON line"
  grep -Fq '"schema_version":"1"' "${output}" || fail "candidate version schema is invalid"
  grep -Fq "\"version\":\"${expected_version}\"" "${output}" || fail "candidate version does not match ${expected_version}"
  grep -Fq "\"os\":\"${expected_os}\"" "${output}" || fail "candidate operating system does not match ${expected_os}"
  grep -Fq "\"arch\":\"${expected_arch}\"" "${output}" || fail "candidate architecture does not match ${expected_arch}"
}

write_receipt() {
  local receipt="$1" target="$2" version="$3" executable_sha="$4" asset_name="$5" asset_sha="$6"
  local temporary installed_at escaped_target
  temporary="$(mktemp "$(dirname "${receipt}")/.schooner-receipt.tmp.XXXXXX")" || return 1
  installed_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  escaped_target="$(json_escape "${target}")"
  if ! {
    printf '{\n'
    printf '  "schema_version": "1",\n'
    printf '  "installation_method": "direct",\n'
    printf '  "executable_path": "%s",\n' "${escaped_target}"
    printf '  "version": "%s",\n' "${version}"
    printf '  "executable_sha256": "%s",\n' "${executable_sha}"
    printf '  "release_asset_kind": "archive",\n'
    printf '  "release_asset_name": "%s",\n' "${asset_name}"
    printf '  "release_asset_sha256": "%s",\n' "${asset_sha}"
    printf '  "installed_at": "%s"\n' "${installed_at}"
    printf '}\n'
  } > "${temporary}"; then
    rm -f "${temporary}"
    return 1
  fi
  chmod 0600 "${temporary}" || { rm -f "${temporary}"; return 1; }
  mv -f "${temporary}" "${receipt}" || { rm -f "${temporary}"; return 1; }
}

path_contains_directory() {
  local wanted="$1" entry physical
  local entries=()
  IFS=: read -r -a entries <<< "${PATH:-}"
  for entry in "${entries[@]}"; do
    [[ -n "${entry}" ]] || entry="."
    if [[ -d "${entry}" ]]; then
      physical="$(cd "${entry}" 2>/dev/null && pwd -P)" || physical=""
      [[ "${physical}" == "${wanted}" ]] && return 0
    fi
  done
  return 1
}

requested_version=""
install_dir=""
while (( $# > 0 )); do
  case "$1" in
    --version)
      (( $# >= 2 )) || fail "--version requires a value"
      requested_version="$2"
      shift 2
      ;;
    --install-dir)
      (( $# >= 2 )) || fail "--install-dir requires a directory"
      install_dir="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *) fail "unknown argument: $1" ;;
  esac
done

require_command bash
require_command curl
require_command tar
require_command awk
require_command sed
require_command stat

platform_os="$(normalize_os "$(uname -s)")"
platform_arch="$(normalize_arch "$(uname -m)")"

if [[ -n "${requested_version}" ]]; then
  validate_version "${requested_version}"
  selected_version="${requested_version}"
else
  selected_version="$(resolve_latest)"
fi

if [[ -z "${install_dir}" ]]; then
  [[ -n "${HOME:-}" && "${HOME}" == /* ]] || fail "HOME must be an absolute path"
  install_dir="${HOME}/.local/bin"
fi
[[ "${install_dir}" != *$'\n'* && "${install_dir}" != *$'\r'* ]] || fail "install directory contains an unsafe newline"
mkdir -p "${install_dir}" || fail "could not create install directory: ${install_dir}"
[[ -d "${install_dir}" && ! -L "${install_dir}" ]] || fail "install directory must be a regular directory, not a symlink"
install_dir="$(cd "${install_dir}" && pwd -P)"

temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/schooner-install.XXXXXX")" || fail "could not create temporary directory"
temporary_root="$(cd "${temporary_root}" && pwd -P)"
manifest_path="${temporary_root}/SHA256SUMS"
archive_name="schooner_${selected_version}_${platform_os}_${platform_arch}.tar.gz"
executable_name="schooner_${selected_version}_${platform_os}_${platform_arch}"
archive_path="${temporary_root}/${archive_name}"
asset_root="${release_root}/download/${selected_version}"

download "${asset_root}/SHA256SUMS" "${manifest_path}" "${max_manifest_bytes}"
manifest_size="$(wc -c < "${manifest_path}" | tr -d ' ')"
(( manifest_size <= max_manifest_bytes )) || fail "checksum manifest exceeds 1 MiB"
digests="$(parse_manifest "${manifest_path}" "${archive_name}" "${executable_name}")" || fail "checksum manifest is invalid"
archive_sha="$(printf '%s\n' "${digests}" | sed -n '1p')"
executable_sha="$(printf '%s\n' "${digests}" | sed -n '2p')"
valid_digest "${archive_sha}" && valid_digest "${executable_sha}" || fail "checksum manifest did not produce valid selected digests"

download "${asset_root}/${archive_name}" "${archive_path}" "${max_archive_bytes}"
archive_size="$(wc -c < "${archive_path}" | tr -d ' ')"
(( archive_size > 0 && archive_size <= max_archive_bytes )) || fail "release archive is empty or exceeds 512 MiB"
[[ "$(hash_file "${archive_path}")" == "${archive_sha}" ]] || fail "archive checksum mismatch for ${archive_name}"
validate_archive "${archive_path}"

target="${install_dir}/schooner"
receipt="${install_dir}/${receipt_name}"
acquire_lock "${install_dir}" "${target}"
baseline_target="$(file_fingerprint "${target}")"
baseline_receipt="$(file_fingerprint "${receipt}")"

authorized=false
adopt_only=false
if [[ "${baseline_target}" == "absent" ]]; then
  authorized=true
elif package_manager_target "${target}"; then
  fail "existing target appears package-managed or is a symlink; its owner must update it: ${target}"
elif [[ "${baseline_target}" != regular:* ]]; then
  fail "existing target is not a regular file and will not be replaced: ${target}"
elif receipt_matches_target "${receipt}" "${target}"; then
  authorized=true
elif [[ "${baseline_target}" == "regular:${executable_sha}" ]] && receipt_is_well_formed "${receipt}" "${target}"; then
  authorized=true
  adopt_only=true
elif [[ "${baseline_receipt}" == "absent" && "${baseline_target}" == "regular:${executable_sha}" ]]; then
  authorized=true
  adopt_only=true
else
  fail "existing Schooner installation is source-built, manual, or has a stale receipt; refusing to replace ${target}"
fi

[[ "${authorized}" == true ]] || fail "installation ownership could not be established"
candidate_path="$(mktemp "${install_dir}/.schooner.tmp.XXXXXX")" || fail "could not create candidate in the install directory"
(ulimit -f 262144; LC_ALL=C tar -xOzf "${archive_path}" schooner) > "${candidate_path}" || fail "could not read the executable from the release archive"
candidate_size="$(wc -c < "${candidate_path}" | tr -d ' ')"
(( candidate_size > 0 && candidate_size <= max_executable_bytes )) || fail "candidate executable is empty or exceeds 256 MiB"
chmod 0755 "${candidate_path}" || fail "could not make the candidate executable"
[[ "$(hash_file "${candidate_path}")" == "${executable_sha}" ]] || fail "archived executable does not match the raw release asset"
if [[ "${platform_os}" == "darwin" ]]; then
  verify_macos_signature "${candidate_path}"
fi
verify_candidate_identity "${candidate_path}" "${selected_version}" "${platform_os}" "${platform_arch}"

[[ "$(file_fingerprint "${target}")" == "${baseline_target}" ]] || fail "installation target changed during verification; retry"
[[ "$(file_fingerprint "${receipt}")" == "${baseline_receipt}" ]] || fail "installation receipt changed during verification; retry"

if [[ "${adopt_only}" != true ]]; then
  mv -f "${candidate_path}" "${target}" || fail "could not atomically install Schooner"
  candidate_path=""
fi
if ! write_receipt "${receipt}" "${target}" "${selected_version}" "${executable_sha}" "${archive_name}" "${archive_sha}"; then
  printf 'install.sh: Schooner %s is valid at %s, but its ownership receipt could not be written.\n' "${selected_version}" "${target}" >&2
  printf 'install.sh: repair it with: curl -fsSL https://raw.githubusercontent.com/thewelshrich/schooner/main/scripts/install.sh | bash -s -- --version %q --install-dir %q\n' "${selected_version}" "${install_dir}" >&2
  exit 1
fi

printf 'Installed Schooner %s to %s\n' "${selected_version}" "${target}"
if ! path_contains_directory "${install_dir}"; then
  printf '\n%s is not on PATH. Add this line to your shell profile, then open a new shell:\n\n' "${install_dir}"
  printf '  export PATH=%q:"$PATH"\n' "${install_dir}"
fi
