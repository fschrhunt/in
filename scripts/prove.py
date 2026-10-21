#!/usr/bin/env python3
"""Prove complete fixture logins across a real Mac/Linux pair using SSH, ET or Tailscale."""

import argparse
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time

REPO = Path(__file__).resolve().parent.parent
Q = shlex.quote


def alias(value):
    """Accept a host alias safe to use in both SSH argv and an SSH config block."""
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", value):
        raise argparse.ArgumentTypeError("use an existing SSH host alias")
    return value


def run(argv, *, env=None, timeout=45, required=True):
    """Capture subprocess output privately; never print commands or raw failures."""
    result = subprocess.run(argv, env=env, capture_output=True, text=True, timeout=timeout)
    if required and result.returncode:
        raise RuntimeError("command failed")
    return result


def free_port():
    """Select a candidate remote callback port; the real bind still detects races."""
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def wait_file(path):
    """Wait briefly for an atomic local readiness file."""
    deadline = time.monotonic() + 5
    while not path.exists():
        if time.monotonic() >= deadline:
            raise RuntimeError("readiness timed out")
        time.sleep(0.05)


def tailscale_identity(ssh_config):
    """Extract a safe MagicDNS target and user from private ssh -G output."""
    values = {}
    for line in ssh_config.splitlines():
        key, _, value = line.partition(" ")
        if key in ("hostname", "user"):
            values[key] = value.strip()
    host = values.get("hostname", "").rstrip(".")
    user = values.get("user", "")
    if not re.fullmatch(r"(?:[A-Za-z0-9-]+\.)+ts\.net", host, re.IGNORECASE):
        raise RuntimeError("server alias must resolve to a MagicDNS .ts.net hostname")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+", user):
        raise RuntimeError("server alias must resolve to a simple SSH user")
    return host, user


