#!/usr/bin/env python3
"""Run a private, credential-free OAuth acceptance fixture for in."""

import argparse
import base64
import hashlib
import http.client
import json
import os
import select
import secrets
import signal
import subprocess
import sys
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


IDENTITY = "authenticated fixture"


def atomic_json(path, value):
    """Replace a fixture JSON file atomically with owner-only permissions."""
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    temp = path.with_name(path.name + "." + secrets.token_hex(8))
    try:
        fd = os.open(str(temp), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as stream:
            json.dump(value, stream, separators=(",", ":"))
            stream.write("\n")
        os.replace(str(temp), str(path))
    finally:
        if temp.exists():
            temp.unlink()


def case_path(root, case):
    """Locate one fixture case without allowing paths outside its root."""
    if not case or not all(c.isalnum() or c in "-_" for c in case) or not case.isascii():
        raise ValueError("invalid case")
    return Path(root) / case


def request(port, method, path, body=None, headers=None):
    """Make one bounded loopback HTTP request without following redirects."""
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    try:
        conn.request(method, path, body=body, headers=headers or {})
        response = conn.getresponse()
        return response.status, dict(response.getheaders()), response.read()
    finally:
        conn.close()


def form_body(values):
    """Encode OAuth form fields as ASCII bytes."""
    return urllib.parse.urlencode(values).encode("ascii")


def fields(query):
    """Parse a query or form while rejecting duplicate and empty values."""
    parsed = urllib.parse.parse_qs(query, keep_blank_values=True, strict_parsing=True)
    if any(len(values) != 1 or not values[0] for values in parsed.values()):
        raise ValueError("invalid fields")
    return {key: values[0] for key, values in parsed.items()}


def callback_parts(url):
    """Accept only an explicit local HTTP callback port."""
    parsed = urllib.parse.urlsplit(url)
    if (parsed.scheme != "http" or parsed.hostname not in ("127.0.0.1", "localhost")
            or parsed.username or parsed.password or not parsed.port or parsed.fragment
            or parsed.path != "/callback"):
        raise ValueError("invalid callback")
    return parsed


def provider(root):
    """Serve a private one-use OAuth issuer until SIGTERM."""
    codes = {}
    tokens = set()

    class Handler(BaseHTTPRequestHandler):
        """Handle the fixture authorization, token, and identity endpoints."""

        def log_message(self, _format, *args):
            """Suppress HTTP logs, which may contain secrets."""

        def reply(self, status, data=b"", content_type="text/plain"):
            """Send a small response without including request data."""
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            """Issue one-use codes or check an opaque bearer token."""
            parsed = urllib.parse.urlsplit(self.path)
            if parsed.path == "/me":
                token = self.headers.get("Authorization", "")
                if token.startswith("Bearer ") and token[7:] in tokens:
                    self.reply(200, IDENTITY.encode("ascii"))
                else:
                    self.reply(401)
                return
            if parsed.path != "/authorize":
                self.reply(404)
                return
            try:
                params = fields(parsed.query)
                redirect = callback_parts(params["redirect_uri"])
                if (params["response_type"] != "code" or params["client_id"] != "in-proof"
                        or params["code_challenge_method"] != "S256"
                        or not params["state"] or not params["case"]
                        or len(params["code_challenge"]) != 43 or redirect.query):
                    raise ValueError("invalid authorization")
                code = secrets.token_urlsafe(32)
                codes[code] = (params["code_challenge"], params["redirect_uri"])
                query = urllib.parse.urlencode({"code": code, "state": params["state"]})
                location = urllib.parse.urlunsplit((redirect.scheme, redirect.netloc,
                                                    redirect.path, query, ""))
                self.send_response(302)
                self.send_header("Location", location)
                self.send_header("Content-Length", "0")
                self.end_headers()
            except (KeyError, ValueError):
                self.reply(400)

        def do_POST(self):
            """Exchange each authorization code once after checking PKCE."""
            if self.path != "/token":
                self.reply(404)
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if length < 1 or length > 8192:
                    raise ValueError("invalid length")
                params = fields(self.rfile.read(length).decode("ascii"))
                if params["grant_type"] != "authorization_code":
                    raise ValueError("invalid grant")
                record = codes.pop(params["code"])
                challenge = base64.urlsafe_b64encode(hashlib.sha256(
                    params["code_verifier"].encode("ascii")).digest()).rstrip(b"=").decode("ascii")
                if record != (challenge, params["redirect_uri"]):
                    raise ValueError("invalid proof")
                token = secrets.token_urlsafe(32)
                tokens.add(token)
                self.reply(200, json.dumps({"access_token": token, "token_type": "Bearer"}).encode(),
                           "application/json")
            except (KeyError, ValueError, UnicodeError):
                self.reply(400)

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    atomic_json(Path(root) / "provider.json", {"port": server.server_port})

    def stop(_signal, _frame):
        """End the provider loop on termination."""
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, stop)
    try:
        server.serve_forever(poll_interval=0.2)
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


