#!/usr/bin/env python3
"""Render Schooner's Homebrew formula from one exact release manifest."""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import re
import sys
import tempfile


VERSION_RE = re.compile(r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
DIGEST_RE = re.compile(r"^[0-9a-f]{64}$")
FORMULA_RELEASE_RE = re.compile(
    r"/releases/download/(v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))/"
)
PLATFORMS = (
    ("darwin", "amd64"),
    ("darwin", "arm64"),
    ("linux", "amd64"),
    ("linux", "arm64"),
)


def fail(message: str) -> None:
    raise SystemExit(f"render_homebrew_formula: {message}")


def version_tuple(value: str) -> tuple[int, int, int]:
    match = VERSION_RE.fullmatch(value)
    if match is None:
        fail("--version must be a stable v-prefixed semantic version")
    return tuple(int(part) for part in match.groups())


def parse_manifest(path: Path, version: str) -> dict[str, str]:
    if path.is_symlink() or not path.is_file():
        fail(f"manifest is not a regular file: {path}")
    if path.stat().st_size > 1024 * 1024:
        fail("manifest exceeds 1 MiB")

    values: dict[str, str] = {}
    for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
        fields = line.split()
        if len(fields) != 2:
            fail(f"manifest line {number} is malformed")
        digest, name = fields
        name = name.removeprefix("*")
        if not DIGEST_RE.fullmatch(digest):
            fail(f"manifest line {number} has an invalid digest")
        if not re.fullmatch(r"[0-9A-Za-z._+-]+", name):
            fail(f"manifest line {number} has an unsafe asset name")
        if name in values:
            fail(f"manifest contains duplicate asset: {name}")
        values[name] = digest

    raw = [f"schooner_{version}_{os_name}_{arch}" for os_name, arch in PLATFORMS]
    archives = [f"{name}.tar.gz" for name in raw]
    expected = set(raw + archives)
    actual = set(values)
    if actual != expected:
        missing = sorted(expected - actual)
        unexpected = sorted(actual - expected)
        fail(f"manifest asset set differs; missing={missing}, unexpected={unexpected}")
    return values


def render(version: str, values: dict[str, str]) -> str:
    def stanza(os_name: str, arch: str) -> str:
        name = f"schooner_{version}_{os_name}_{arch}.tar.gz"
        url = f"https://github.com/thewelshrich/schooner/releases/download/{version}/{name}"
        return f'      url "{url}"\n      sha256 "{values[name]}"'

    return f'''# typed: false
# frozen_string_literal: true

class Schooner < Formula
  desc "Operate persistent, user-owned development machines"
  homepage "https://github.com/thewelshrich/schooner"
  license "Apache-2.0"

  on_macos do
    on_arm do
{stanza("darwin", "arm64")}
    end
    on_intel do
{stanza("darwin", "amd64")}
    end
  end

  on_linux do
    on_arm do
{stanza("linux", "arm64")}
    end
    on_intel do
{stanza("linux", "amd64")}
    end
  end

  def install
    bin.install "schooner"
    generate_completions_from_executable(bin/"schooner", "completion")
  end

  test do
    require "json"

    document = JSON.parse(shell_output("#{{bin}}/schooner version --output json"))
    assert_equal "1", document.fetch("schema_version")
    assert_equal "v#{{version}}", document.fetch("version")
    assert_equal OS.mac? ? "darwin" : "linux", document.fetch("os")
    assert_equal Hardware::CPU.arm? ? "arm64" : "amd64", document.fetch("arch")
  end
end
'''


def write_formula(path: Path, version: str, contents: str) -> str:
    requested = version_tuple(version)
    if path.exists():
        if path.is_symlink() or not path.is_file():
            fail(f"output is not a regular file: {path}")
        current_contents = path.read_text(encoding="utf-8")
        release_versions = set(FORMULA_RELEASE_RE.findall(current_contents))
        if len(release_versions) != 1:
            fail("existing formula has no single supported release version")
        current_version = release_versions.pop()
        current = version_tuple(current_version)
        if current > requested:
            fail("refusing to downgrade the existing formula")
        if current == requested:
            if current_contents != contents:
                legacy_version = f'  version "{version.removeprefix("v")}"\n'
                if (
                    current_contents.count(legacy_version) != 1
                    or current_contents.replace(legacy_version, "", 1) != contents
                ):
                    fail("existing formula differs for the same version")
            else:
                return "unchanged"

    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as handle:
            handle.write(contents)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, 0o644)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)
    return "updated"


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", required=True)
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()

    version_tuple(args.version)
    values = parse_manifest(args.manifest, args.version)
    result = write_formula(args.output, args.version, render(args.version, values))
    print(result)


if __name__ == "__main__":
    main()
