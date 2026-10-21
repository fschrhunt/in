# Compatibility and validation

Observed 2026-10-21. These are separate levels of evidence. No completed provider
login has been performed for this version.

## Installed CLI launch probes

The probe uses temporary CLI configuration, a recorder BROWSER, and no consent
or token exchange. It records only URL shape, parameter names and callback
addresses, never authorization URLs, state values, codes or tokens.

| CLI | Installed version | Observed browser launch | Evidence |
| --- | --- | --- | --- |
| Wrangler | 4.151.2 | HTTPS authorization URL with redirect_uri to localhost:8976 | Actual BROWSER invocation; fixed endpoint parsed |
| Claude Code | 2.1.305 | HTTPS authorization URL with redirect_uri to a dynamically selected localhost port | Actual BROWSER invocation; dynamic endpoint parsed |
| rclone | 1.75.2 | Direct HTTP loopback URL on 127.0.0.1:53682 | Actual BROWSER invocation; direct endpoint parsed |

Run `./x compatibility` to repeat against installed versions. It starts login
commands in isolated app configuration and terminates them after observing the
browser request. This is optional and is not run in CI. Other versions may use
different launchers or flows.

Cloudflare's [callback source](https://github.com/cloudflare/workers-sdk/blob/main/packages/workers-auth/src/callback-server.ts)
confirms separate advertised and listener endpoints are possible; non-default
callback-host/port mappings are not inferred. rclone's
[OAuth source](https://github.com/rclone/rclone/blob/master/lib/oauthutil/oauthutil.go)
uses a loopback authorization page before provider navigation. Claude's
[authentication documentation](https://code.claude.com/docs/en/authentication)
describes its local callback and copy/paste fallback. in does not select these
fallbacks or change provider-specific authentication.

## Executed integration coverage

`./x integration` runs on Linux with actual OpenSSH 10.2p1, Mosh 1.4.0 and tmux
available in the development environment. It creates disposable fixture keys,
an isolated SSH configuration/server and synthetic browser/provider callbacks.
No personal SSH configuration, keys, CLI credentials or browser session is used.

Both ends run on the same kernel. A fixture adapter maps the remote listener to
127.0.0.2 while the laptop uses 127.0.0.1/::1, simulating separate loopbacks without
root or a container. The adapter is test code, not a runtime compatibility option.
This proves the transport and installed-hook path; it is not a substitute for
macOS/Linux testing on separate machines.

The suite covers late listener startup, unchanged POST callback/response bytes,
Mosh handoff, a single attached tmux session, two laptops connecting to one
account, browser preconnects, detached openers, occupied laptop ports, missing remote helpers,
existing SSH masters, rejection of unrelated same-account listeners, cleanup and
unaffected file transfer. Cancellation is also checked with an active silent
callback, including remote helper cleanup while the SSH master survives. Focused Go tests
cover URL boundaries, socket-table decoding, route cancellation, reversible
setup, partially edited ownership markers, PID reuse, startup/uninstall races
and concurrent edits. Remote stop is also exercised at full relay capacity. CI has a Linux integration job and Linux/macOS Go
checks. All four target binaries cross-build.

## Native macOS and real CLI callback checks

Native `./x check` passed on two Apple Silicon Macs using Go 1.27.3 and system
OpenSSH 10.3p1. Each connected to Linux through its existing SSH host configuration
and authentication, with a separate temporary control socket and reversible
application setup. No personal shell/SSH files were changed. One test used
`/usr/bin/open` and the actual browser: it displayed the page served by the Linux
callback listener. The second used a scripted HTTP opener.

Additional native checks used temporary CLI configuration and a scripted opener
on macOS. Wrangler's fixed callback and Claude Code's dynamic callback returned
HTTP 400 for deliberately invalid OAuth state; rclone's actual loopback
authorization page returned HTTP 307.
No provider redirect was followed and no credentials were exchanged. These checks
exercise real listeners, ownership discovery and the SSH data path; they do not
prove completed provider login. Temporary installations were stopped and removed.

## Remaining validation and explicit limits

- Completed remote Wrangler/Claude/rclone login after provider consent.
- Direct localhost browser requests can also be dev-tool opens. A login-only
  classification policy is not implemented; starting a server alone does nothing.
- Intel macOS runtime and older macOS system-SSH compatibility; no minimum
  OpenSSH version is claimed yet.
- Mosh transport recovery after network loss. The terminal can survive while the
  SSH bridge does not; automatic reconnect/rebinding is not implemented.
- SSH jump-host and interactive MFA/hardware-key setups need real runtime tests;
  the implementation delegates existing authentication to system SSH.
- Existing LocalCommand hooks, ambiguous multi-client tmux, wildcard callback
  listeners, sibling/detached listener processes, hidden redirects, printed-only URLs and launchers bypassing BROWSER
  do not have a verified automatic path.
- Containers, nested SSH, account switching, local HTTPS and custom URI schemes
  are outside this version's verified scope.

The working default path is broader than a Wrangler-specific integration, but
these limits prevent describing it as universal remote authentication support.