def callback_server(root, case, port, state, verifier, host, issuer_port):
    """Create the private callback listener and exchange a verified code once."""
    completed = []

    class Handler(BaseHTTPRequestHandler):
        """Reject bad state and persist credentials only after token exchange."""

        def log_message(self, _format, *args):
            """Suppress callback URL logging."""

        def do_GET(self):
            """Handle a single valid callback and serve a tiny success page."""
            try:
                parsed = urllib.parse.urlsplit(self.path)
                params = fields(parsed.query)
                if parsed.path != "/callback" or params["state"] != state or not params["code"]:
                    raise ValueError("invalid callback")
                redirect = "http://{}:{}/callback".format(host, self.server.server_port)
                body = form_body({"grant_type": "authorization_code", "code": params["code"],
                                  "code_verifier": verifier, "redirect_uri": redirect})
                status, _, response = request(issuer_port, "POST", "/token", body,
                                              {"Content-Type": "application/x-www-form-urlencoded"})
                if status != 200:
                    raise ValueError("exchange failed")
                token = json.loads(response)["access_token"]
                atomic_json(case_path(root, case) / "auth.json",
                            {"issuer_port": issuer_port, "access_token": token})
                page = b"<html><body>Login complete.</body></html>"
                self.send_response(200)
                self.send_header("Content-Type", "text/html")
                self.send_header("Content-Length", str(len(page)))
                self.end_headers()
                self.wfile.write(page)
                completed.append(True)
            except (KeyError, ValueError, OSError, UnicodeError):
                self.send_response(400)
                self.send_header("Content-Length", "0")
                self.end_headers()

    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    server.timeout = 0.2
    return server, completed


def wait_callback(server, completed, deadline, hook=None):
    """Serve callback requests until success or the login deadline."""
    while time.monotonic() < deadline and not completed:
        server.handle_request()
        if hook is not None and hook.poll() not in (None, 0):
            raise ValueError("browser failed")
    if not completed:
        raise TimeoutError("callback timeout")


