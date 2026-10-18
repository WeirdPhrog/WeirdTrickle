import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

from scripts.release_version import release_notes, validate_version


class ReleaseVersionTests(unittest.TestCase):
    def test_tag_validation_requires_release_notes_before_building(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "scripts").mkdir()
            script = root / "scripts/release_version.py"
            shutil.copyfile(Path(__file__).resolve().parents[1] / "scripts/release_version.py", script)
            (root / "VERSION").write_text("1.0.2\n")
            (root / "CHANGELOG.md").write_text("## [1.0.1]\n- Previous release\n")
            for ref, success in [("refs/heads/develop", True), ("refs/tags/v1.0.2", False)]:
                with self.subTest(ref=ref):
                    result = subprocess.run([sys.executable, str(script)], env=dict(os.environ, GITHUB_REF=ref), capture_output=True, text=True)
                    self.assertEqual(result.returncode == 0, success, result.stderr)
            (root / "CHANGELOG.md").write_text("## [1.0.2]\n- Current release\n## [1.0.1]\n- Previous release\n")
            result = subprocess.run([sys.executable, str(script), "--notes"], env=dict(os.environ, GITHUB_REF="refs/tags/v1.0.2", PYTHONUTF8="1"), capture_output=True, text=True, encoding="utf-8")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("- Current release", result.stdout)
            self.assertNotIn("Previous release", result.stdout)

    def test_release_notes_select_exact_version_and_stop_at_next_section(self):
        changelog = """# Changes
## [Unreleased]
- Future changes
## [1.0.10] — 2026-10-20
- Later release
## [1.0.1] — 2026-10-18
### Исправлено

- Новые домены подписок ждут маршрутизации.
## Calendar releases
- Older changes
"""
        self.assertEqual(release_notes(changelog, "1.0.1"), "### Исправлено\n\n- Новые домены подписок ждут маршрутизации.")

    def test_last_release_section_and_windows_line_endings(self):
        self.assertEqual(release_notes("## [1.0.1]\r\n\r\n- Fix\r\n", "1.0.1"), "- Fix")

    def test_missing_empty_and_duplicate_release_notes_fail(self):
        for changelog in [
            "## [1.0.10]\n- Different release\n",
            "## [1.0.1]\n\n## [1.0.0]\n- Older release\n",
            "## [1.0.1]\n### Fixed\n",
            "## [1.0.1]\n- First\n## [1.0.1] — 2026-10-18\n- Duplicate\n",
        ]:
            with self.subTest(changelog=changelog), self.assertRaises(ValueError):
                release_notes(changelog, "1.0.1")

    def test_stable_version_and_matching_tag(self):
        for version in ["1.0.0", "1.0.1", "1.10.0", "2.0.0"]:
            with self.subTest(version=version):
                self.assertEqual(validate_version(version, f"refs/tags/v{version}"), version)

    def test_branch_builds_use_same_version(self):
        self.assertEqual(validate_version("1.0.0", "refs/heads/develop"), "1.0.0")

    def test_invalid_versions_and_mismatched_tags_fail(self):
        for version in ["2026.10.16.20", "01.0.0", "1.0", "v1.0.0", "1.0.0\nextra", "../1.0.0"]:
            with self.subTest(version=version), self.assertRaises(ValueError):
                validate_version(version)
        for ref in ["refs/tags/v1.0.1", "refs/tags/1.0.0", "refs/tags/v2026.10.16.20"]:
            with self.subTest(ref=ref), self.assertRaises(ValueError):
                validate_version("1.0.0", ref)
