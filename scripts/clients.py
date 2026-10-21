#!/usr/bin/env python3
"""Exercise real ET and Kitty clients with disposable SSH and isolated remote homes."""

import fcntl
import os
from pathlib import Path
import pty
import select
import shlex
import shutil
import socket
import struct
import subprocess
import sys
import termios
import threading
import time
import unittest

import integration
ROOT = integration.ROOT

Q = shlex.quote


class ReconnectingProxy:
    """Drop only a fixture's ET TCP connection while allowing its next reconnect."""

    def __init__(self, port):
        """Start a loopback-only proxy to one test-owned ET daemon."""
        self.port = port
        self.lock = threading.Lock()
        self.connections = set()
        self.stopped = threading.Event()
        self.listener = socket.socket()
        self.listener.bind(('127.0.0.1', 0))
        self.listener.listen()
        self.listener.settimeout(.1)
        self.thread = threading.Thread(target=self.accept, daemon=True)
        self.thread.start()

    def accept(self):
        """Pair connections until the fixture shuts down."""
        while not self.stopped.is_set():
            client = None
            try:
                client, _ = self.listener.accept()
                server = socket.create_connection(('127.0.0.1', self.port), timeout=2)
                server.settimeout(None)
            except socket.timeout:
                continue
            except OSError:
                if client is not None:
                    client.close()
                return
            with self.lock:
                self.connections.update((client, server))
            threading.Thread(target=self.pump, args=(client, server), daemon=True).start()
            threading.Thread(target=self.pump, args=(server, client), daemon=True).start()

    def pump(self, source, destination):
        """Carry opaque ET bytes without decoding or storing its authentication data."""
        try:
            while data := source.recv(65536):
                destination.sendall(data)
        except OSError:
            pass
        finally:
            for connection in (source, destination):
                try:
                    connection.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass
                connection.close()
            with self.lock:
                self.connections.difference_update((source, destination))

    def disconnect(self):
        """Simulate a network loss without stopping the daemon or terminal shell."""
        with self.lock:
            for connection in list(self.connections):
                try:
                    connection.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass

    def close(self):
        """Stop this test's proxy and wait for its accept loop."""
        self.stopped.set()
        self.disconnect()
        self.listener.close()
        self.thread.join(timeout=2)


