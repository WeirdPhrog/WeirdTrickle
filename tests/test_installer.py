"""Run the real installer in a temporary filesystem with fake network/opkg.

No router, privileges, network access or third-party Python modules are needed.
"""
import hashlib
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import time
import unittest


FAKE = r'''#!/usr/bin/env python3
import os, pathlib, shutil, sys
root = pathlib.Path(os.environ['FAKE_ROOT'])
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with (root / 'calls').open('a') as f:
    f.write(name + ' ' + ' '.join(args) + '\n')
if name == 'id':
    print('0')
elif name == 'curl':
    url = next(x for x in args if x.startswith('https://'))
    filename = url.rsplit('/', 1)[1]
    source = root / 'release' / filename
    if not source.exists(): sys.exit(22)
    shutil.copyfile(source, args[args.index('-o') + 1])
elif name == 'opkg':
    command = args[0]
    if command == 'print-architecture':
        print('arch all 1\narch ' + os.environ.get('FAKE_ARCH', 'mipsel-3.4') + ' 10')
    elif command == 'status':
        if (root / 'installed').exists():
            print('Version: ' + (root / 'installed').read_text())
            print('Description: ' + os.environ.get('FAKE_DESCRIPTION', 'WeirdTrickle DNS-based routing application'))
    elif command == 'compare-versions':
        sys.exit(0 if os.environ.get('FAKE_NEWER') else 1)
    elif command == 'flag':
        (root / 'flag').write_text(args[1])
    elif command == '--force-downgrade':
        if (root / 'installed').exists() and (root / 'installed').read_text() == pathlib.Path(args[-1]).name.split('_')[1] and '--force-reinstall' not in args:
            sys.exit(0)  # opkg treats the same version as already installed.
        if os.environ.get('FAKE_INSTALL_FAIL'): sys.exit(1)
        (root / 'installed').write_text(pathlib.Path(args[-1]).name.split('_')[1])
        config = root / 'opt/var/lib/magitrickle/config.yaml'
        config.parent.mkdir(parents=True, exist_ok=True)
        config.write_text('package defaults\n')
        if os.environ.get('FAKE_PARTIAL_INSTALL_FAIL'): sys.exit(1)
    elif command != 'update':
        raise SystemExit('Unexpected opkg command: ' + command)
elif name == 'S99magitrickle':
    pass
else:
    raise SystemExit('Unexpected command: ' + name)
'''


