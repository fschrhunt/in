"""Exercise installed browser hooks over disposable real SSH and Mosh transports.

Both ends run on one machine. A fixture adapter maps remote callback connections
onto 127.0.0.2, keeping laptop and remote loopbacks distinct without root or a
network namespace. All keys, browser URLs and callback codes are synthetic.
"""
import fcntl
import termios
import struct
import json
import os
from pathlib import Path
import pty
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parent.parent


class LoginIntegration(unittest.TestCase):
    """Keep one disposable SSH server, with fresh installation state per test."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='in-')
        self.root = Path(self.temp.name)
        self.addCleanup(self.temp.cleanup)
        self.local = self.root / 'laptop'
        self.remote = self.root / 'remote'
        self.config = self.root / 'ssh.conf'
        self.rc = self.root / 'laptop.rc'
        self.remote_rc = self.root / 'remote.rc'
        self.env = dict(os.environ, IN_HOME=str(self.local), IN_BROWSER_EXEC=str(self.root / 'browser'))
        for name in ('host', 'client'):
            self.run_local('ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(self.root / name))
        self.port = self.free_port('127.0.0.1')
        self.run_local(str(ROOT / 'bin/in'), 'setup', 'remote', '--shell-file', str(self.remote_rc),
                       env=dict(os.environ, IN_HOME=str(self.remote)))
        # sshd's restricted environment may omit Mosh's binary and UTF-8 locale.
        mosh_path = str(Path(shutil.which('mosh-server')).parent) if shutil.which('mosh-server') else ''
        self.write_executable('session', '#!/bin/sh\nexport PATH=' + shlex.quote(mosh_path) + ':"$PATH"' +
                              '\nexport LC_ALL=C.UTF-8' +
                              '\nexport IN_HOME=' + shlex.quote(str(self.remote)) +
                              '\n. ' + shlex.quote(str(self.remote_rc)) +
                              '\nexec /bin/sh -c "$SSH_ORIGINAL_COMMAND"\n')
        self.write_executable('remote-in', '#!/bin/sh\nif [ "$1" = pipe ]; then\n  exec ' +
                              shlex.quote(str(self.remote / 'bin/in')) + ' pipe 127.0.0.2 "$3" "$4" "${5:-}"\nfi\nexec ' +
                              shlex.quote(str(self.remote / 'bin/in')) + ' "$@"\n')
        self.write_executable('browser', '#!' + sys.executable + '\n' + '''import json, os, socket, sys, time
from pathlib import Path
from urllib.parse import urlsplit, parse_qs
url = sys.argv[1]
values = parse_qs(urlsplit(url).query)
callback = urlsplit(values['redirect_uri'][0])
port = callback.port
def record(value):
    path = Path(os.environ['IN_HOME'], 'browser-result')
    pending = path.with_name('browser-result.' + str(os.getpid()))
    pending.write_text(value)
    pending.replace(path)
try:
    if os.environ.get('IN_FIXTURE_PRECONNECT'):
        with socket.create_connection(('127.0.0.1', port), timeout=15): pass
    with socket.create_connection(('127.0.0.1', port), timeout=15) as conn:
        request = ('POST ' + callback.path + '?code=fixture&state=fixture HTTP/1.1\\r\\nHost: localhost:' + str(port) + '\\r\\nContent-Length: 7\\r\\nConnection: close\\r\\n\\r\\nfixture').encode()
        conn.sendall(request)
        conn.shutdown(socket.SHUT_WR)
        response = b''
        while chunk := conn.recv(4096):
            response += chunk
    record('ok' if response.endswith(b'approved') else 'bad response')
except Exception:
    record('transport failed')
''')
        self.write_executable('login', '#!' + sys.executable + '\n' + '''import os, socket, subprocess, sys
from urllib.parse import quote
port = int(sys.argv[1])
late = len(sys.argv) > 2 and sys.argv[2] == 'late'
listener = socket.socket()
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
def bind():
    listener.bind(('127.0.0.2', port))
    listener.listen()
    listener.settimeout(15)
if not late: bind()
url = 'https://provider.invalid/authorize?response_type=code&client_id=fixture&state=fixture&redirect_uri=' + quote('http://localhost:' + str(port) + '/callback', safe='')
if len(sys.argv) > 2 and sys.argv[2] == 'detached':
    result = subprocess.run([sys.executable, '-c', 'import os,subprocess,sys; subprocess.run([os.environ["BROWSER"],sys.argv[1]],check=True)', url], start_new_session=True, capture_output=True)
else:
    result = subprocess.run(['xdg-open' if len(sys.argv) > 2 and sys.argv[2] == 'xdg' else os.environ['BROWSER'], url], capture_output=True)
if result.returncode:
    print('browser hook rejected request', file=sys.stderr)
    sys.exit(1)
if late: bind()
with listener:
    while True:
      with listener.accept()[0] as conn:
          conn.settimeout(15)
          payload = b''
          while chunk := conn.recv(4096): payload += chunk
          if not payload: continue
          expected = ('POST /callback?code=fixture&state=fixture HTTP/1.1\\r\\nHost: localhost:' + str(port) + '\\r\\nContent-Length: 7\\r\\nConnection: close\\r\\n\\r\\nfixture').encode()
          if payload != expected:
              print('callback bytes changed', file=sys.stderr)
              sys.exit(2)
          conn.sendall(b'HTTP/1.1 200 OK\\r\\nContent-Length: 8\\r\\nConnection: close\\r\\n\\r\\napproved')
      break
print('authenticated fixture')
''')
        self.config.write_text(f'''Host fixture
  HostName 127.0.0.1
  Port {self.port}
  User {os.environ.get('USER', 'runner')}
  IdentityFile {self.root}/client
  IdentitiesOnly yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
''')
        self.base_config = self.config.read_text()
        server_config = self.root / 'sshd.conf'
        server_config.write_text(f'''Port {self.port}
ListenAddress 127.0.0.1
HostKey {self.root}/host
PidFile {self.root}/sshd.pid
AuthorizedKeysFile {self.root}/client.pub
StrictModes no
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
LogLevel ERROR
Subsystem sftp /usr/lib/openssh/sftp-server
ForceCommand {self.root}/session
''')
        self.log = open(self.root / 'server.log', 'w')
        self.addCleanup(self.log.close)
        sshd = shutil.which('sshd') or '/usr/sbin/sshd'
        self.server = subprocess.Popen([sshd, '-D', '-e', '-f', str(server_config)], stderr=self.log)
        self.addCleanup(self.end_server)
        deadline = time.monotonic() + 3
        while True:
            try:
                with socket.create_connection(('127.0.0.1', self.port), timeout=.1): break
            except OSError:
                if time.monotonic() > deadline: self.fail('disposable SSH server did not start')
                time.sleep(.03)
        self.run_local(str(ROOT / 'bin/in'), 'setup', 'laptop', '--shell-file', str(self.rc),
                       '--ssh-config', str(self.config), '--remote', str(self.root / 'remote-in'), 'fixture')

    def run_local(self, *args, env=None, timeout=20):
        """Capture fixture output without echoing SSH keys or authorization URLs."""
        return subprocess.run(args, env=env or self.env, check=True, capture_output=True, text=True, timeout=timeout)

    def write_executable(self, name, content):
        """Write an isolated executable fixture."""
        path = self.root / name
        path.write_text(content)
        path.chmod(0o700)

    def free_port(self, host):
        """Choose a fixture port unused on both simulated IPv4 hosts for callbacks."""
        with socket.socket() as sock:
            sock.bind(('0.0.0.0' if host == '127.0.0.2' else host, 0))
            return sock.getsockname()[1]

    def end_server(self):
        """Stop only test-owned SSH processes and coordinator connections."""
        subprocess.run([str(ROOT / 'bin/in'), 'stop'], env=self.env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        subprocess.run(['ssh', '-F', str(self.config), '-O', 'exit', 'fixture'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.server.terminate()
        self.server.wait(timeout=5)

    def ssh(self, command):
        """Use the installed LocalCommand hook, without an in command wrapper."""
        return self.run_local('ssh', '-F', str(self.config), 'fixture', command)

    def assert_browser_completed(self):
        """Wait for the fake laptop browser to observe the original CLI response."""
        path = self.local / 'browser-result'
        deadline = time.monotonic() + 5
        while not path.exists() and time.monotonic() < deadline: time.sleep(.02)
        self.assertTrue(path.exists(), 'laptop browser did not receive the callback response')
        self.assertEqual(path.read_text(), 'ok')

    def test_late_listener_and_cleanup(self):
        """The hook returns before listener startup; cancelling in frees the laptop port."""
        port = self.free_port('127.0.0.2')
        result = self.ssh(shlex.quote(str(self.root / 'login')) + f' {port} late')
        self.assertEqual(result.stdout.strip(), 'authenticated fixture')
        self.assert_browser_completed()
        self.run_local(str(ROOT / 'bin/in'), 'stop')
        deadline = time.monotonic() + 3
        while True:
            try:
                with socket.socket() as listener: listener.bind(('127.0.0.1', port))
                break
            except OSError:
                if time.monotonic() > deadline: self.fail('owned callback port leaked')
                time.sleep(.02)
        self.assertEqual(self.ssh('printf preserved').stdout, 'preserved')

    def test_login_waits_for_session_registration(self):
        """A browser hook started before relay registration follows its original SSH tuple."""
        helper = self.root / 'remote-in'
        helper.write_text(helper.read_text().replace(
            '#!/bin/sh\n', '#!/bin/sh\nif [ "$1" = relay ]; then sleep .3; fi\n', 1))
        port = self.free_port('127.0.0.2')
        result = self.ssh(shlex.quote(str(self.root / 'login')) + f' {port} late')
        self.assertEqual(result.stdout.strip(), 'authenticated fixture')
        self.assert_browser_completed()

    def test_mosh_uses_normal_shell_integration(self):
        """The installed Mosh function retains the authenticated route through handoff."""
        if not shutil.which('mosh') or not shutil.which('mosh-server'): self.skipTest('Mosh is not installed')
        port = self.free_port('127.0.0.2')
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
        command = '. ' + shlex.quote(str(self.rc)) + '; mosh --no-ssh-pty --ssh=' + shlex.quote('ssh -F ' + str(self.config)) + ' fixture -- ' + shlex.quote(str(self.root / 'login')) + f' {port} late'
        env = dict(self.env, TERM='xterm-256color', LC_ALL='C.UTF-8')
        proc = subprocess.Popen(['bash', '-c', command], stdin=slave, stdout=slave, stderr=slave, env=env)
        os.close(slave)
        try:
            proc.wait(timeout=25)
            if proc.returncode != 0:
                os.set_blocking(master, False)
                try: diagnostic = os.read(master, 16384).decode(errors='replace')
                except OSError: diagnostic = ''
                # Mosh keys are ephemeral test secrets; never include startup records.
                diagnostic = '\n'.join(line for line in diagnostic.splitlines() if 'MOSH CONNECT' not in line)
                self.fail('Mosh fixture failed: ' + diagnostic)
            self.assert_browser_completed()
        finally:
            if proc.poll() is None: proc.kill(); proc.wait()
            os.close(master)

    def test_mosh_login_survives_ssh_master_replacement(self):
        """A live Mosh terminal can authenticate after its original SSH master exits."""
        if not shutil.which('mosh') or not shutil.which('mosh-server'): self.skipTest('Mosh is not installed')
        ready = self.root / 'mosh-ready'
        release = self.root / 'mosh-release'
        port = self.free_port('127.0.0.2')
        self.write_executable('gated-login', '#!/bin/sh\n' +
                              'printf "%s" "$SSH_CONNECTION" >' + shlex.quote(str(ready)) + '\n' +
                              'n=0\nwhile [ ! -e ' + shlex.quote(str(release)) + ' ] && [ "$n" -lt 400 ]; do\n' +
                              '  sleep .05\n  n=$((n + 1))\ndone\n' +
                              '[ -e ' + shlex.quote(str(release)) + ' ] || exit 1\n' +
                              'exec ' + shlex.quote(str(self.root / 'login')) + f' {port} late\n')
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 24, 80, 0, 0))
        command = '. ' + shlex.quote(str(self.rc)) + '; mosh --no-ssh-pty --ssh=' + \
                  shlex.quote('ssh -F ' + str(self.config)) + ' fixture -- ' + \
                  shlex.quote(str(self.root / 'gated-login'))
        proc = subprocess.Popen(['bash', '-c', command], stdin=slave, stdout=slave, stderr=slave,
                                env=dict(self.env, TERM='xterm-256color', LC_ALL='C.UTF-8'))
        os.close(slave)
        try:
            deadline = time.monotonic() + 12
            while not ready.exists() and proc.poll() is None and time.monotonic() < deadline: time.sleep(.03)
            if not ready.exists():
                os.set_blocking(master, False)
                try: diagnostic = os.read(master, 16384).decode(errors='replace')
                except OSError: diagnostic = ''
                diagnostic = '\n'.join(line for line in diagnostic.splitlines() if 'MOSH CONNECT' not in line)
                self.fail('Mosh terminal did not reach the login gate: ' + diagnostic)
            self.assertTrue(ready.read_text().strip(), 'Mosh terminal lost its original SSH connection tuple')
            self.run_local('ssh', '-F', str(self.config), '-O', 'exit', 'fixture', timeout=5)
            self.assertIsNone(proc.poll(), 'Mosh terminal exited with its SSH master')
            self.assertEqual(self.ssh('printf reconnected').stdout, 'reconnected')
            release.touch()
            proc.wait(timeout=25)
            self.assertEqual(proc.returncode, 0, 'Mosh login failed after SSH master replacement')
            self.assert_browser_completed()
        finally:
            release.touch()
            if proc.poll() is None: proc.kill(); proc.wait(timeout=5)
            os.close(master)

    def test_preconnect_does_not_consume_route(self):
        """An empty browser preconnect cannot steal or prematurely close the callback route."""
        self.env['IN_FIXTURE_PRECONNECT'] = '1'
        port = self.free_port('127.0.0.2')
        result = self.ssh(shlex.quote(str(self.root / 'login')) + f' {port}')
        self.assertEqual(result.stdout.strip(), 'authenticated fixture')
        self.assert_browser_completed()

    def test_dev_browser_open_leaves_server_alone(self):
        """A dev tool's direct localhost open must not reserve a laptop port or open its browser."""
        marker = self.root / 'dev-ready'
        self.write_executable('dev', '#!' + sys.executable + '\n' + '''import os, socket, subprocess, sys
from pathlib import Path
with socket.socket() as listener:
    listener.bind(('127.0.0.2', int(sys.argv[1])))
    listener.listen()
    listener.settimeout(8)
    result = subprocess.run([os.environ['BROWSER'], 'http://localhost:'+sys.argv[1]+'/?state=fixture'], capture_output=True)
    if result.returncode == 0: raise SystemExit('dev URL was accepted as login')
    Path(sys.argv[2]).touch()
    with listener.accept()[0]: pass
print('dev server still reachable')
''')
        port = self.free_port('127.0.0.2')
        command = shlex.join([str(self.root / 'dev'), str(port), str(marker)])
        proc = subprocess.Popen(['ssh', '-F', str(self.config), 'fixture', command], env=self.env,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            deadline = time.monotonic() + 4
            while not marker.exists() and time.monotonic() < deadline: time.sleep(.02)
            self.assertTrue(marker.exists(), 'dev browser request was not refused')
            self.assertFalse((self.local / 'browser-result').exists())
            with socket.socket() as local: local.bind(('127.0.0.1', port))
            with socket.create_connection(('127.0.0.2', port), timeout=1): pass
            output, _ = proc.communicate(timeout=3)
            self.assertEqual(proc.returncode, 0)
            self.assertEqual(output.strip(), 'dev server still reachable')
        finally:
            if proc.poll() is None: proc.terminate(); proc.communicate(timeout=3)

    def test_detached_opener_retains_cli_ownership(self):
        """A new POSIX session in an opener must not hide its original CLI ancestor."""
        port = self.free_port('127.0.0.2')
        result = self.ssh(shlex.quote(str(self.root / 'login')) + f' {port} detached')
        self.assertEqual(result.stdout.strip(), 'authenticated fixture')
        self.assert_browser_completed()

    def test_native_opener_uses_the_same_callback_path(self):
        """A tool that calls xdg-open directly needs no BROWSER integration of its own."""
        port = self.free_port('127.0.0.2')
        result = self.ssh(shlex.quote(str(self.root / 'login')) + f' {port} xdg')
        self.assertEqual(result.stdout.strip(), 'authenticated fixture')
        self.assert_browser_completed()

    def test_stop_closes_active_remote_callback(self):
        """Stopping a hanging callback must reap its remote helper through an SSH master."""
        marker = self.root / 'accepted'
        self.write_executable('hold', '#!' + sys.executable + '\n' + '''import os, socket, subprocess, sys, time
from pathlib import Path
from urllib.parse import quote
with socket.socket() as listener:
    listener.bind(('127.0.0.2', int(sys.argv[1])))
    listener.listen()
    url = 'https://provider.invalid/authorize?response_type=code&client_id=fixture&redirect_uri=' + quote('http://localhost:'+sys.argv[1]+'/callback', safe='')
    subprocess.run([os.environ['BROWSER'], url], check=True)
    with listener.accept()[0]:
        Path(sys.argv[2]).write_text(str(os.getpid()))
        time.sleep(30)
''')
        port = self.free_port('127.0.0.2')
        command = shlex.join([str(self.root / 'hold'), str(port), str(marker)])
        proc = subprocess.Popen(['ssh', '-F', str(self.config), 'fixture', command], env=self.env,
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        helpers = []
        try:
            deadline = time.monotonic() + 4
            while not marker.exists() and time.monotonic() < deadline: time.sleep(.02)
            self.assertTrue(marker.exists(), 'fixture did not accept callback')
            for path in Path('/proc').glob('[0-9]*/exe'):
                try:
                    if path.readlink() != self.remote / 'bin/in': continue
                    args = (path.parent / 'cmdline').read_bytes().split(b'\x00')
                    if b'pipe' in args: helpers.append(path.parent)
                except OSError: continue
            self.assertEqual(len(helpers), 1)
            self.run_local(str(ROOT / 'bin/in'), 'stop')
            deadline = time.monotonic() + 2
            while any(path.exists() for path in helpers) and time.monotonic() < deadline: time.sleep(.02)
            self.assertFalse(any(path.exists() for path in helpers), 'remote callback helper leaked')
            self.run_local('ssh', '-F', str(self.config), '-O', 'check', 'fixture')
        finally:
            if marker.exists():
                try: os.kill(int(marker.read_text()), signal.SIGTERM)
                except ProcessLookupError: pass
            if proc.poll() is None: proc.terminate()
            proc.wait(timeout=5)

    def test_port_conflict_preserves_other_listener(self):
        """Refuse a login before opening the browser when the laptop port is occupied."""
        with socket.socket() as occupied:
            occupied.bind(('127.0.0.1', 0))
            occupied.listen()
            port = occupied.getsockname()[1]
            with self.assertRaises(subprocess.CalledProcessError):
                self.ssh(shlex.quote(str(self.root / 'login')) + f' {port}')
            with socket.create_connection(('127.0.0.1', port), timeout=1): pass
            self.assertFalse((self.local / 'browser-result').exists())

    def test_late_unrelated_remote_listener_receives_no_connection(self):
        """A listener absent from the advertised endpoint cannot qualify through unrelated ancestors."""
        with socket.socket() as unrelated:
            unrelated.bind(('127.0.0.2', 0))
            unrelated.listen()
            unrelated.settimeout(.2)
            port = unrelated.getsockname()[1]
            url = f'https://provider.invalid/authorize?response_type=code&client_id=fixture&redirect_uri=http%3A%2F%2Flocalhost%3A{port}%2Fcallback'
            # Keep a real CLI ancestor alive through hook acknowledgement; a shell may exec the hook.
            self.write_executable('unrelated-open', '#!' + sys.executable + '\nimport os,subprocess,sys\n' +
                                  'sys.exit(subprocess.run([os.environ["BROWSER"],sys.argv[1]]).returncode)\n')
            self.ssh(shlex.quote(str(self.root / 'unrelated-open')) + ' ' + shlex.quote(url))
            result = self.local / 'browser-result'
            deadline = time.monotonic() + 5
            while not result.exists() and time.monotonic() < deadline: time.sleep(.02)
            self.assertTrue(result.exists(), 'browser did not observe refusal')
            self.assertEqual(result.read_text(), 'transport failed')
            with self.assertRaises(socket.timeout): unrelated.accept()

    def test_two_laptops_keep_browser_requests_separate(self):
        """Two authenticated transports to one account retain distinct laptop ownership."""
        second = self.root / 'second'
        config = self.root / 'second.conf'
        config.write_text(self.base_config)
        env = dict(self.env, IN_HOME=str(second))
        self.run_local(str(ROOT / 'bin/in'), 'setup', 'laptop', '--shell-file', str(self.root / 'second.rc'),
                       '--ssh-config', str(config), '--remote', str(self.root / 'remote-in'), 'fixture', env=env)
        try:
            self.run_local('ssh', '-F', str(config), 'fixture', 'true', env=env)
            port = self.free_port('127.0.0.2')
            self.ssh(shlex.quote(str(self.root / 'login')) + f' {port}')
            self.assert_browser_completed()
            self.assertFalse((second / 'browser-result').exists())
            port = self.free_port('127.0.0.2')
            self.run_local('ssh', '-F', str(config), 'fixture', shlex.quote(str(self.root / 'login')) + f' {port}', env=env)
            deadline = time.monotonic() + 3
            while not (second / 'browser-result').exists() and time.monotonic() < deadline: time.sleep(.02)
            self.assertEqual((second / 'browser-result').read_text(), 'ok')
        finally:
            subprocess.run([str(ROOT / 'bin/in'), 'stop'], env=env, capture_output=True)
            subprocess.run(['ssh', '-F', str(config), '-O', 'exit', 'fixture'], capture_output=True)

    def test_existing_master_is_reused_and_preserved(self):
        """Join a master created before setup and leave it running when in stops."""
        self.run_local(str(ROOT / 'bin/in'), 'uninstall')
        self.config.write_text(self.base_config + '\nHost fixture\n  ControlMaster auto\n  ControlPath ' + str(self.root / 'existing') + '\n  ControlPersist 600\n')
        self.run_local('ssh', '-F', str(self.config), '-N', '-f', 'fixture')
        self.run_local(str(ROOT / 'bin/in'), 'setup', 'laptop', '--shell-file', str(self.rc),
                       '--ssh-config', str(self.config), '--remote', str(self.root / 'remote-in'), 'fixture')
        port = self.free_port('127.0.0.2')
        try:
            self.ssh(shlex.quote(str(self.root / 'login')) + f' {port}')
        except subprocess.CalledProcessError:
            error = self.local / 'last-error'
            options = self.run_local('ssh', '-G', '-F', str(self.config), 'fixture').stdout
            selected = '\n'.join(line for line in options.splitlines() if line.startswith(('controlpath ', 'localcommand ', 'permitlocalcommand ')))
            self.fail('Existing master integration failed: ' + (error.read_text() if error.exists() else 'no coordinator diagnostic') + '\n' + selected)
        self.assert_browser_completed()
        self.run_local(str(ROOT / 'bin/in'), 'stop')
        self.run_local('ssh', '-F', str(self.config), '-O', 'check', 'fixture')

    def test_existing_coordinator_refreshes_helper_settings(self):
        """Repeating setup updates a live coordinator instead of retaining its old helper path."""
        self.assertEqual(self.ssh('printf connected').stdout, 'connected')
        next_helper = self.root / 'updated-in'
        marker = self.root / 'updated-helper-used'
        self.write_executable('updated-in', '#!/bin/sh\n: >' + shlex.quote(str(marker)) +
                              '\nexec ' + shlex.quote(str(self.root / 'remote-in')) + ' "$@"\n')
        self.run_local(str(ROOT / 'bin/in'), 'setup', 'laptop', '--shell-file', str(self.rc),
                       '--ssh-config', str(self.config), '--remote', str(next_helper), 'fixture')
        port = self.free_port('127.0.0.2')
        self.assertEqual(self.ssh(shlex.quote(str(self.root / 'login')) + f' {port}').stdout.strip(),
                         'authenticated fixture')
        self.assertTrue(marker.exists(), 'coordinator kept its old helper settings')
        self.assert_browser_completed()

    def test_command_line_control_paths_join_only_the_selected_master(self):
        """Both SSH override forms route callbacks through their actual, test-owned master."""
        self.run_local(str(ROOT / 'bin/in'), 'uninstall')
        ordinary = self.root / 'ordinary-master'
        self.config.write_text(self.base_config + '\nHost fixture\n  ControlMaster auto\n  ControlPath ' + str(ordinary) + '\n  ControlPersist 600\n')
        self.run_local('ssh', '-F', str(self.config), '-N', '-f', 'fixture')
        self.run_local(str(ROOT / 'bin/in'), 'setup', 'laptop', '--shell-file', str(self.rc),
                       '--ssh-config', str(self.config), '--remote', str(self.root / 'remote-in'), 'fixture')
        try:
            for name, override in (('socket-option', '-S'), ('config-option', '-oControlPath=')):
                with self.subTest(override=name):
                    selected = self.root / name
                    args = [override, str(selected)] if override == '-S' else [override + str(selected)]
                    try:
                        port = self.free_port('127.0.0.2')
                        result = self.run_local('ssh', '-F', str(self.config), *args,
                                                '-oControlMaster=auto', '-oControlPersist=600', 'fixture',
                                                shlex.quote(str(self.root / 'login')) + f' {port}')
                        self.assertEqual(result.stdout.strip(), 'authenticated fixture')
                        self.assert_browser_completed()
                        self.run_local('ssh', '-F', str(self.config), '-S', str(selected), '-O', 'check', 'fixture')
                        self.run_local('ssh', '-F', str(self.config), '-S', str(ordinary), '-O', 'check', 'fixture')
                    finally:
                        subprocess.run(['ssh', '-F', str(self.config), '-S', str(selected), '-O', 'exit', 'fixture'],
                                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
                        (self.local / 'browser-result').unlink(missing_ok=True)
            self.run_local('ssh', '-F', str(self.config), '-S', str(ordinary), '-O', 'check', 'fixture')
        finally:
            subprocess.run(['ssh', '-F', str(self.config), '-S', str(ordinary), '-O', 'exit', 'fixture'],
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)

    def test_proxy_command_byte_stream_preserves_callback(self):
        """An authenticated SSH byte proxy can carry a target login and its callback."""
        proxy = self.root / 'byte-proxy'
        marker = self.root / 'proxy-used'
        self.write_executable('byte-proxy', '#!/bin/sh\n: >' + shlex.quote(str(marker)) +
                              '\nexec ssh -F ' + shlex.quote(str(self.config)) + ' -W "$1:$2" jump\n')
        self.config.write_text(self.config.read_text() + '\nHost jump\n  HostName 127.0.0.1\n  Port ' +
                               str(self.port) + '\n  User ' + os.environ.get('USER', 'runner') +
                               '\n  IdentityFile ' + str(self.root / 'client') +
                               '\n  IdentitiesOnly yes\n  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n  LogLevel ERROR\n' +
                               '\nHost fixture\n  ProxyCommand ' + str(proxy) + ' %h %p\n')
        port = self.free_port('127.0.0.2')
        result = self.ssh(shlex.quote(str(self.root / 'login')) + f' {port}')
        self.assertEqual(result.stdout.strip(), 'authenticated fixture')
        self.assertTrue(marker.exists(), 'SSH did not use the byte proxy')
        self.assert_browser_completed()

    def test_proxy_jump_preserves_callback(self):
        """A jump host's direct TCP channel carries the target login and callback."""
        self.config.write_text(self.config.read_text() + '\nHost jump\n  HostName 127.0.0.1\n  Port ' +
                               str(self.port) + '\n  User ' + os.environ.get('USER', 'runner') +
                               '\n  IdentityFile ' + str(self.root / 'client') +
                               '\n  IdentitiesOnly yes\n  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n  LogLevel ERROR\n' +
                               '\nHost fixture\n  ProxyJump jump\n')
        port = self.free_port('127.0.0.2')
        result = self.ssh(shlex.quote(str(self.root / 'login')) + f' {port}')
        self.assertEqual(result.stdout.strip(), 'authenticated fixture')
        self.assert_browser_completed()

    def test_missing_master_attach_never_uses_proxy_authentication(self):
        """Internal attach fails closed without invoking a configured authentication proxy."""
        marker = self.root / 'unexpected-proxy'
        proxy = self.root / 'auth-proxy'
        self.write_executable('auth-proxy', '#!/bin/sh\n: >' + shlex.quote(str(marker)) + '\nexit 1\n')
        config = self.root / 'missing-master.conf'
        config.write_text(self.base_config + '\nHost fixture\n  ProxyCommand ' + str(proxy) + '\n')
        fresh = self.root / 'fresh-laptop'
        self.run_local(str(ROOT / 'bin/in'), 'setup', 'laptop', '--shell-file', str(self.root / 'fresh.rc'),
                       '--ssh-config', str(config), '--remote', str(self.root / 'remote-in'), 'fixture',
                       env=dict(self.env, IN_HOME=str(fresh)))
        result = subprocess.run([str(ROOT / 'bin/in'), 'attach', '--host', 'fixture',
                                 '--control', str(self.root / 'absent-master'), '--config', str(config),
                                 '--remote', str(self.root / 'remote-in')], env=dict(self.env, IN_HOME=str(fresh)),
                                capture_output=True, text=True, timeout=8)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(marker.exists(), 'attach attempted fresh proxy authentication')
        self.assertIn('remote bridge unavailable', result.stderr)

    def test_tmux_routes_the_attached_session(self):
        """A tmux pane resolves its sole attached client's SSH route at request time."""
        if not shutil.which('tmux'): self.skipTest('tmux is not installed')
        name = 'in-' + self.root.name
        port = self.free_port('127.0.0.2')
        command = 'tmux -L ' + shlex.quote(name) + ' -f /dev/null new-session -s fixture ' + shlex.quote(str(self.root / 'login') + f' {port} late')
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 24, 80, 0, 0))
        proc = subprocess.Popen(['ssh', '-tt', '-F', str(self.config), 'fixture', command], stdin=slave, stdout=slave, stderr=slave, env=dict(self.env, TERM='xterm-256color'))
        os.close(slave)
        try:
            proc.wait(timeout=20)
            self.assertEqual(proc.returncode, 0)
            self.assert_browser_completed()
        finally:
            if proc.poll() is None: proc.kill(); proc.wait()
            os.close(master)
            subprocess.run(['tmux', '-L', name, 'kill-server'], capture_output=True)

    def test_remote_stop_works_at_relay_capacity(self):
        """A saturated relay must still accept its management stop request."""
        self.ssh('true')
        deadline = time.monotonic() + 3
        paths = []
        while not paths and time.monotonic() < deadline:
            paths = list(self.remote.glob('r-*.sock'))
            if not paths: time.sleep(.02)
        self.assertEqual(len(paths), 1)
        watchers = []
        try:
            for _ in range(32):
                conn = socket.socket(socket.AF_UNIX)
                watchers.append(conn)
                conn.settimeout(2)
                conn.connect(str(paths[0]))
                conn.sendall(b'{"version":3,"kind":"watch"}\n')
                self.assertIn(b'"ready"', conn.recv(512))
            self.run_local(str(ROOT / 'bin/in'), 'stop', env=dict(self.env, IN_HOME=str(self.remote)))
            for conn in watchers: self.assertEqual(conn.recv(1), b'')
            self.run_local('ssh', '-F', str(self.config), '-O', 'check', 'fixture')
        finally:
            for conn in watchers: conn.close()

    def test_file_transfer_survives_missing_helper(self):
        """Installing the hook cannot corrupt SSH file-transfer protocols."""
        if not Path('/usr/lib/openssh/sftp-server').exists(): self.skipTest('fixture sftp server unavailable')
        (self.root / 'remote-in').unlink()
        source = self.root / 'source'
        destination = self.root / 'destination'
        source.write_bytes(b'fixture file transfer\x00\n')
        self.run_local('scp', '-q', '-F', str(self.config), str(source), 'fixture:' + str(destination))
        self.assertEqual(destination.read_bytes(), source.read_bytes())

    def test_missing_remote_helper_preserves_ssh(self):
        """A failed bridge must not prevent ordinary remote commands from completing."""
        (self.root / 'remote-in').unlink()
        self.assertEqual(self.ssh('printf ordinary-command').stdout, 'ordinary-command')


if __name__ == '__main__':
    unittest.main()
