#!/usr/bin/env bash

set -euo pipefail
umask 077

if (( $# != 1 )); then
  printf 'Usage: scripts/public_release_smoke.sh VERSION\n' >&2
  exit 2
fi

version="$1"
if [[ ! "${version}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  printf 'public_release_smoke: version must be a stable v-prefixed semantic version\n' >&2
  exit 2
fi

case "$(uname -s)" in
  Darwin) platform_os=darwin ;;
  Linux) platform_os=linux ;;
  *) printf 'public_release_smoke: unsupported operating system\n' >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) platform_arch=amd64 ;;
  arm64|aarch64) platform_arch=arm64 ;;
  *) printf 'public_release_smoke: unsupported architecture\n' >&2; exit 1 ;;
esac

test_tmp="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/schooner-public-smoke.XXXXXX")"
trap 'rm -rf -- "${test_tmp}"' EXIT
installer="${test_tmp}/install.sh"
install_dir="${test_tmp}/install"

curl -fsSL --proto '=https' --proto-redir '=https' \
  --connect-timeout 10 --max-time 120 \
  --output "${installer}" \
  https://raw.githubusercontent.com/thewelshrich/schooner/main/scripts/install.sh
bash -n "${installer}"
bash "${installer}" --version "${version}" --install-dir "${install_dir}"
bash "${installer}" --install-dir "${install_dir}"

version_json="$("${install_dir}/schooner" version --output json)"
VERSION_JSON="${version_json}" EXPECTED_VERSION="${version}" EXPECTED_OS="${platform_os}" EXPECTED_ARCH="${platform_arch}" python3 - <<'PY'
import json
import os

document = json.loads(os.environ["VERSION_JSON"])
assert document["schema_version"] == "1"
assert document["version"] == os.environ["EXPECTED_VERSION"]
assert document["os"] == os.environ["EXPECTED_OS"]
assert document["arch"] == os.environ["EXPECTED_ARCH"]
PY

receipt="${install_dir}/.schooner-install-receipt.json"
grep -Fq '  "release_asset_kind": "archive",' "${receipt}"
grep -Fq "  \"version\": \"${version}\"," "${receipt}"

if [[ "${platform_os}" == darwin ]]; then
  asset="schooner_${version}_${platform_os}_${platform_arch}.tar.gz"
  release_root="https://github.com/thewelshrich/schooner/releases/download/${version}"
  manifest="${test_tmp}/SHA256SUMS"
  archive="${test_tmp}/${asset}"
  candidate="${test_tmp}/quarantined-schooner"
  curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 120 \
    --output "${manifest}" "${release_root}/SHA256SUMS"
  curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 120 \
    --output "${archive}" "${release_root}/${asset}"
  expected_sha="$(awk -v asset="${asset}" '$2 == asset { count++; digest=$1 } END { if (count != 1) exit 2; print digest }' "${manifest}")"
  actual_sha="$(shasum -a 256 "${archive}" | awk '{print $1}')"
  [[ "${actual_sha}" == "${expected_sha}" ]] || { printf 'public_release_smoke: archive checksum mismatch\n' >&2; exit 1; }
  [[ "$(tar -tzf "${archive}")" == $'LICENSE\nREADME.md\nschooner' ]] || { printf 'public_release_smoke: archive member set is invalid\n' >&2; exit 1; }
  tar -xOzf "${archive}" schooner > "${candidate}"
  chmod 0755 "${candidate}"
  xattr -w com.apple.quarantine '0081;68b2c000;SchoonerReleaseSmoke;' "${candidate}"
  requirement='anchor apple generic and identifier "app.schooner.cli" and certificate leaf[subject.OU] = "LDCWNW7T7K" and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists'
  /usr/bin/codesign --verify --strict --verbose=4 -R="${requirement}" "${candidate}"
  set +e
  assessment="$(/usr/sbin/spctl --assess --type execute --verbose=4 "${candidate}" 2>&1)"
  assessment_status=$?
  set -e
  if (( assessment_status != 0 )) && [[ "${assessment}" != *"valid but does not seem to be an app"* ]]; then
    printf '%s\n' "${assessment}" >&2
    printf 'public_release_smoke: Gatekeeper returned an unexpected assessment\n' >&2
    exit 1
  fi
  quarantined_json="$("${candidate}" version --output json)"
  [[ "${quarantined_json}" == *"\"version\":\"${version}\""* ]] || { printf 'public_release_smoke: quarantined candidate did not execute\n' >&2; exit 1; }
fi

printf 'public_release_smoke: %s verified on %s/%s\n' "${version}" "${platform_os}" "${platform_arch}"
