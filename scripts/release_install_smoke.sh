#!/usr/bin/env bash

set -euo pipefail

if (( $# != 2 )); then
  printf 'Usage: scripts/release_install_smoke.sh VERSION DIST\n' >&2
  exit 2
fi

version="$1"
dist="$(cd "$2" && pwd -P)"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
test_tmp="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/schooner-install-smoke.XXXXXX")"
trap 'rm -rf -- "${test_tmp}"' EXIT
mkdir -p "${test_tmp}/bin" "${test_tmp}/install"

cat > "${test_tmp}/bin/curl" <<'CURL'
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
    --fail|--silent|--show-error|--location|-f|-s|-S|-L)
      shift
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done
if [[ "${url}" == "https://github.com/thewelshrich/schooner/releases/latest" ]]; then
  [[ "${write_out}" == *url_effective* ]] || exit 64
  printf 'https://github.com/thewelshrich/schooner/releases/tag/%s' "${SCHOONER_SMOKE_VERSION}"
  exit 0
fi
prefix="https://github.com/thewelshrich/schooner/releases/download/${SCHOONER_SMOKE_VERSION}/"
[[ "${url}" == "${prefix}"* && -n "${output}" ]] || exit 64
cp "${SCHOONER_SMOKE_DIST}/${url#${prefix}}" "${output}"
CURL
chmod 0755 "${test_tmp}/bin/curl"

PATH="${test_tmp}/bin:${PATH}" \
SCHOONER_SMOKE_DIST="${dist}" \
SCHOONER_SMOKE_VERSION="${version}" \
  bash "${repo_root}/scripts/install.sh" --yes --install-dir "${test_tmp}/install"

version_json="$("${test_tmp}/install/schooner" version --output json)"
VERSION_JSON="${version_json}" EXPECTED_VERSION="${version}" python3 - <<'PY'
import json
import os

value = json.loads(os.environ["VERSION_JSON"])
assert value["schema_version"] == "1"
assert value["version"] == os.environ["EXPECTED_VERSION"]
PY

test -f "${test_tmp}/install/.schooner-install-receipt.json"
PATH="${test_tmp}/bin:${PATH}" \
SCHOONER_SMOKE_DIST="${dist}" \
SCHOONER_SMOKE_VERSION="${version}" \
  bash "${repo_root}/scripts/install.sh" --yes --version "${version}" --install-dir "${test_tmp}/install"

printf 'release_install_smoke: %s installed and reverified\n' "${version}"