class Proof:
    """Own one disposable installation and report only fixed, non-secret outcomes."""

    def __init__(self, args, root):
        """Track resources before acquiring them so partial setup can be cleaned up."""
        self.args, self.root = args, root
        self.mac = None
        self.provider = None
        self.etserver = None
        self.installed = False
        self.configured = False
        self.ports = set()
        self.stage = "prerequisites"
        self.results = []
        self.env = dict(os.environ, IN_HOME=str(root / "remote"))

    def passed(self, name):
        """Record a completed assertion, never an inferred or skipped success."""
        self.results.append(name)
        print("PASS " + name, flush=True)

    def checked_command(self, command):
        """Carry explicit results even when an SSH server loses the child exit status."""
        runner = ("import json,subprocess,sys; "
                  "p=subprocess.run(['/bin/sh','-c',sys.argv[1]],capture_output=True,text=True,timeout=40); "
                  "print(json.dumps({'status':p.returncode,'stdout':p.stdout,'stderr':p.stderr}))")
        return "python3 -c " + Q(runner) + " " + Q(command)

    def command_result(self, result, required):
        """Require a complete result envelope; never treat missing output as success."""
        record = json.loads(result.stdout)
        result.returncode = record["status"]
        result.stdout, result.stderr = record["stdout"], record["stderr"]
        if required and result.returncode:
            raise RuntimeError("remote command failed")
        return result

    def laptop(self, command, *, required=True, **kwargs):
        """Run a bounded command through the user's existing laptop SSH alias."""
        config = Path.home() / ".ssh/config"
        result = run(["ssh", *(["-F", str(config)] if config.is_file() else []),
                      "-o", "BatchMode=yes", "-o", "ConnectTimeout=8",
                      "--", self.args.laptop, self.checked_command(command)], **kwargs)
        return self.command_result(result, required)

    def copy(self, source, name):
        """Copy a fixture asset into the validated, test-owned Mac directory."""
        config = Path.home() / ".ssh/config"
        run(["scp", "-q", *(["-F", str(config)] if config.is_file() else []),
             "-o", "BatchMode=yes", "-o", "ConnectTimeout=8",
             str(source), self.args.laptop + ":" + self.mac + "/" + name])

    def remote(self, command, *, required=True, **kwargs):
        """Use the selected transport with the temporary integration and explicit results."""
        if self.args.client == "tailscale" and hasattr(self, "tailscale_target"):
            invocation = ("tailscale ssh " + Q(self.tailscale_target) + " -F " +
                          Q(self.config) + " " + Q(self.checked_command(command)))
        else:
            invocation = ("ssh -F " + Q(self.config) + " -- " + Q(self.args.server) +
                          " " + Q(self.checked_command(command)))
        result = self.laptop(self.prefix + " " + invocation, **kwargs)
        return self.command_result(result, required)

    def cli(self, *args, **kwargs):
        """Route ET login specially; use the selected remote transport for other commands."""
        command = ". " + Q(str(self.root / "remote.rc")) + "; " + shlex.join(
            ["python3", str(self.root / "oauth.py"), *map(str, args)])
        if self.args.client == "et" and args[0] == "login":
            return self.et_login(command, str(args[2]), **kwargs)
        return self.remote(command, **kwargs)

    def et_login(self, command, case, *, required=True):
        """Run a fixture login in an ET PTY and read its private Linux result files."""
        folder = self.root / case
        folder.mkdir(mode=0o700, exist_ok=True)
        output, status = folder / "result.json", folder / "status"
        release = folder / "release"
        output.unlink(missing_ok=True)
        status.unlink(missing_ok=True)
        release.unlink(missing_ok=True)
        remote_command = (command + " > " + Q(str(output)) + " 2>/dev/null; "
                          "code=$?; printf '%s' \"$code\" > " + Q(str(status)) +
                          "; timeout 35 sh -c " + Q("until test -f " + Q(str(release)) +
                          "; do sleep .05; done") + "; exit \"$code\"")
        invocation = (". " + Q(self.mac + "/local.rc") + "; et 127.0.0.1 --port " +
                      str(self.et_mac_port) + " --serverfifo " + Q(str(self.root / "et.fifo")) +
                      " --terminal-path " + Q(str(self.root / "etterminal")) +
                      " --silent --telemetry=false --command " + Q(remote_command))
        helper = ("import fcntl,os,pty,select,signal,struct,subprocess,sys,termios,time,json\n"
                  "master,slave=pty.openpty()\n"
                  "fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',24,80,0,0))\n"
                  "p=subprocess.Popen(['bash','-c',sys.argv[1]],stdin=slave,stdout=slave,stderr=slave,start_new_session=True)\n"
                  "os.close(slave)\n"
                  "deadline=time.monotonic()+30\n"
                  "released=False\n"
                  "while p.poll() is None and time.monotonic()<deadline:\n"
                  " if select.select([master],[],[],.1)[0]:\n"
                  "  try: os.read(master,65536)\n"
                  "  except OSError: pass\n"
                  " if not released and os.path.exists(sys.argv[2]):\n"
                  "  r=subprocess.run(['/usr/bin/ssh','-F',sys.argv[4],'--',sys.argv[5],"
                  "'touch '+sys.argv[3]],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=5)\n"
                  "  released=r.returncode==0\n"
                  "if p.poll() is None:\n"
                  " os.killpg(p.pid,signal.SIGTERM)\n"
                  " try: p.wait(timeout=2)\n"
                  " except subprocess.TimeoutExpired: os.killpg(p.pid,signal.SIGKILL); p.wait()\n"
                  "os.close(master)\n"
                  "print(json.dumps({'status':p.returncode}))\n")
        mac_command = ("PATH=" + Q(self.mac + "/tools") + ':"$PATH" ' + self.prefix +
                       " python3 -c " + Q(helper) + " " + shlex.join([
                           invocation, self.marker, str(release), self.config, self.args.server]))
        result = self.laptop(mac_command)
        if json.loads(result.stdout)["status"] != 0 or not status.exists():
            raise RuntimeError("ET fixture login failed")
        code = int(status.read_text())
        completed = subprocess.CompletedProcess([], code, output.read_text(), "")
        if required and code:
            raise RuntimeError("ET fixture login failed")
        return completed

    def setup(self):
        """Build the current sources and install only into private temporary paths."""
        if sys.platform != "linux" or any(not shutil.which(tool) for tool in ("ssh", "scp", "go")):
            raise RuntimeError("run on Linux with Go, Python, ssh and scp")
        self.stage = "Mac prerequisites"
        platform = self.laptop("uname -s; uname -m; command -v python3").stdout.splitlines()
        if len(platform) != 3 or platform[0] != "Darwin" or platform[1] not in ("arm64", "x86_64"):
            raise RuntimeError("Mac with Python 3 required")
        if self.args.client == "et":
            self.stage = "ET prerequisites"
            if any(not shutil.which(tool) for tool in ("etserver", "etterminal", "bwrap")):
                raise RuntimeError("ET server, terminal and bwrap required")
            version = self.laptop("command -v et >/dev/null && et --version").stdout
            match = re.search(r"\b(\d+)\.\d+", version)
            if not match or int(match.group(1)) < 7:
                raise RuntimeError("Mac ET version 7 or newer required")
        self.stage = "build current source"
        for name, target, arch in (("linux-in", "linux", None),
                                   ("mac-in", "darwin", {"arm64": "arm64", "x86_64": "amd64"}[platform[1]])):
            env = dict(os.environ, GOOS=target, CGO_ENABLED="0")
            if arch:
                env["GOARCH"] = arch
            else:
                env.pop("GOARCH", None)
            run(["go", "build", "-trimpath", "-o", str(self.root / name), "./cmd/in"], env=env, timeout=120)
        shutil.copyfile(REPO / "scripts/fixtures/oauth.py", self.root / "oauth.py")
        self.provider = subprocess.Popen([sys.executable, str(self.root / "oauth.py"),
                                          "provider", str(self.root)],
                                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        wait_file(self.root / "provider.json")
        self.stage = "isolated remote setup"
        self.installed = True
        run([str(self.root / "linux-in"), "setup", "remote", "--shell-file",
             str(self.root / "remote.rc")], env=self.env)
        wrapper = self.root / "remote-in"
        wrapper.write_text("#!/bin/sh\nIN_HOME=" + Q(str(self.root / "remote")) +
                           " exec " + Q(str(self.root / "remote/bin/in")) + ' "$@"\n')
        wrapper.chmod(0o700)
        self.stage = "isolated laptop setup"
        mac = self.laptop("mktemp -d /tmp/in-proof.XXXXXX").stdout.strip()
        if not re.fullmatch(r"/tmp/in-proof\.[A-Za-z0-9]+", mac):
            raise RuntimeError("unexpected temporary path")
        self.mac = mac
        self.config = mac + "/ssh.conf"
        self.marker = mac + "/browser-result.json"
        config = self.root / "ssh.conf"
        config.write_text("Host " + self.args.server + "\n  BatchMode yes\n  ConnectTimeout 8\n"
                          "  ControlMaster auto\n  ControlPath " + mac + "/master\n"
                          "  ControlPersist 60\nHost *\n  Include ~/.ssh/config\n")
        browser = self.root / "browser"
        browser.write_text("#!/bin/sh\nexec python3 " + Q(mac + "/oauth.py") + ' browser "$@"\n')
        browser.chmod(0o700)
        for path, name in ((config, "ssh.conf"), (browser, "browser"),
                           (self.root / "mac-in", "in"), (self.root / "oauth.py", "oauth.py")):
            self.copy(path, name)
        values = {"IN_HOME": mac + "/state", "IN_BROWSER_EXEC": mac + "/browser",
                  "IN_PROOF_SSH_CONFIG": self.config, "IN_PROOF_SERVER": self.args.server,
                  "IN_PROOF_REMOTE_FIXTURE": str(self.root / "oauth.py"),
                  "IN_PROOF_BROWSER_RESULT": self.marker}
        self.prefix = " ".join(key + "=" + Q(value) for key, value in values.items())
        self.stage = "server alias identity"
        self.remote("test -f " + Q(str(wrapper)))
        self.stage = "isolated laptop setup"
        self.configured = True
        self.laptop(self.prefix + " " + Q(mac + "/in") + " setup laptop --shell-file " +
                    Q(mac + "/local.rc") + " --ssh-config " + Q(self.config) +
                    " --remote " + Q(str(wrapper)) + " " + Q(self.args.server))
        self.passed("temporary setup with existing SSH authentication")

        if self.args.client == "et":
            self.setup_et()
        if self.args.client == "tailscale":
            self.setup_tailscale(config)

    def setup_tailscale(self, config):
        """Open a fresh native Tailscale SSH connection to the canonical MagicDNS host."""
        self.stage = "Tailscale CLI prerequisite"
        self.laptop("command -v tailscale >/dev/null")
        self.stage = "MagicDNS SSH identity"
        host, user = tailscale_identity(self.laptop(
            "ssh -F " + Q(self.config) + " -G " + Q(self.args.server)).stdout)
        canonical = host + "."
        stanza = ("Host " + canonical + "\n  User " + user + "\n  HostName " + canonical +
                  "\n  BatchMode yes\n  ConnectTimeout 8\n  ControlMaster auto\n  ControlPath " +
                  self.mac + "/master\n  ControlPersist 60\n")
        config.write_text(config.read_text().replace("Host *\n", stanza + "Host *\n", 1))
        self.copy(config, "ssh.conf")
        self.stage = "canonical laptop setup"
        self.laptop(self.prefix + " " + Q(self.mac + "/in") + " setup laptop --shell-file " +
                    Q(self.mac + "/local.rc") + " --ssh-config " + Q(self.config) +
                    " --remote " + Q(str(self.root / "remote-in")) + " " + Q(canonical))
        self.stage = "close original fixture SSH master"
        self.laptop("ssh -F " + Q(self.config) + " -O exit " + Q(self.args.server))
        self.tailscale_target = user + "@" + canonical
        self.stage = "native Tailscale SSH wrapper connection"
        if self.remote("printf tail-alive").stdout != "tail-alive":
            raise RuntimeError("native Tailscale SSH command failed")
        self.passed("native Tailscale SSH wrapper")

    def setup_et(self):
        """Start a private ET daemon and forward only its loopback test endpoint."""
        self.stage = "private ET fixture setup"
        self.laptop(self.prefix + " " + Q(self.mac + "/in") + " setup laptop --shell-file " +
                    Q(self.mac + "/local.rc") + " --ssh-config " + Q(self.config) +
                    " --remote " + Q(str(self.root / "remote-in")) + " 127.0.0.1")
        fake_home = self.root / "user"
        fake_home.mkdir(mode=0o700)
        for name in (".zshrc", ".bashrc", ".bash_profile"):
            (fake_home / name).write_text(". " + Q(str(self.root / "remote.rc")) + "\n")
        terminal = self.root / "etterminal"
        terminal.write_text("#!/bin/sh\nexec " + shlex.join([
            shutil.which("bwrap"), "--ro-bind", "/", "/", "--dev-bind", "/dev", "/dev",
            "--bind", str(fake_home), str(Path.home()), "--bind", str(self.root), str(self.root),
            "--", shutil.which("etterminal"), "--logdir", str(self.root)]) + ' "$@"\n')
        terminal.chmod(0o700)
        self.et_port = free_port()
        self.etserver = subprocess.Popen([
            shutil.which("etserver"), "--bindip", "127.0.0.1", "--port", str(self.et_port),
            "--serverfifo", str(self.root / "et.fifo"), "--pidfile", str(self.root / "et.pid"),
            "--logdir", str(self.root), "--telemetry=false"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + 5
        while not (self.root / "et.fifo").exists():
            if self.etserver.poll() is not None or time.monotonic() >= deadline:
                raise RuntimeError("private ET server did not become ready")
            time.sleep(0.05)
        # ET calls this Unix IPC socket a fifo; it is not a POSIX named pipe.
        if not (self.root / "et.fifo").is_socket() or self.etserver.poll() is not None:
            raise RuntimeError("private ET server did not become ready")
        tools = self.mac + "/tools"
        wrapper = self.root / "ssh-et"
        wrapper.write_text("#!/bin/sh\ncase \"$1\" in 127.0.0.1|*@127.0.0.1) "
                           "shift; set -- " + Q(self.args.server) + " \"$@\";; esac\n"
                           "exec /usr/bin/ssh -F " + Q(self.config) + ' "$@"\n')
        wrapper.chmod(0o700)
        self.laptop("mkdir -m 700 " + Q(tools))
        self.copy(wrapper, "tools/ssh")
        self.et_mac_port = int(self.laptop("python3 -c " + Q(
            "import socket; s=socket.socket(); s.bind(('127.0.0.1',0)); print(s.getsockname()[1]); s.close()")
            ).stdout.strip())
        self.laptop("ssh -F " + Q(self.config) + " -O forward -L " +
                    Q("127.0.0.1:" + str(self.et_mac_port) + ":127.0.0.1:" + str(self.et_port)) +
                    " " + Q(self.args.server))
        self.laptop(self.prefix + " " + Q(self.mac + "/in") + " stop")
        self.passed("ET cross-OS fixture endpoint transport uses SSH forward")

    def login(self, name, *flags):
        """Require state rejection, token exchange, private storage and fresh-process auth."""
        self.stage = name
        if self.cli("whoami", self.root, name, required=False).returncode == 0:
            raise RuntimeError("profile was already authenticated")
        self.laptop("rm -f " + Q(self.marker))
        result = self.cli("login", self.root, name, *flags)
        self.ports.add(int(json.loads(result.stdout)["callback_port"]))
        if self.cli("whoami", self.root, name).stdout != "authenticated fixture\n":
            raise RuntimeError("fresh authentication check failed")
        if (self.root / name / "auth.json").stat().st_mode & 0o777 != 0o600:
            raise RuntimeError("credential permissions failed")
        evidence = self.browser_result()
        if evidence != {"state_rejected": True, "system_browser": "--system-browser" in flags}:
            raise RuntimeError("browser assertion failed")
        self.passed(name)

    def browser_result(self):
        """Require the Mac browser's atomic evidence file within a bounded wait."""
        deadline = time.monotonic() + 5
        while True:
            marker = self.laptop("cat " + Q(self.marker), required=False)
            if marker.returncode == 0:
                return json.loads(marker.stdout)
            if time.monotonic() >= deadline:
                raise RuntimeError("browser evidence missing")
            time.sleep(0.1)

    def execute(self):
        """Exercise distinct login lifecycles over the normal installed browser hooks."""
        self.setup()
        self.login("dynamic-port")
        self.login("late-listener", "--late", "--port", str(free_port()))
        self.stage = "cancelled login"
        port = free_port()
        self.ports.add(port)
        self.laptop("rm -f " + Q(self.marker))
        self.cli("login", self.root, "cancelled", "--port", port, "--cancel")
        if self.browser_result() != {"cancelled": True}:
            raise RuntimeError("cancel request did not reach browser")
        if self.cli("whoami", self.root, "cancelled", required=False).returncode == 0:
            raise RuntimeError("cancelled profile authenticated")
        self.passed("cancelled login remains unauthenticated")
        self.login("same-port-retry", "--port", str(port), "--host", "localhost")
        self.login("service-listener-xdg-open", "--service", "--launcher", "xdg-open")
        if self.args.browser:
            self.login("system-browser", "--system-browser")
        self.stage = "stop preserves SSH and releases callback ports"
        self.laptop(self.prefix + " " + Q(self.mac + "/in") + " stop")
        self.laptop("ssh -F " + Q(self.config) + " -O check " + Q(self.args.server))
        if self.remote("printf ssh-alive").stdout != "ssh-alive":
            raise RuntimeError("SSH did not survive stop")
        # A refused connection proves the route closed without relying on TIME_WAIT binds.
        probe = ("import socket,sys\n"
                 "for port in " + repr(sorted(self.ports)) + ":\n"
                 " for family,address in ((socket.AF_INET,'127.0.0.1'),(socket.AF_INET6,'::1')):\n"
                 "  with socket.socket(family) as s:\n"
                 "   s.settimeout(1)\n"
                 "   if s.connect_ex((address,port)) == 0: sys.exit(1)\n")
        self.laptop("python3 -c " + Q(probe))
        self.passed(self.stage)

    def cleanup(self):
        """Attempt every owned-resource cleanup even after partial setup or failure."""
        failures = []

        def attempt(name, action):
            """Retain fixed cleanup labels while allowing subsequent cleanup to run."""
            try:
                action()
                return True
            except (Exception, KeyboardInterrupt):
                failures.append(name)
                return False

        if self.etserver:
            def stop_et():
                """Terminate and reap only this proof's ET daemon."""
                if self.etserver.poll() is None:
                    self.etserver.terminate()
                try:
                    self.etserver.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.etserver.kill()
                    self.etserver.wait()
                    raise RuntimeError("ET daemon required forceful shutdown")
            attempt("private ET server shutdown", stop_et)

        if self.mac:
            if self.configured:
                attempt("laptop uninstall", lambda: self.laptop(
                    self.prefix + " " + Q(self.mac + "/in") + " uninstall"))
            # This ControlPath belongs exclusively to this proof, never to the user.
            attempt("test SSH master", lambda: self.laptop(
                "if [ -S " + Q(self.mac + "/master") + " ]; then ssh -F " +
                Q(self.mac + "/ssh.conf") + " -O exit " + Q(self.args.server) + "; fi"))
            if not failures:
                if attempt("laptop temporary files", lambda: self.laptop("rm -rf -- " + Q(self.mac))):
                    self.mac = None
        if self.installed:
            attempt("remote uninstall", lambda: run([str(self.root / "linux-in"), "uninstall"], env=self.env))
        if self.provider:
            self.provider.terminate()
            try:
                self.provider.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.provider.kill()
                self.provider.wait()
                failures.append("fixture provider shutdown")
        return failures


def main():
    """Run a bounded native proof and optionally save a sanitized, versioned report."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--laptop", required=True, type=alias, help="SSH alias from this Linux server to the Mac")
    parser.add_argument("--server", required=True, type=alias, help="SSH alias from the Mac back to this server")
    parser.add_argument("--browser", action="store_true", help="also open a fixture callback in the Mac system browser")
    parser.add_argument("--client", choices=("ssh", "et", "tailscale"), default="ssh",
                        help="terminal client for fixture login (default: ssh)")
    parser.add_argument("--report", type=Path, help="write a JSON result (no URLs or credentials)")
    args = parser.parse_args()

    def interrupted(_signal, _frame):
        """Route graceful process termination through the same owned-resource cleanup."""
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupted)
    os.chdir(REPO)
    started = time.monotonic()
    report = {"schema": 1, "kind": "fixture-oauth", "client": args.client,
              "system_browser": args.browser,
              "commit": run(["git", "rev-parse", "HEAD"]).stdout.strip(),
              "dirty": bool(run(["git", "status", "--porcelain"]).stdout), "passed": False}
    root = Path(tempfile.mkdtemp(prefix="in-proof-", dir="/tmp"))
    proof = Proof(args, root)
    try:
        proof.execute()
        report["passed"] = True
    except (Exception, KeyboardInterrupt) as error:
        report["failed_stage"] = proof.stage
        reason = str(error) if type(error) is RuntimeError else type(error).__name__
        report["failure"] = reason
        print("FAIL " + proof.stage + ": " + reason, file=sys.stderr)
    finally:
        report["cleanup_failures"] = proof.cleanup()
        report["checks"] = proof.results
    if report["cleanup_failures"]:
        report["passed"] = False
        report["recovery_paths"] = {"server": str(root), "laptop": proof.mac}
        print("FAIL cleanup: " + ", ".join(report["cleanup_failures"]), file=sys.stderr)
        print("Recovery files retained: " + json.dumps(report["recovery_paths"]), file=sys.stderr)
    else:
        shutil.rmtree(root)
        print("PASS temporary resources removed", flush=True)
    report["seconds"] = round(time.monotonic() - started, 1)
    if args.report:
        args.report.write_text(json.dumps(report, indent=2) + "\n")
    if report["passed"]:
        print("Fixture OAuth proof passed; real-provider consent is a separate check.")
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
