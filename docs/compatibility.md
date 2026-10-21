# Compatibility and validation

Observed 2026-10-21. These are separate levels of evidence. No completed provider
login has been performed for this version. Compatibility follows the browser
launch and callback mechanism, not a registry of CLI names. The protocol fixtures
protect those shared behaviors; the real CLI probes below provide additional
evidence and are not required setup steps for users.

## Installed CLI launch probes

The probe uses temporary CLI configuration, a recorder BROWSER, and no consent
or token exchange. It records only URL shape, parameter names and callback
addresses, never authorization URLs, state values, codes or tokens.

| CLI | Installed version | Observed browser launch | Evidence |
| --- | --- | --- | --- |
| Wrangler | 4.151.2 | HTTPS authorization URL with redirect_uri to localhost:8976 | Actual BROWSER invocation; fixed endpoint parsed |
| Claude Code | 2.1.305 | HTTPS authorization URL with redirect_uri to a dynamically selected localhost port | Actual BROWSER invocation; dynamic endpoint parsed |
| rclone | 1.75.2 | Direct HTTP loopback URL on 127.0.0.1:53682 | Actual BROWSER invocation; verified rclone process and direct entry point required |
| Codex | 0.163.0 | HTTPS authorization URL with redirect_uri to 127.0.0.1:1455 | Actual BROWSER invocation in isolated configuration |
| OpenCode | 2.1.2 | ChatGPT browser method with redirect_uri to localhost:1455 | Actual BROWSER invocation through its launcher; interactive terminal and private server |

Run `./x compatibility` to repeat against installed versions. It starts login
commands in isolated app configuration and terminates them after observing the
browser request. This is optional and is not run in CI. Other versions may use
different launchers or flows. Codex and OpenCode probes use Linux bubblewrap to
hide the real home directory and isolate writable app paths; bubblewrap is a probe
dependency, not an in runtime dependency. OpenCode selects its browser method with
an isolated private server, and receives a pseudo terminal because its
[tagged login source](https://github.com/anomalyco/opencode/blob/v2.1.2/packages/cli/src/commands/handlers/auth/login.ts)
only attempts browser launch interactively. This does not cover every provider or
authentication method in OpenCode. Codex's
[tagged callback source](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/login/src/server.rs)
uses the actual private listener port, including its fallback port.

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
Mosh handoff, BROWSER and PATH-resolved xdg-open launches, a single attached tmux session, two laptops connecting to one
account, browser preconnects, detached openers, refusal of dev-server opens, occupied laptop ports, missing remote helpers,
existing SSH masters, rejection of unrelated listeners during late startup, cleanup and
unaffected file transfer. Cancellation is also checked with an active silent
callback, including remote helper cleanup while the SSH master survives. Focused Go tests
cover URL boundaries, socket-table decoding, route cancellation, reversible
setup, partially edited ownership markers, PID reuse, startup/uninstall races
socket pinning for background services and rejection of replacement listeners,
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
Codex 0.163.0 and OpenCode 2.1.2's ChatGPT browser method also returned HTTP 400
through the Mac bridge with isolated configuration. OpenCode passed with both a
private server and normal background-service mode; the latter used an isolated
service port to avoid the existing personal instance, and its test service was
stopped afterward. Running Codex and OpenCode consecutively
exercised cancellation and immediate port reuse across 127.0.0.1/localhost aliases.
No provider redirect was followed and no credentials were exchanged. These checks
exercise real listeners, ownership discovery and the SSH data path; they do not
prove completed provider login. Temporary installations were stopped and removed.

A further native check passed with the new interception and socket policy: a
foreground command invoked PATH-resolved xdg-open while a separate Linux service
process held the callback listener. The Mac's scripted browser received HTTP 200
through SSH. Native checks passed on that Apple Silicon Mac for this revision.
This is a synthetic OAuth request, not an OpenCode authentication claim.

## Remaining validation and explicit limits

- Completed remote provider login after consent for any of the observed CLIs.
- Only explicit OAuth authorization-code callbacks and the verified rclone direct
  entry point are forwarded. Unknown direct localhost opens are refused; dev
  servers retain their normal forwarding workflow.
- Intel macOS runtime and older macOS system-SSH compatibility; no minimum
  OpenSSH version is claimed yet.
- Mosh transport recovery after network loss. The terminal can survive while the
  SSH bridge does not; automatic reconnect/rebinding is not implemented.
- SSH jump-host and interactive MFA/hardware-key setups need real runtime tests;
  the implementation delegates existing authentication to system SSH.
- Existing LocalCommand hooks, ambiguous multi-client tmux, wildcard callback
  listeners, background services that start listening only after the browser hook,
  hidden redirects, printed-only URLs and launchers bypassing both BROWSER and PATH xdg-open
  do not have a verified automatic path.
- Containers, nested SSH, account switching, local HTTPS and custom URI schemes
  are outside this version's verified scope.

The working default path is broader than a Wrangler-specific integration, but
these limits prevent describing it as universal remote authentication support.
