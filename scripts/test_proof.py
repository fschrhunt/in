"""Protect the proof from accepting broken OAuth validation or lost SSH exit codes."""

import argparse
import base64
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from urllib.parse import urlencode, urlsplit

from fixtures import oauth
from prove import Proof, tailscale_identity, wait_file


class ProofTests(unittest.TestCase):
    """Verify assertions that make the acceptance result trustworthy."""

    def test_remote_failure_survives_successful_ssh_exit(self):
        """Some SSH endpoints return zero regardless of the command's exit status."""
        proof = Proof(argparse.Namespace(), Path('/unused'))
        result = subprocess.run(['/bin/sh', '-c', proof.checked_command('exit 7')],
                                capture_output=True, text=True, check=True)
        self.assertEqual(proof.command_result(result, False).returncode, 7)
        result.stdout = json.dumps({'status': 7, 'stdout': '', 'stderr': ''})
        with self.assertRaises(RuntimeError):
            proof.command_result(result, True)

    def test_failed_uninstall_preserves_recovery_files(self):
        """Cleanup must still close its master but retain files if uninstall fails."""
        proof = Proof(argparse.Namespace(server='fixture'), Path('/unused'))
        proof.mac, proof.prefix, proof.configured = '/tmp/in-proof.fixture', '', True
        calls = []

        def laptop(command):
            """Simulate an uninstall failure without executing any external command."""
            calls.append(command)
            if command.endswith(' uninstall'):
                raise RuntimeError('stop failed')

        proof.laptop = laptop
        self.assertEqual(proof.cleanup(), ['laptop uninstall'])
        self.assertTrue(any('-O exit' in command for command in calls))
        self.assertFalse(any('rm -rf' in command for command in calls))

    def test_et_login_requires_remote_status_file(self):
        """A clean ET client exit alone cannot certify fixture authentication."""
        with tempfile.TemporaryDirectory() as directory:
            proof = Proof(argparse.Namespace(client='et', server='fixture'), Path(directory))
            proof.mac, proof.config, proof.marker = '/tmp/fixture', '/tmp/fixture/config', '/tmp/fixture/marker'
            proof.prefix, proof.et_mac_port = '', 12345

            def successful_et(_command):
                """Return a client exit without any remote fixture completion."""
                return subprocess.CompletedProcess([], 0, '{"status": 0}', '')

            proof.laptop = successful_et
            with self.assertRaisesRegex(RuntimeError, 'ET fixture login failed'):
                proof.cli('login', directory, 'dynamic-port')

    def test_tailscale_requires_canonical_magicdns_identity(self):
        """Only a resolved MagicDNS host and simple user may enter private SSH config."""
        self.assertEqual(tailscale_identity('hostname host.tail.ts.net.\nuser fixture\n'),
                         ('host.tail.ts.net', 'fixture'))
        with self.assertRaisesRegex(RuntimeError, 'MagicDNS'):
            tailscale_identity('hostname 100.100.0.1\nuser fixture\n')
        with self.assertRaisesRegex(RuntimeError, 'simple SSH user'):
            tailscale_identity('hostname host.tail.ts.net\nuser bad name\n')

    def test_tailscale_remote_uses_canonical_wrapper_destination(self):
        """Remote fixture commands must enter Tailscale with the dotted target first."""
        proof = Proof(argparse.Namespace(client='tailscale'), Path('/unused'))
        proof.tailscale_target, proof.config, proof.prefix = 'fixture@host.tail.ts.net.', '/tmp/ssh.conf', ''
        commands = []

        def laptop(command):
            """Capture the private wrapper invocation without opening a connection."""
            commands.append(command)
            return subprocess.CompletedProcess([], 0,
                                               json.dumps({'status': 0, 'stdout': 'ok', 'stderr': ''}), '')

        proof.laptop = laptop
        self.assertEqual(proof.remote('printf ok').stdout, 'ok')
        self.assertIn('tailscale ssh fixture@host.tail.ts.net. -F /tmp/ssh.conf ', commands[0])

    def test_provider_requires_pkce_redirect_and_one_use_code(self):
        """A successful fixture exchange must mean PKCE and redirect binding were checked."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            provider = subprocess.Popen([sys.executable, oauth.__file__, 'provider', directory],
                                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                wait_file(root / 'provider.json')
                port = json.loads((root / 'provider.json').read_text())['port']
                verifier = 'test-verifier'
                challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b'=').decode()
                redirect = 'http://127.0.0.1:19000/callback'

                def code():
                    """Obtain a new code bound to this test's redirect and challenge."""
                    query = urlencode(dict(response_type='code', client_id='in-proof',
                                           redirect_uri=redirect, state='test-state', case='test',
                                           code_challenge=challenge, code_challenge_method='S256'))
                    status, headers, _ = oauth.request(port, 'GET', '/authorize?' + query)
                    self.assertEqual(status, 302)
                    return oauth.fields(urlsplit(headers['Location']).query)['code']

                def exchange(value, proof=verifier, uri=redirect):
                    """Try one exchange without exposing the code in test output."""
                    body = oauth.form_body(dict(grant_type='authorization_code', code=value,
                                                 code_verifier=proof, redirect_uri=uri))
                    return oauth.request(port, 'POST', '/token', body,
                                         {'Content-Type': 'application/x-www-form-urlencoded'})

                self.assertEqual(exchange(code(), proof='wrong')[0], 400)
                self.assertEqual(exchange(code(), uri=redirect + '-wrong')[0], 400)
                value = code()
                status, _, body = exchange(value)
                self.assertEqual(status, 200)
                token = json.loads(body)['access_token']
                self.assertEqual(oauth.request(port, 'GET', '/me', headers={'Authorization': 'Bearer ' + token})[0], 200)
                self.assertEqual(exchange(value)[0], 400)
                self.assertEqual(oauth.request(port, 'GET', '/me', headers={'Authorization': 'Bearer invalid'})[0], 401)
            finally:
                provider.terminate()
                try:
                    provider.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    provider.kill()
                    provider.wait()
