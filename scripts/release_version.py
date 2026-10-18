"""Validate the shared release version and require matching release tags."""
import os
from pathlib import Path
import re


def validate_version(version, ref=""):
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", version):
        raise ValueError("VERSION must contain MAJOR.MINOR.PATCH without leading zeroes")
    if ref.startswith("refs/tags/") and ref != f"refs/tags/v{version}":
        raise ValueError(f"Release tag must match VERSION: v{version}")
    return version


if __name__ == "__main__":
    version = (Path(__file__).resolve().parents[1] / "VERSION").read_text().strip()
    print(validate_version(version, os.environ.get("GITHUB_REF", "")))
