#!/usr/bin/env python3
"""Prove complete fixture logins across a real Mac/Linux pair using existing SSH."""

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


class Proof:
    """Own one disposable installation and report only fixed, non-secret outcomes."""

    def __init__(self, args, root):
        """Track resources before acquiring them so partial setup can be cleaned up."""
        self.args, self.root = args, root
        self.mac = None
        self.provider = None
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
        result = run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8",
                      "--", self.args.laptop, self.checked_command(command)], **kwargs)
        return self.command_result(result, required)

    def copy(self, source, name):
        """Copy a fixture asset into the validated, test-owned Mac directory."""
        run(["scp", "-q", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8",
             str(source), self.args.laptop + ":" + self.mac + "/" + name])

    def remote(self, command, *, required=True, **kwargs):
        """Use ordinary SSH with the temporary integration and inherited authentication."""
        result = self.laptop(self.prefix + " ssh -F " + Q(self.config) + " -- " +
                             Q(self.args.server) + " " + Q(self.checked_command(command)), **kwargs)
        return self.command_result(result, required)

    def cli(self, *args, **kwargs):
        """Run an ordinary fixture CLI after loading its isolated remote shell setup."""
        command = ". " + Q(str(self.root / "remote.rc")) + "; " + shlex.join(
            ["python3", str(self.root / "oauth.py"), *map(str, args)])
        return self.remote(command, **kwargs)

    def setup(self):
        """Build the current sources and install only into private temporary paths."""
        if sys.platform != "linux" or any(not shutil.which(tool) for tool in ("ssh", "scp", "go")):
            raise RuntimeError("run on Linux with Go, Python, ssh and scp")
        self.stage = "Mac prerequisites"
        platform = self.laptop("uname -s; uname -m; command -v python3").stdout.splitlines()
        if len(platform) != 3 or platform[0] != "Darwin" or platform[1] not in ("arm64", "x86_64"):
            raise RuntimeError("Mac with Python 3 required")
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
    parser.add_argument("--report", type=Path, help="write a JSON result (no URLs or credentials)")
    args = parser.parse_args()

    def interrupted(_signal, _frame):
        """Route graceful process termination through the same owned-resource cleanup."""
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupted)
    os.chdir(REPO)
    started = time.monotonic()
    report = {"schema": 1, "kind": "fixture-oauth", "system_browser": args.browser,
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
