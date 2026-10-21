"""Build and verify four portable archives locally; never contact a release service."""

import argparse
import hashlib
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parent.parent
TARGETS = (("linux", "amd64"), ("linux", "arm64"),
           ("darwin", "amd64"), ("darwin", "arm64"))


def release_notes(tag, changelog):
    """Require a stable version tag and its nonempty, exact changelog section."""
    if not re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", tag):
        raise ValueError("expected a stable vX.Y.Z tag")
    match = re.search(r"^## " + re.escape(tag[1:]) + r"\n(.*?)(?=^## |\Z)",
                      changelog, re.M | re.S)
    if not match or not match[1].strip():
        raise ValueError(f"CHANGELOG.md needs a nonempty '## {tag[1:]}' section")
    return match[1].strip() + "\n"


def package(version, output, notes):
    """Build CGO-free targets and verify archive contents and SHA-256 manifests."""
    output.mkdir(parents=True, exist_ok=False)
    checksums = []
    with tempfile.TemporaryDirectory(prefix="in-build-") as temp:
        binary = Path(temp) / "in"
        for goos, goarch in TARGETS:
            env = dict(os.environ, GOOS=goos, GOARCH=goarch, CGO_ENABLED="0")
            subprocess.run(["go", "build", "-trimpath", "-ldflags", f"-X main.version={version}",
                            "-o", str(binary), "./cmd/in"],
                           cwd=ROOT, env=env, check=True)
            archive = output / f"in-{version}-{goos}-{goarch}.tar.gz"
            with tarfile.open(archive, "w:gz") as bundle:
                bundle.add(binary, arcname="in")
                bundle.add(ROOT / "LICENSE", arcname="LICENSE")
                bundle.add(ROOT / "README.md", arcname="README.md")
                bundle.add(ROOT / "install.sh", arcname="install.sh")
                bundle.add(ROOT / "docs", arcname="docs")
                for name in ("CONTRIBUTING.md", "AGENTS.md", "SECURITY.md", "CHANGELOG.md"):
                    bundle.add(ROOT / name, arcname=name)
            with tarfile.open(archive) as bundle:
                member = bundle.getmember("in")
                if not member.isfile() or not member.mode & 0o111:
                    raise ValueError(f"{archive.name}: missing executable")
                if bundle.extractfile(member).read() != binary.read_bytes():
                    raise ValueError(f"{archive.name}: binary content mismatch")
                bundle.getmember("LICENSE")
            digest = hashlib.sha256(archive.read_bytes()).hexdigest()
            checksums.append(f"{digest}  {archive.name}\n")
            print(f"Built and inspected {archive.name}", flush=True)
    manifest = output / "checksums.txt"
    manifest.write_text("".join(checksums))
    for line in manifest.read_text().splitlines():
        expected, name = line.split("  ", 1)
        if hashlib.sha256((output / name).read_bytes()).hexdigest() != expected:
            raise ValueError(f"checksum mismatch: {name}")
    (output / "release-notes.md").write_text(notes)


def main():
    """Use temporary output for checks; refuse to overwrite any dist directory."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("check", "dist"))
    parser.add_argument("tag", nargs="?")
    args = parser.parse_args()
    if args.command == "dist" and not args.tag:
        parser.error("dist requires vX.Y.Z")
    notes = release_notes(args.tag, (ROOT / "CHANGELOG.md").read_text()) if args.tag else "Unreleased verification build.\n"
    if args.command == "check":
        with tempfile.TemporaryDirectory(prefix="in-release-check-") as temp:
            package(args.tag or "dev", Path(temp) / "archives", notes)
        print("Four-target release check passed; temporary archives removed.")
    else:
        output = ROOT / "dist" / args.tag
        package(args.tag, output, notes)
        print(f"Local release files: {output}")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, FileExistsError) as error:
        raise SystemExit(str(error)) from error
