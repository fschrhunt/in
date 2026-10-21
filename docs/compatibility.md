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

`./x integration` now covers 21 cases and runs on Linux with actual OpenSSH 10.2p1, Mosh 1.4.0 and tmux
available in the development environment. It creates disposable fixture keys,
an isolated SSH configuration/server and synthetic browser/provider callbacks.
No personal SSH configuration, keys, CLI credentials or browser session is used.

Both ends run on the same kernel. A fixture adapter maps the remote listener to
127.0.0.2 while the laptop uses 127.0.0.1/::1, simulating separate loopbacks without
root or a container. The adapter is test code, not a runtime compatibility option.
This proves the transport and installed-hook path; it is not a substitute for
macOS/Linux testing on separate machines.

The 21 real SSH/Mosh integration cases cover late listener startup, unchanged POST callback/response bytes,
Mosh handoff, BROWSER and PATH-resolved xdg-open launches, a single attached tmux session, two laptops connecting to one
account, browser preconnects, detached openers, refusal of dev-server opens, occupied laptop ports, missing remote helpers,
existing SSH masters, rejection of unrelated listeners during late startup, cleanup and
unaffected file transfer, literal SSH control-socket overrides, real ProxyJump
and `ProxyCommand -W`, a Mosh shell surviving master replacement and logging in
after a fresh authenticated hook, and refusal to fall back to authentication
when the master is missing. Cancellation is also checked with an active silent
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
- A surviving Mosh shell regains its bridge only after a fresh authenticated
  hook binds the replacement master to its logical session. There is no unattended
  authentication or replay after an uncertain callback delivery.
- ProxyJump and `ProxyCommand -W` passed real SSH fixture tests. Provider-managed
  Cloudflare or Teleport MFA and hardware-key setups have not been live checked.
- Existing LocalCommand hooks, ambiguous multi-client tmux, wildcard callback
  listeners, background services that start listening only after the browser hook,
  hidden redirects, printed-only URLs and launchers bypassing both BROWSER and PATH xdg-open
  do not have a verified automatic path.
- Containers, nested SSH, account switching, local HTTPS and custom URI schemes
  are outside this version's verified scope.

The working default path is broader than a Wrangler-specific integration, but
these limits prevent describing it as universal remote authentication support.

## Native transport evidence

`./x prove --laptop mini --server server` passed on the new architecture. The
server banner confirmed Tailscale SSH; this was an actual SSH server path.
The native macOS Tailscale 1.104.2 `tailscale ssh` wrapper also passed the full
OAuth proof on a fresh connection with its own host-key verification. Native ET 7.0.0 on Linux passed an isolated
terminal test: after its TCP connection dropped and the SSH master exited, the
surviving terminal delivered a new fixture callback. Kitty 0.45.0 under Xvfb passed with its default connection sharing and a
late callback listener, using its own chosen SSH socket. `./x clients` is the
isolated native ET/Kitty proof and requires Linux, `et`, `etserver`, `etterminal`,
`kitty`, `kitten`, Xvfb, bubblewrap and the integration prerequisites. ET 7.0.0 on Apple Silicon macOS also completed the full fixture OAuth proof
against Linux. Intel macOS runtime remains unverified. Kitty was exercised on Linux; native Kitty on macOS remains unverified.

## Repeatable native OAuth proof

`./x prove --laptop mini --server server --browser` passed on an Apple Silicon
Mac and this Linux server. It completed isolated fixture OAuth exchanges for
dynamic ports, a late listener, cancellation and same-port retry, a separate
service listener through xdg-open, and an actual system-browser callback. Each
completed login rejected incorrect state, exchanged a PKCE-bound code, persisted
private credentials and authenticated from a fresh remote process. Stop released
callback ports while preserving SSH; temporary installations were removed.

See [testing](testing.md) to repeat the proof. This validates the shared login
transport and a controlled OAuth lifecycle; it does not replace real-provider
consent checks or prove that every CLI exposes a supported browser-launch hook.