class ClientIntegration(unittest.TestCase):
    """Use real clients while hiding the user's home from remote bootstraps."""

    def setUp(self):
        """Reuse the disposable SSH fixture without rerunning its independent tests."""
        self.fixture = integration.LoginIntegration(methodName='runTest')
        self.addCleanup(self.fixture.doCleanups)
        self.fixture.setUp()
        f = self.fixture
        fake_home = f.root / 'user'
        fake_home.mkdir()
        self.fake_home = fake_home
        self.session_script = (f.root / 'session').read_text()
        for rc in ('.zshrc', '.bashrc', '.bash_profile'):
            (fake_home / rc).write_text('. ' + Q(str(f.remote_rc)) + '\n')
        # HOME is inherited; a mount hides its contents rather than changing its meaning.
        sandbox = ['bwrap', '--unshare-net', '--ro-bind', '/', '/', '--dev-bind', '/dev', '/dev',
                   '--bind', str(fake_home), str(Path.home()), '--bind', str(f.root), str(f.root),
                   '--chdir', '/tmp', '--', '/bin/sh', '-c']
        f.write_executable('session', '#!/bin/sh\nexport IN_HOME=' + Q(str(f.remote)) +
                           '\n. ' + Q(str(f.remote_rc)) + '\nexec ' + shlex.join(sandbox) +
                           ' "$SSH_ORIGINAL_COMMAND"\n')

    def wait_path(self, path, process, timeout=15):
        """Require a fixture marker while refusing early client failure."""
        deadline = time.monotonic() + timeout
        while not path.exists():
            if process.poll() is not None:
                self.fail('client exited before the fixture became ready')
            if time.monotonic() > deadline:
                self.fail('client readiness timed out')
            time.sleep(.05)

    def end_process(self, process):
        """Reap one test-owned process without signalling other client sessions."""
        if process.poll() is None:
            process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()

    def test_et_reconnect_uses_its_own_session_tunnel(self):
        """A surviving ET shell logs in after both its SSH master and ET TCP connection die."""
        f = self.fixture
        native_login = (f.root / 'login').read_text().replace("127.0.0.2", "127.0.0.1")
        f.write_executable('login', native_login)
        f.run_local(str(ROOT / 'bin/in'), 'setup', 'laptop', '--shell-file', str(f.rc),
                    '--ssh-config', str(f.config), '--remote', str(f.root / 'remote-in'), '127.0.0.1')
        et_port = f.free_port('127.0.0.1')
        fifo = str(f.root / 'et.fifo')
        server = subprocess.Popen(['etserver', '--bindip', '127.0.0.1', '--port', str(et_port),
                                   '--serverfifo', fifo, '--pidfile', str(f.root / 'et.pid'),
                                   '--logdir', str(f.root), '--telemetry=false'],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.addCleanup(self.end_process, server)
        self.wait_path(Path(fifo), server)
        proxy = ReconnectingProxy(et_port)
        self.addCleanup(proxy.close)
        # ET uses system SSH; the fixture wrapper selects an isolated SSH config.
        tools = f.root / 'tools'
        tools.mkdir()
        ssh = tools / 'ssh'
        ssh.write_text('#!/bin/sh\ncase "$1" in 127.0.0.1|*@127.0.0.1) shift; set -- fixture "$@";; esac\nexec /usr/bin/ssh -F ' + Q(str(f.config)) + ' "$@"\n')
        ssh.chmod(0o700)
        env = dict(f.env, PATH=str(tools) + ':' + os.environ['PATH'],
                   TERM='xterm-256color', LC_ALL='C.UTF-8')
        callback_port = f.free_port('127.0.0.2')
        ready, complete = (f.root / name for name in ('ready', 'complete'))
        command = ('printf ready > ' + Q(str(ready)) + '; read -r proof_confirmation; ' +
                   Q(str(f.root / 'login')) + ' ' + str(callback_port) +
                   ' late && printf complete > ' + Q(str(complete)))
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 24, 80, 0, 0))
        invocation = '. ' + Q(str(f.rc)) + '; et 127.0.0.1 --port ' + str(proxy.listener.getsockname()[1]) + \
                     ' --serverfifo ' + Q(fifo) + ' --terminal-path ' + Q(shutil.which('etterminal') + ' --logdir ' + str(f.root)) + \
                     ' --silent --telemetry=false --command ' + Q(command)
        process = subprocess.Popen(['bash', '-c', invocation], env=env, stdin=slave, stdout=slave, stderr=slave)
        os.close(slave)
        self.addCleanup(os.close, master)
        self.addCleanup(self.end_process, process)
        self.wait_path(ready, process)
        f.run_local('ssh', '-F', str(f.config), '-O', 'exit', 'fixture')
        proxy.disconnect()
        # The shell can consume this input only after ET has restored its terminal stream.
        os.write(master, b'continue\n')
        self.wait_path(complete, process, timeout=25)
        process.wait(timeout=10)
        self.assertEqual(process.returncode, 0, 'ET fixture command failed')
        f.assert_browser_completed()

    def test_kitty_keeps_default_connection_sharing(self):
        """An actual kitten SSH session uses Kitty's chosen socket and completes the callback."""
        f = self.fixture
        # All Kitty SSH channels share one user namespace, preserving /proc FD ownership checks.
        # Separate per-command sandboxes would incorrectly hide sibling callback descriptors.
        (f.root / 'session').write_text(self.session_script)
        f.server.terminate()
        f.server.wait(timeout=5)
        sandbox = ['bwrap', '--die-with-parent', '--ro-bind', '/', '/', '--dev-bind', '/dev', '/dev',
                   '--bind', str(self.fake_home), str(Path.home()), '--bind', str(f.root), str(f.root),
                   '--', shutil.which('sshd') or '/usr/sbin/sshd', '-D', '-e', '-f', str(f.root / 'sshd.conf')]
        f.server = subprocess.Popen(sandbox, stderr=f.log)
        deadline = time.monotonic() + 5
        while True:
            try:
                with socket.create_connection(('127.0.0.1', f.port), timeout=.1):
                    break
            except OSError:
                if time.monotonic() >= deadline:
                    self.fail('isolated SSH server did not start')
                time.sleep(.03)
        display_read, display_write = os.pipe()
        xvfb = subprocess.Popen(['Xvfb', '-displayfd', str(display_write), '-screen', '0', '800x600x24',
                                 '-nolisten', 'tcp'], pass_fds=(display_write,),
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        os.close(display_write)
        self.addCleanup(self.end_process, xvfb)
        try:
            if not select.select([display_read], [], [], 5)[0]:
                self.fail('isolated display did not start')
            display = os.read(display_read, 32).decode().strip()
        finally:
            os.close(display_read)
        config = f.root / 'kitty'
        config.mkdir()
        (config / 'ssh.conf').write_text('remote_kitty no\n')
        env = dict(f.env, DISPLAY=':' + display, KITTY_CONFIG_DIRECTORY=str(config),
                   XDG_CACHE_HOME=str(f.root / 'cache'), LIBGL_ALWAYS_SOFTWARE='1')
        callback_port = f.free_port('127.0.0.2')
        complete = f.root / 'complete'
        command = Q(str(f.root / 'login')) + ' ' + str(callback_port) + ' late && printf done > ' + Q(str(complete))
        self.client_log = f.root / 'kitty-output'
        log = open(self.client_log, 'w')
        self.addCleanup(log.close)
        kitty = subprocess.Popen(['kitty', '--hold', '--config', 'NONE',
                                  '--override', 'confirm_os_window_close=0',
                                  '--override', 'linux_display_server=x11',
                                  'kitten', 'ssh', '-F', str(f.config), 'fixture', command], env=env,
                                 stdout=log, stderr=log)
        self.addCleanup(self.end_process, kitty)
        self.wait_path(complete, kitty, timeout=30)
        f.assert_browser_completed()


if __name__ == '__main__':
    missing = [name for name in ('et', 'etserver', 'etterminal', 'kitty', 'kitten', 'Xvfb', 'bwrap')
               if not shutil.which(name)]
    if missing:
        sys.exit('client proof requires: ' + ', '.join(missing))
    unittest.main()
