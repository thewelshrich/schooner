#!/usr/bin/env python3
"""Build and verify Schooner's deterministic human-facing release archives."""

from __future__ import annotations

import argparse
import datetime as dt
import gzip
import hashlib
import io
import os
from pathlib import Path
import re
import sys
import tarfile
import tempfile


PLATFORMS = (
    ("darwin", "amd64"),
    ("darwin", "arm64"),
    ("linux", "amd64"),
    ("linux", "arm64"),
)
VERSION_RE = re.compile(
    r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
MEMBERS = (
    ("LICENSE", 0o644),
    ("README.md", 0o644),
    ("schooner", 0o755),
)


def fail(message: str) -> "NoReturn":
    raise SystemExit(f"package_release: {message}")


def parse_epoch(value: str) -> int:
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        fail("--built-at must be an RFC 3339 timestamp")
    if parsed.tzinfo is None:
        fail("--built-at must include a UTC offset")
    return int(parsed.timestamp())


def sha256_bytes(contents: bytes) -> str:
    return hashlib.sha256(contents).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def require_regular(path: Path) -> bytes:
    if path.is_symlink() or not path.is_file():
        fail(f"required regular file is missing: {path}")
    contents = path.read_bytes()
    if not contents:
        fail(f"required regular file is empty: {path}")
    return contents


def write_archive(path: Path, epoch: int, values: dict[str, bytes]) -> None:
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "wb") as raw:
            with gzip.GzipFile(filename="", mode="wb", fileobj=raw, compresslevel=9, mtime=0) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as archive:
                    for name, mode in MEMBERS:
                        contents = values[name]
                        member = tarfile.TarInfo(name)
                        member.size = len(contents)
                        member.mode = mode
                        member.mtime = epoch
                        member.uid = 0
                        member.gid = 0
                        member.uname = ""
                        member.gname = ""
                        archive.addfile(member, io.BytesIO(contents))
            raw.flush()
            os.fsync(raw.fileno())
        os.chmod(temporary, 0o644)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def verify_archive(path: Path, epoch: int, values: dict[str, bytes]) -> None:
    header = path.read_bytes()[:10]
    if len(header) != 10 or header[:2] != b"\x1f\x8b" or header[3] & 0x08 or header[4:8] != b"\0\0\0\0":
        fail(f"archive has a nondeterministic gzip header: {path.name}")
    with tarfile.open(path, mode="r:gz") as archive:
        members = archive.getmembers()
        expected_names = [name for name, _ in MEMBERS]
        if [member.name for member in members] != expected_names:
            fail(f"archive member set is invalid: {path.name}")
        for member, (name, mode) in zip(members, MEMBERS):
            if not member.isreg() or member.mode != mode:
                fail(f"archive member type or mode is invalid: {path.name}:{name}")
            if member.uid != 0 or member.gid != 0 or member.uname or member.gname or member.mtime != epoch:
                fail(f"archive member metadata is invalid: {path.name}:{name}")
            extracted = archive.extractfile(member)
            if extracted is None or extracted.read() != values[name]:
                fail(f"archive member contents are invalid: {path.name}:{name}")


def write_manifest(path: Path, assets: list[Path]) -> None:
    contents = "".join(f"{sha256_file(asset)}  {asset.name}\n" for asset in sorted(assets, key=lambda item: item.name))
    descriptor, temporary_name = tempfile.mkstemp(prefix=".SHA256SUMS.", dir=path.parent)
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


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", required=True)
    parser.add_argument("--built-at", required=True)
    parser.add_argument("--dist", required=True, type=Path)
    parser.add_argument("--source-root", default=Path.cwd(), type=Path)
    args = parser.parse_args()

    if not VERSION_RE.fullmatch(args.version):
        fail("--version must be a v-prefixed semantic version")
    epoch = parse_epoch(args.built_at)
    dist = args.dist.resolve()
    source = args.source_root.resolve()
    if not dist.is_dir():
        fail(f"bundle directory does not exist: {dist}")

    raw = [dist / f"schooner_{args.version}_{os_name}_{arch}" for os_name, arch in PLATFORMS]
    archives = [Path(f"{item}.tar.gz") for item in raw]
    allowed = {item.name for item in raw + archives}
    unexpected = sorted(item.name for item in dist.glob("schooner_*") if item.name not in allowed)
    if unexpected:
        fail(f"bundle contains unexpected Schooner assets: {', '.join(unexpected)}")

    license_contents = require_regular(source / "LICENSE")
    readme_contents = require_regular(source / "README.md")
    for executable, archive in zip(raw, archives):
        executable_contents = require_regular(executable)
        values = {"LICENSE": license_contents, "README.md": readme_contents, "schooner": executable_contents}
        write_archive(archive, epoch, values)
        verify_archive(archive, epoch, values)
        with tarfile.open(archive, mode="r:gz") as bundle:
            bundled = bundle.extractfile("schooner")
            if bundled is None or sha256_bytes(bundled.read()) != sha256_file(executable):
                fail(f"archived executable differs from raw asset: {archive.name}")

    assets = raw + archives
    write_manifest(dist / "SHA256SUMS", assets)
    if len((dist / "SHA256SUMS").read_text(encoding="utf-8").splitlines()) != 8:
        fail("checksum manifest must contain exactly eight assets")


if __name__ == "__main__":
    main()
