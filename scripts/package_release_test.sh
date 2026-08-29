#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
test_tmp="$(mktemp -d "${TMPDIR:-/tmp}/schooner-package-release-test.XXXXXX")"
trap 'rm -rf -- "${test_tmp}"' EXIT

fail() {
  printf 'package_release_test: %s\n' "$*" >&2
  exit 1
}

make_fixture() {
  local root="$1"
  mkdir -p "${root}/dist"
  printf 'Apache test license\n' > "${root}/LICENSE"
  printf '# Schooner test\n' > "${root}/README.md"
  local os arch
  for os in darwin linux; do
    for arch in amd64 arm64; do
      printf '#!/usr/bin/env bash\nprintf "%%s\\n" %q\n' "${os}/${arch}" > "${root}/dist/schooner_v1.2.3_${os}_${arch}"
    done
  done
}

fixture="${test_tmp}/fixture"
make_fixture "${fixture}"

package() {
  python3 "${repo_root}/scripts/package_release.py" \
    --version v1.2.3 \
    --built-at 2026-08-29T08:00:00Z \
    --source-root "${fixture}" \
    --dist "${fixture}/dist"
}

archive_digests() {
  FIXTURE="${fixture}" python3 - <<'PY'
import hashlib
import os
from pathlib import Path

for path in sorted((Path(os.environ["FIXTURE"]) / "dist").glob("*.tar.gz")):
    print(hashlib.sha256(path.read_bytes()).hexdigest(), path.name)
PY
}

package
archive_digests > "${test_tmp}/first.sha256"
package
archive_digests > "${test_tmp}/second.sha256"
cmp "${test_tmp}/first.sha256" "${test_tmp}/second.sha256" || fail "repeated packaging was not deterministic"

FIXTURE="${fixture}" python3 - <<'PY'
import hashlib
import os
from pathlib import Path
import tarfile

root = Path(os.environ["FIXTURE"])
dist = root / "dist"
archives = sorted(dist.glob("*.tar.gz"))
assert len(archives) == 4, archives
for path in archives:
    with tarfile.open(path, "r:gz") as archive:
        members = archive.getmembers()
        assert [member.name for member in members] == ["LICENSE", "README.md", "schooner"]
        assert [member.mode for member in members] == [0o644, 0o644, 0o755]
        assert all(member.isreg() and member.uid == member.gid == 0 for member in members)
        raw = dist / path.name.removesuffix(".tar.gz")
        bundled = archive.extractfile("schooner").read()
        assert hashlib.sha256(bundled).digest() == hashlib.sha256(raw.read_bytes()).digest()

lines = (dist / "SHA256SUMS").read_text().splitlines()
assert len(lines) == 8
assert lines == sorted(lines, key=lambda line: line.split(maxsplit=1)[1])
PY

missing="${test_tmp}/missing"
make_fixture "${missing}"
rm "${missing}/dist/schooner_v1.2.3_linux_arm64"
if python3 "${repo_root}/scripts/package_release.py" --version v1.2.3 --built-at 2026-08-29T08:00:00Z --source-root "${missing}" --dist "${missing}/dist" >/dev/null 2>&1; then
  fail "packaging accepted a missing raw executable"
fi

unexpected="${test_tmp}/unexpected"
make_fixture "${unexpected}"
printf 'unexpected\n' > "${unexpected}/dist/schooner_v1.2.3_windows_amd64"
if python3 "${repo_root}/scripts/package_release.py" --version v1.2.3 --built-at 2026-08-29T08:00:00Z --source-root "${unexpected}" --dist "${unexpected}/dist" >/dev/null 2>&1; then
  fail "packaging accepted an unexpected Schooner asset"
fi

printf 'package_release_test: all tests passed\n'