def launch(command, url):
    """Launch the requested browser hook with its unmodified authorization URL."""
    hook = subprocess.Popen(command + [url], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return hook


def listener_port(child, deadline):
    """Read child readiness with the same finite deadline as the login."""
    remaining = max(0, deadline - time.monotonic())
    if not select.select([child.stdout], [], [], remaining)[0]:
        raise TimeoutError("listener timeout")
    return int(json.loads(child.stdout.readline())["port"])


def finish_hook(hook, deadline):
    """Require browser-hook completion within the login deadline."""
    if hook is None:
        return
    remaining = max(0.01, deadline - time.monotonic())
    if hook.wait(timeout=remaining) != 0:
        raise ValueError("browser failed")


def login(args):
    """Run a fresh OAuth CLI login, optionally with a separate listener child."""
    auth_path = case_path(args.root, args.case) / "auth.json"
    if auth_path.exists():
        auth_path.unlink()
    if args.late and not args.port:
        raise ValueError("late requires port")
    issuer_port = int(json.loads((Path(args.root) / "provider.json").read_text())["port"])
    state = secrets.token_urlsafe(32)
    verifier = secrets.token_urlsafe(32)
    challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
    deadline = time.monotonic() + 20
    server = None
    child = None
    hook = None
    try:
        if args.service and not args.late:
            payload = {"root": args.root, "case": args.case, "port": args.port, "state": state,
                       "verifier": verifier, "issuer_port": issuer_port, "host": args.host}
            child = subprocess.Popen([sys.executable, __file__, "_listener"], stdin=subprocess.PIPE,
                                     stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
            child.stdin.write(json.dumps(payload) + "\n")
            child.stdin.close()
            callback_port = listener_port(child, deadline)
        elif not args.late:
            server, completed = callback_server(args.root, args.case, args.port, state, verifier,
                                                 args.host, issuer_port)
            callback_port = server.server_port
        else:
            callback_port = args.port
        redirect = "http://{}:{}/callback".format(args.host, callback_port)
        params = {"response_type": "code", "client_id": "in-proof", "redirect_uri": redirect,
                  "state": state, "code_challenge": challenge, "code_challenge_method": "S256",
                  "case": args.case}
        if args.cancel:
            params["cancel"] = "1"
        if args.system_browser:
            params["system_browser"] = "1"
        url = "https://provider.invalid:{}/authorize?{}".format(issuer_port,
                                                                  urllib.parse.urlencode(params))
        command = [os.environ["BROWSER"]] if args.launcher == "browser" else ["xdg-open"]
        hook = launch(command, url)
        if args.late:
            finish_hook(hook, deadline)
            hook = None
            if not args.cancel:
                if args.service:
                    payload = {"root": args.root, "case": args.case, "port": args.port,
                               "state": state, "verifier": verifier,
                               "issuer_port": issuer_port, "host": args.host}
                    child = subprocess.Popen([sys.executable, __file__, "_listener"],
                                             stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                             stderr=subprocess.DEVNULL, text=True)
                    child.stdin.write(json.dumps(payload) + "\n")
                    child.stdin.close()
                    if listener_port(child, deadline) != callback_port:
                        raise ValueError("listener port changed")
                else:
                    server, completed = callback_server(args.root, args.case, callback_port,
                                                         state, verifier, args.host, issuer_port)
        if args.cancel:
            finish_hook(hook, deadline)
            return
        if args.service:
            finish_hook(hook, deadline)
            hook = None
            if child.wait(timeout=max(0.01, deadline - time.monotonic())) != 0:
                raise ValueError("listener failed")
        else:
            wait_callback(server, completed, deadline, hook)
            finish_hook(hook, deadline)
            hook = None
        print(json.dumps({"callback_port": callback_port}, separators=(",", ":")))
    finally:
        if hook is not None:
            if hook.poll() is None:
                hook.terminate()
            try:
                hook.wait(timeout=1)
            except subprocess.TimeoutExpired:
                hook.kill()
                hook.wait()
        if server is not None:
            server.server_close()
        if child is not None:
            if child.poll() is None:
                child.terminate()
            try:
                child.wait(timeout=1)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()
            child.stdout.close()


def listener_child():
    """Receive private login proof over stdin and own a callback listener."""
    payload = json.loads(sys.stdin.readline())
    server, completed = callback_server(payload["root"], payload["case"], payload["port"],
                                        payload["state"], payload["verifier"], payload["host"],
                                        payload["issuer_port"])
    try:
        print(json.dumps({"port": server.server_port}), flush=True)
        wait_callback(server, completed, time.monotonic() + 20)
    finally:
        server.server_close()


def authorize():
    """Fetch a provider redirect over loopback without relaying the callback."""
    url = sys.stdin.readline().rstrip("\n")
    parsed = urllib.parse.urlsplit(url)
    if (parsed.scheme != "https" or parsed.hostname != "provider.invalid"
            or parsed.username or parsed.password or parsed.path != "/authorize"
            or parsed.fragment or not parsed.port or parsed.port <= 1023):
        raise ValueError("invalid issuer URL")
    status, headers, _ = request(parsed.port, "GET", parsed.path + "?" + parsed.query)
    if status != 302 or "Location" not in headers:
        raise ValueError("authorization failed")
    print(headers["Location"])


def browser(url):
    """Act as a laptop browser while obtaining only provider access by SSH."""
    parsed = urllib.parse.urlsplit(url)
    params = fields(parsed.query)
    if params.get("cancel") == "1":
        atomic_json(Path(os.environ["IN_PROOF_BROWSER_RESULT"]), {"cancelled": True})
        return
    command = ["ssh", "-F", os.environ["IN_PROOF_SSH_CONFIG"], "--",
               os.environ["IN_PROOF_SERVER"], "python3", os.environ["IN_PROOF_REMOTE_FIXTURE"],
               "authorize"]
    result = subprocess.run(command, input=url + "\n", text=True, stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, timeout=20, check=True)
    callback = result.stdout.strip()
    target = urllib.parse.urlsplit(callback)
    if target.scheme != "http" or target.hostname not in ("127.0.0.1", "localhost") or not target.port:
        raise ValueError("invalid provider redirect")
    values = fields(target.query)
    values["state"] = secrets.token_urlsafe(32)
    tampered = target.path + "?" + urllib.parse.urlencode(values)
    status, _, _ = request(target.port, "GET", tampered)
    if status != 400:
        raise ValueError("state accepted")
    if params.get("system_browser") == "1":
        subprocess.run(["/usr/bin/open", callback], stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL, timeout=5, check=True)
    else:
        status, _, _ = request(target.port, "GET", target.path + "?" + target.query)
        if status != 200:
            raise ValueError("callback failed")
    marker = os.environ.get("IN_PROOF_BROWSER_RESULT")
    if marker:
        atomic_json(Path(marker), {"state_rejected": True,
                                   "system_browser": params.get("system_browser") == "1"})


def whoami(root, case):
    """Verify saved fixture credentials against the live issuer in a fresh process."""
    record = json.loads((case_path(root, case) / "auth.json").read_text())
    status, _, body = request(record["issuer_port"], "GET", "/me", headers={
        "Authorization": "Bearer " + record["access_token"]})
    if status != 200 or body.decode("ascii") != IDENTITY:
        raise ValueError("unauthenticated")
    print(IDENTITY)


def main():
    """Dispatch the public fixture modes and private child-listener mode."""
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="mode", required=True)
    for mode in ("provider", "whoami", "login"):
        command = sub.add_parser(mode)
        command.add_argument("root")
        if mode != "provider":
            command.add_argument("case")
        if mode == "login":
            command.add_argument("--port", type=int, default=0)
            command.add_argument("--host", choices=("127.0.0.1", "localhost"), default="127.0.0.1")
            command.add_argument("--late", action="store_true")
            command.add_argument("--service", action="store_true")
            command.add_argument("--launcher", choices=("browser", "xdg-open"), default="browser")
            command.add_argument("--cancel", action="store_true")
            command.add_argument("--system-browser", action="store_true")
    sub.add_parser("authorize")
    browser_parser = sub.add_parser("browser")
    browser_parser.add_argument("url")
    sub.add_parser("_listener")
    args = parser.parse_args()

    def interrupted(_signal, _frame):
        """Let fixture children and listeners be reaped on graceful termination."""
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupted)
    if args.mode == "provider":
        provider(args.root)
    elif args.mode == "login":
        login(args)
    elif args.mode == "whoami":
        whoami(args.root, args.case)
    elif args.mode == "authorize":
        authorize()
    elif args.mode == "browser":
        browser(args.url)
    else:
        listener_child()


if __name__ == "__main__":
    try:
        main()
    except (Exception, KeyboardInterrupt):
        print("fixture failed", file=sys.stderr)
        sys.exit(1)
