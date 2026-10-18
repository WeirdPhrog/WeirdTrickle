"""Validate release tags and extract their notes from CHANGELOG.md."""
import argparse
import os
from pathlib import Path
import re


def validate_version(version, ref=""):
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", version):
        raise ValueError("VERSION must contain MAJOR.MINOR.PATCH without leading zeroes")
    if ref.startswith("refs/tags/") and ref != f"refs/tags/v{version}":
        raise ValueError(f"Release tag must match VERSION: v{version}")
    return version


def release_notes(changelog, version):
    validate_version(version)
    headings = list(re.finditer(r"^## .+$", changelog, re.MULTILINE))
    pattern = rf"## \[{re.escape(version)}\](?: — [0-9]{{4}}-[0-9]{{2}}-[0-9]{{2}})?\s*"
    matches = [i for i, heading in enumerate(headings) if re.fullmatch(pattern, heading[0])]
    if len(matches) != 1:
        raise ValueError(f"CHANGELOG.md must contain exactly one section for {version}")
    index = matches[0]
    end = headings[index + 1].start() if index + 1 < len(headings) else len(changelog)
    notes = changelog[headings[index].end():end].strip()
    if not any(line.strip() and not line.startswith("#") for line in notes.splitlines()):
        raise ValueError(f"CHANGELOG.md section for {version} must not be empty")
    return notes


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--notes", action="store_true", help="print release notes instead of the version")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    ref = os.environ.get("GITHUB_REF", "")
    version = validate_version((root / "VERSION").read_text(encoding="utf-8").strip(), ref)
    if args.notes or ref.startswith("refs/tags/"):
        notes = release_notes((root / "CHANGELOG.md").read_text(encoding="utf-8"), version)
    if args.notes:
        print(notes)
        print("\nУстановка и обновление: [README](https://github.com/WeirdPhrog/WeirdTrickle#установка-из-этого-форка-keenetic--netcraze--entware)")
    else:
        print(version)
