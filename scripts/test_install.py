"""Protect atomic installation from corrupted or mislabeled release archives."""
import hashlib
import io
from pathlib import Path
import platform
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent


class InstallerTests(unittest.TestCase):
    """Use synthetic release payloads without downloading or installing globally."""

    def exercise(self, payload_version, corrupt=False):
        """Run the installer against a local archive and return result plus installed bytes."""
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            target_os = 'darwin' if platform.system() == 'Darwin' else 'linux'
            arch = 'arm64' if platform.machine() in ('arm64', 'aarch64') else 'amd64'
            asset = f'in-v1.2.3-{target_os}-{arch}.tar.gz'
            archive = root / asset
            payload = f'#!/bin/sh\nprintf "%s\\n" "{payload_version}"\n'.encode()
            with tarfile.open(archive, 'w:gz') as bundle:
                entry = tarfile.TarInfo('in')
                entry.size = len(payload)
                entry.mode = 0o755
                bundle.addfile(entry, io.BytesIO(payload))
            digest = hashlib.sha256(archive.read_bytes()).hexdigest()
            manifest = root / 'checksums.txt'
            manifest.write_text(f'{digest}  {asset}\n')
            if corrupt:
                archive.write_bytes(archive.read_bytes() + b'corruption')
            destination = root / 'bin'
            destination.mkdir()
            (destination / 'in').write_bytes(b'existing binary')
            result = subprocess.run(['sh', str(ROOT / 'install.sh'), '--version', 'v1.2.3',
                                     '--archive', str(archive), '--checksums', str(manifest),
                                     '--dir', str(destination)], capture_output=True)
            return result.returncode, (destination / 'in').read_bytes()

    def test_checksum_failure_preserves_existing_binary(self):
        code, installed = self.exercise('v1.2.3', corrupt=True)
        self.assertNotEqual(code, 0)
        self.assertEqual(installed, b'existing binary')

    def test_version_failure_preserves_existing_binary(self):
        code, installed = self.exercise('v9.9.9')
        self.assertNotEqual(code, 0)
        self.assertEqual(installed, b'existing binary')

    def test_verified_archive_replaces_binary(self):
        code, installed = self.exercise('v1.2.3')
        self.assertEqual(code, 0)
        self.assertIn(b'v1.2.3', installed)