class InstallerTests(unittest.TestCase):
    version = '1.0.0'

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = dict(os.environ, FAKE_ROOT=str(self.root))
        self.opt = self.root / 'opt'
        for directory in ['bin', 'etc/init.d', 'etc/ndm']:
            (self.opt / directory).mkdir(parents=True, exist_ok=True)
        for name in ['opkg', 'curl', 'id']:
            self.executable(self.opt / 'bin' / name, FAKE)
        self.executable(self.opt / 'etc/init.d/S99magitrickle', FAKE)
        source = (Path(__file__).resolve().parents[1] / 'install.sh').read_text()
        source = source.replace('/opt', str(self.opt))
        source = source.replace('/tmp/magitrickle-update.', str(self.root / 'magitrickle-update.'))
        source = source.replace('sleep 2', ':')
        self.script = self.root / 'install.sh'
        self.script.write_text(source)
        self.release = self.root / 'release'
        self.release.mkdir()
        self.prepare_release(self.version)

    def prepare_release(self, version):
        self.version = version
        (self.release / 'VERSION').write_text(self.version + '\n')
        self.manifest = ''
        for target in ['mipsel-3.4', 'mipsel-3.4_kn', 'aarch64-3.10_kn']:
            filename = f'magitrickle_{self.version}-1_entware_{target}.ipk'
            data = ('test package ' + target).encode()
            (self.release / filename).write_bytes(data)
            self.manifest += hashlib.sha256(data).hexdigest() + '  ' + filename + '\n'
        (self.release / 'SHA256SUMS').write_text(self.manifest)

    def executable(self, path, source):
        path.write_text(source)
        path.chmod(0o755)

    def run_installer(self, *args, success=True):
        result = subprocess.run(['sh', str(self.script), *args], env=self.env, text=True, capture_output=True)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        return (self.root / 'calls').read_text()

    def existing_config(self, version='0.8.0-1'):
        (self.root / 'installed').write_text(version)
        config = self.opt / 'var/lib/magitrickle/config.yaml'
        config.parent.mkdir(parents=True)
        config.write_bytes(b'groups: [custom]\nsubscriptions: [custom]\n')
        return config

    def seed_backups(self, count=3):
        directory = self.opt / 'var/backups/magitrickle'
        directory.mkdir(parents=True, exist_ok=True)
        names = [
            'before-2026.10.16.20-20261016T120000Z-31.tar.gz',
            'before-2.0.2-20261018T120000Z-32.tar.gz',
            'before-2.1.0-20261019T120000Z-33.tar.gz',
        ]
        archives = []
        for index, name in enumerate(names[:count]):
            archive = directory / name
            archive.write_bytes(f'previous backup {index}'.encode())
            # Future mtimes simulate a router whose clock has since moved back.
            stamp = time.time() + 86400 + index
            os.utime(archive, (stamp, stamp))
            archives.append(archive)
        return archives

    def test_fresh_keenetic_install(self):
        calls = self.run_installer()
        self.assertIn('https://github.com/WeirdPhrog/WeirdTrickle/releases/', calls)
        self.assertIn(f'/download/v{self.version}/magitrickle_{self.version}-1_entware_mipsel-3.4_kn.ipk', calls)
        self.assertEqual((self.root / 'flag').read_text(), 'hold')
        self.assertIn('arch mipsel-3.4_kn 250', (self.opt / 'etc/opkg/magitrickle-fork.conf').read_text())
        self.assertIn('S99magitrickle check', calls)

    def test_update_backs_up_and_preserves_config(self):
        config = self.existing_config()
        before = config.read_bytes()
        calls = self.run_installer()
        self.assertEqual(config.read_bytes(), before)
        archives = list((self.opt / 'var/backups/magitrickle').glob('*.tar.gz'))
        self.assertEqual(len(archives), 1)
        self.assertEqual(archives[0].stat().st_mode & 0o777, 0o600)
        with tarfile.open(archives[0]) as archive:
            self.assertEqual(archive.extractfile('var/lib/magitrickle/config.yaml').read(), before)
        self.assertLess(calls.index('opkg flag ok'), calls.index('opkg --force-downgrade'))
        self.assertLess(calls.index('opkg --force-downgrade'), calls.index('opkg flag hold'))

    def test_backup_rotation_keeps_new_and_latest_previous_and_ignores_other_files(self):
        config = self.existing_config()
        old = self.seed_backups()
        self.env['QUOTING_STYLE'] = 'shell-always'
        directory = old[0].parent
        unrelated = [directory / 'manual.tar.gz', directory / 'before-manual.tar.gz',
                     directory / 'before-1.0.0-20261018T120000Z-44\nextra.tar.gz']
        for path in unrelated:
            path.write_bytes(b'untouched')
        subdirectory = directory / 'before-1.0.0-20261018T120000Z-45.tar.gz'
        subdirectory.mkdir()
        outside = self.root / 'outside.tar.gz'
        outside.write_bytes(b'outside')
        link = directory / 'before-1.0.0-20261018T120000Z-46.tar.gz'
        link.symlink_to(outside)

        self.run_installer()

        self.assertFalse(old[0].exists())
        self.assertFalse(old[1].exists())
        self.assertEqual(old[2].read_bytes(), b'previous backup 2')
        new = set(directory.iterdir()) - set(old) - set(unrelated) - {subdirectory, link}
        self.assertEqual(len(new), 1)
        current = new.pop()
        with tarfile.open(current) as archive:
            self.assertEqual(archive.extractfile('var/lib/magitrickle/config.yaml').read(), config.read_bytes())
        self.assertEqual(set(directory.iterdir()), {old[2], current, *unrelated, subdirectory, link})
        self.assertTrue(link.is_symlink())
        self.assertEqual(outside.read_bytes(), b'outside')
        self.assertTrue(all(path.read_bytes() == b'untouched' for path in unrelated))

    def test_current_version_rotates_without_creating_another_archive(self):
        self.existing_config(self.version + '-1')
        old = self.seed_backups()
        (old[0].parent / '.backup.incomplete').write_bytes(b'interrupted backup')
        self.assertNotIn('opkg update', self.run_installer())
        self.assertEqual(set(old[0].parent.iterdir()), set(old[1:]))

    def test_check_does_not_rotate_backups(self):
        self.existing_config()
        old = self.seed_backups()
        incomplete = old[0].parent / '.backup.incomplete'
        incomplete.write_bytes(b'interrupted backup')
        self.run_installer('--check')
        self.assertEqual(set(old[0].parent.iterdir()), {*old, incomplete})

    def test_failed_archive_preserves_both_previous_backups(self):
        self.existing_config()
        old = self.seed_backups(2)
        self.executable(self.opt / 'bin/tar', '#!/bin/sh\nprintf partial > "$2"\nexit 1\n')
        self.assertNotIn('opkg update', self.run_installer(success=False))
        self.assertEqual(set(old[0].parent.iterdir()), set(old))
        self.assertEqual([path.read_bytes() for path in old], [b'previous backup 0', b'previous backup 1'])
        self.assertFalse((self.root / 'magitrickle-update.lock').exists())

    def test_failed_archive_rename_preserves_both_previous_backups(self):
        self.existing_config()
        old = self.seed_backups(2)
        self.executable(self.opt / 'bin/mv', '#!/bin/sh\nexit 1\n')
        self.assertNotIn('opkg update', self.run_installer(success=False))
        self.assertEqual(set(old[0].parent.iterdir()), set(old))

    def test_failed_install_retains_new_backup_with_two_archive_limit(self):
        config = self.existing_config()
        old = self.seed_backups(2)
        self.env['FAKE_PARTIAL_INSTALL_FAIL'] = '1'
        self.run_installer(success=False)
        archives = set(old[0].parent.iterdir())
        self.assertEqual(len(archives), 2)
        self.assertIn(old[1], archives)
        current = next(path for path in archives if path != old[1])
        with tarfile.open(current) as archive:
            self.assertEqual(archive.extractfile('var/lib/magitrickle/config.yaml').read(), config.read_bytes())

    def test_plain_entware_uses_plain_target(self):
        (self.opt / 'etc/ndm').rmdir()
        calls = self.run_installer()
        self.assertIn(f'entware_mipsel-3.4.ipk', calls)
        self.assertNotIn('entware_mipsel-3.4_kn.ipk', calls)

    def test_arm64_keenetic(self):
        self.env['FAKE_ARCH'] = 'aarch64-3.10'
        self.assertIn('entware_aarch64-3.10_kn.ipk', self.run_installer())

    def test_check_is_read_only(self):
        self.existing_config()
        calls = self.run_installer('--check')
        self.assertNotIn('opkg update', calls)
        self.assertNotIn('opkg flag', calls)
        self.assertFalse((self.opt / 'etc/opkg').exists())

    def test_old_persistent_lock_does_not_block_update(self):
        lock = self.opt / 'var/lock/magitrickle-update.lock'
        lock.mkdir(parents=True)
        self.assertIn('opkg --force-downgrade', self.run_installer())
        self.assertFalse((self.root / 'magitrickle-update.lock').exists())

    def test_concurrent_update_is_rejected(self):
        lock = self.root / 'magitrickle-update.lock'
        lock.mkdir()
        self.assertNotIn('opkg --force-downgrade', self.run_installer(success=False))
        self.assertTrue(lock.exists(), 'must not remove another updater lock')

    def test_architecture_registration_removed_only_on_uninstall(self):
        source = (Path(__file__).resolve().parents[1] / 'files/entware/_ipk/control/postrm').read_text()
        script = self.root / 'postrm'
        script.write_text(source.replace('/opt', str(self.opt)))
        config = self.opt / 'etc/opkg/magitrickle-fork.conf'
        config.parent.mkdir(parents=True)
        for action, remains in [('upgrade', True), ('remove', False), ('purge', False)]:
            with self.subTest(action=action):
                config.write_text('arch mipsel-3.4_kn 250\n')
                subprocess.run(['sh', str(script), action], check=True)
                self.assertEqual(config.exists(), remains)

    def test_current_version_is_noop(self):
        self.existing_config(self.version + '-1')
        self.assertNotIn('opkg --force-downgrade', self.run_installer())

    def test_same_version_upstream_is_reinstalled_and_preserves_config(self):
        config = self.existing_config(self.version + '-1')
        before = config.read_bytes()
        self.env['FAKE_DESCRIPTION'] = 'MagiTrickle DNS-based routing application'
        calls = self.run_installer()
        self.assertIn('opkg --force-downgrade --force-reinstall install', calls)
        self.assertEqual(config.read_bytes(), before)
        self.assertEqual((self.root / 'flag').read_text(), 'hold')
        self.assertEqual(len(list((self.opt / 'var/backups/magitrickle').glob('*.tar.gz'))), 1)

    def test_same_version_unknown_package_is_not_mistaken_for_fork(self):
        self.existing_config(self.version + '-1')
        self.env['FAKE_DESCRIPTION'] = ''
        self.assertIn('opkg --force-downgrade --force-reinstall install', self.run_installer())

    def test_check_same_version_upstream_does_not_reinstall(self):
        self.existing_config(self.version + '-1')
        self.env['FAKE_DESCRIPTION'] = 'MagiTrickle DNS-based routing application'
        calls = self.run_installer('--check')
        self.assertNotIn('opkg update', calls)
        self.assertNotIn('opkg --force-downgrade', calls)
        self.assertNotIn('opkg flag', calls)

    def test_corrupt_package_never_installs(self):
        for package in self.release.glob('*.ipk'):
            package.write_bytes(b'corrupt')
        calls = self.run_installer(success=False)
        self.assertNotIn('opkg update', calls)
        self.assertNotIn('opkg flag', calls)

    def test_missing_checksum_never_installs(self):
        (self.release / 'SHA256SUMS').write_text('')
        self.assertNotIn('opkg update', self.run_installer(success=False))

    def test_invalid_version_never_installs(self):
        for version in ['../bad', '01.0.0', '1.0', '1.0.0\nextra', '2026.10.16.20']:
            with self.subTest(version=version):
                (self.release / 'VERSION').write_text(version)
                self.assertNotIn('opkg update', self.run_installer(success=False))

    def test_calendar_version_migrates_without_losing_config(self):
        config = self.existing_config('2026.10.16.20-1')
        before = config.read_bytes()
        self.env['FAKE_NEWER'] = '1'
        calls = self.run_installer()
        self.assertIn('opkg --force-downgrade install', calls)
        self.assertEqual((self.root / 'installed').read_text(), '1.0.0-1')
        self.assertEqual(config.read_bytes(), before)

    def test_semver_downgrade_still_refused(self):
        self.existing_config('1.1.0-1')
        self.env['FAKE_NEWER'] = '1'
        self.assertNotIn('opkg update', self.run_installer(success=False))

    def test_next_patch_release_installs(self):
        self.existing_config('1.0.0-1')
        self.prepare_release('1.0.1')
        self.run_installer()
        self.assertEqual((self.root / 'installed').read_text(), '1.0.1-1')

    def test_explicit_legacy_release_remains_available(self):
        self.existing_config('1.0.0-1')
        self.prepare_release('2026.10.16.20')
        calls = self.run_installer('--version', self.version)
        self.assertNotIn('/latest/', calls)
        self.assertIn('/download/v2026.10.16.20/', calls)

    def test_download_failure_never_installs(self):
        (self.release / 'SHA256SUMS').unlink()
        self.assertNotIn('opkg update', self.run_installer(success=False))

    def test_failed_install_restores_hold(self):
        config = self.existing_config()
        before = config.read_bytes()
        self.env['FAKE_INSTALL_FAIL'] = '1'
        self.run_installer(success=False)
        self.assertEqual((self.root / 'flag').read_text(), 'hold')
        self.assertEqual(config.read_bytes(), before)

    def test_automatic_downgrade_refused(self):
        self.existing_config('9999.1.1.1-1')
        self.env['FAKE_NEWER'] = '1'
        self.assertNotIn('opkg update', self.run_installer(success=False))

    def test_partial_install_failure_restores_config(self):
        config = self.existing_config()
        before = config.read_bytes()
        self.env['FAKE_PARTIAL_INSTALL_FAIL'] = '1'
        self.run_installer(success=False)
        self.assertEqual(config.read_bytes(), before)

    def test_explicit_version_allows_rollback(self):
        self.existing_config('9999.1.1.1-1')
        self.env['FAKE_NEWER'] = '1'
        calls = self.run_installer('--version', self.version)
        self.assertNotIn('/latest/', calls)
        self.assertIn('opkg --force-downgrade install', calls)


if __name__ == '__main__':
    unittest.main()
