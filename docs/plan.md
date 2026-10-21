# Implementation plan

Status (2026-10-21): SSH and ET share callback admission and stream handling.
Twenty-one real SSH/Mosh integration cases pass, including fresh-master recovery for
a surviving Mosh shell. Native ET 7.0.0 on Linux passed a TCP-drop and SSH-master-exit
fixture callback; Kitty 0.45.0 under Xvfb also passed with default connection sharing. The repeatable
[OAuth proof](testing.md) passes on a Mac/Linux pair over a confirmed Tailscale
SSH server, including credential persistence and fresh-process authentication
against a controlled provider. Five CLI launch paths were observed. Completed
provider consent logins and Intel macOS runtime remain unverified. ET 7.0.0 on
Apple Silicon macOS and the native `tailscale ssh` wrapper also passed the full
fixture OAuth proof.
Current behavior is documented in [architecture](architecture.md),
[usage](usage.md) and [compatibility](compatibility.md).
See the [architecture audit](architecture-audit.md) for evidence and corrections.

The remaining sections preserve the original research plan and hypotheses as
historical context. Statements there about candidate designs, missing recovery
or pending SSH jump-host tests do not describe the current implementation.

## Product contract

Install once on a macOS laptop and Linux server. Continue using ordinary SSH or
Mosh and the CLI's normal commands. When a remote CLI requests a browser, in
opens the laptop browser and routes an observable loopback callback to the
original remote listener. The CLI owns OAuth, user consent and credential storage.
No daily wrapper, manual tunnel, new SSH identity or device-flow flag.
Forwarding is login-only. Starting a dev server or opening its localhost page
must not create a callback route. Direct localhost login pages require verified
tool support; the current narrow exception is rclone.

Broad CLI compatibility is the objective. Browser launch interception and
callback discovery are separate capabilities; neither implies universal support.
Wrangler, Claude Code and a third unrelated loopback CLI are validation targets.
Pin their versions and inspect their actual behavior before claiming support.

A broken or absent in must not prevent SSH/Mosh from connecting, alter remote
command output, or break file transfer. Report a failed login bridge without
silently redirecting the request elsewhere. Ordinary browser opens should still
work; an unknown URL must not be misreported as a supported login.

## Small architecture and existing authentication

Use one Go binary with a remote browser-helper role and a laptop coordinator
role. Start with the standard library and system OpenSSH. Prefer existing SSH
forwarding to implementing a general stream multiplexer. Choose a resident
service only after proving that automatic startup or request lifetime needs it.
No root daemon, hosted relay, account system, credential store or plugin framework.

System SSH resolves the existing host alias, config, agent, keys, certificates,
jump hosts and host verification. in never reads private keys, copies credentials,
collects passwords, requires new keys, or enables agent forwarding. Preserve
connection-specific options: reconnecting with a host alias alone may lose
options supplied to the original SSH command.

Reuse an authenticated multiplexed connection when available. A new connection
may need normal password, MFA or hardware-key approval. Mosh's terminal transport
cannot carry the bridge's SSH forwarding; retain or establish an SSH transport
separately. Prove that bootstrap works without an extra routine authentication
prompt, and expose required reauthentication honestly after a disconnect.
Do not weaken host verification or keep retrying interactive authentication in
an unattended process. Never terminate a user-owned SSH master during cleanup.

Configuration consists of existing SSH host configuration, minimal owned setup
and transient requests. No second host inventory or custom SSH config parser.
Installing integration on a host authorizes browser requests from that remote
account; the setup should explain this once, without per-login approval prompts.

## Decide automatic bootstrap first

The first implementation experiment is an ordinary SSH/Mosh session with a
working browser request route. A manually launched tunnel proves forwarding,
but cannot qualify the proposed product architecture.

Start by testing an opt-in SSH configuration fragment, a small local connection
hook, a private forwarded control socket, and remote shell initialization. This
is a candidate, not a selected design. Compare alternatives only when this fails.
The implementation uses an owned LocalCommand, a master command channel and a
private remote socket keyed by SSH_CONNECTION. Existing masters are attached at
setup; multiplexed slaves on the tested OpenSSH bypass LocalCommand. Mosh requires
a reversible shell function selecting its remote-IP startup mode. This preserves
the user's everyday command but changes the original no-command-shim hypothesis.
There is no automatic transport recovery yet. Resolve these remaining constraints:

- LocalCommand runs synchronously and cannot access the originating SSH session.
  A hook must return promptly, avoid recursive invocation, and coexist with an
  existing hook. Do not use Match exec for persistent side effects.
- SendEnv/SetEnv need server acceptance; do not require administrator changes to
  sshd or smuggle identity through TERM. User-level installation must suffice.
- Prove how each remote shell finds its matching laptop through the authenticated
  connection. A hostname, source IP, newest socket or sole currently reachable
  endpoint is not sufficient evidence of session ownership.
- Existing multiplexing, absent multiplexing, custom config paths and jump hosts
  must be accounted for. Do not replace existing LocalCommand, RemoteCommand,
  ProxyCommand or ControlPath settings to make the demo work.
- Remote control sockets belong in a private directory, with collision-safe
  ownership and stale-socket handling. Never unlink another live session's socket.
  Keep the SSH master control socket on the laptop; exposing it remotely would
  grant substantially more capability than opening a browser.
- Missing helpers, prohibited forwarding and a stopped laptop coordinator must
  leave terminal startup usable and stdout untouched. Do not enable global
  ExitOnForwardFailure or other options that turn bridge failure into SSH failure.

Measure startup delay, idle processes/connections and work per request. Use
bounded startup waits and event-driven operation. Select concrete deadline and
resource limits from the experiment before advancing; no continuous scanning of
processes, ports or terminal output.

## Request lifecycle

Keep browser launch, callback transport and authentication success distinct.

1. The remote hook submits the original URL over the authenticated private route.
   Validate bounded input, protocol version and request identity. Parse only to
   discover supported routing; pass the original URL unchanged to the browser.
2. Determine the advertised browser callback endpoint and the actual remote
   listener endpoint separately. Equal ports are a common case, not a universal
   fact. A URL with hidden redirect details is not evidence of no callback.
3. Reserve the required laptop loopback endpoint atomically before opening the
   browser. Do not probe then close a port before binding it. Keep one active
   owner per overlapping local endpoint; report conflicts without changing ports,
   displacing other software, or guessing from callback OAuth state.
4. Acknowledge that the coordinator owns the request and let the hook return.
   A CLI may wait for the launcher to exit before starting its listener. Neither
   hook acknowledgement nor browser launch means login succeeded.
5. Open the browser once local forwarding is ready. Handle a late remote listener
   with a bounded connection-establishment policy. SSH forward creation alone
   does not prove destination readiness. Do not send synthetic HTTP probes or
   consume a one-shot accept as a readiness check. Test a fixture whose listener
   starts only after its browser hook returns.
6. Forward callback bytes unchanged, including responses and TCP half-closes.
   Preserve Host, path, query, POST bodies and TLS bytes. Retry only connection
   establishment before delivery; never replay callback data after uncertain
   delivery. Bound waiting connections and memory while a listener is unavailable.
7. Release owned resources after an observable end, cancellation or deadline.
   An opaque byte relay cannot generally identify OAuth success, and the first
   connection can be a browser preconnect. Do not close the route after the first
   accept or first EOF. Use a finite lease when CLI completion is unobservable;
   measure its duration against real flows and allow active responses to drain
   within a bounded limit.

The coordinator owns the route after acknowledging a request; hook exit does
not cancel it. Bound control messages, concurrent requests, connections and
leases. Duplicate request IDs must not reopen the browser. A lost acknowledgement
must not trigger blind resubmission. Process restart or transport loss invalidates
pending requests; never persist or automatically reopen authorization URLs.
Report interrupted requests and let the CLI/user initiate a fresh attempt.
Recover the transport for future requests where existing authentication permits.

## Routing, trust and failure boundaries

Use explicit loopback binds for 127.0.0.1, ::1 and the required localhost family
coverage. Pin remote callback destinations to verified loopback addresses;
never resolve an arbitrary hostname into permission to reach remote services.
A container's loopback may be different from the SSH server's: do not claim
container or nested-SSH support without reaching the correct network namespace.
Keep the redirect URI intact even when the two machines resolve localhost
differently. Initial support targets unprivileged HTTP loopback callbacks;
local HTTPS and custom schemes require separate evidence, not certificate hacks.

Session routing must be explicit and current. For tmux, updating its session
environment does not update already-running shells. Test discovery at request
time, not only shell startup. If several laptops are attached to the same pane,
the initiating laptop may be unknowable: refuse ambiguous routing. Do not use
last-active-client guesses. Nested SSH and account switching must not silently
inherit a usable route to the wrong owner.

Trust the opted-in remote account, local user and SSH host identity. Processes
running as the same user, and root on either machine, are outside the isolation
boundary. Private socket permissions prevent other users from issuing requests;
request/session identifiers prevent accidental cross-routing, not a malicious
same-account process. Expose only browser requests and their leased loopback
routes, never arbitrary commands or general access to the laptop network.

Accept only intended web URL forms, never file/command/custom schemes by default.
Invoke a fixed browser opener with argument boundaries; no shell evaluation of
URLs. Browser navigation retains the normal browser trust boundary: in cannot
promise to police every provider redirect or page. Keep authorization URLs and
callback payloads out of in logs, persistent files and diagnostics. Use private
IPC for transport, not shell arguments or environment dumps. CLI output, browser
history and upstream opener argv are outside in's logging control; do not claim
those are redacted. Forwarding necessarily carries the authorization code in
memory; in does not interpret it or redeem it.

During an in-owned request, a forwarding failure produces a concise error rather
than silently opening the URL without its required route. Outside an active
integration, preserve the prior browser launcher without recursion. Some launch
libraries discard child stderr: prove an error reaches the user through a
supported local or remote channel. Diagnostics report stages and corrective
steps, not URLs or callback contents. No idle success messages or telemetry.

## Compatibility by capability

| Case | Initial policy and required evidence |
| --- | --- |
| Observable launcher and explicit OAuth authorization-code loopback redirect | Core path; fixed/random ports, both listener startup orders and address families |
| Remote listener differs from advertised callback | Requires proven endpoint discovery; do not assume port equality |
| Non-loopback URL with no observable callback | Can open as a normal URL; callback support remains unknown |
| Direct localhost browser page | Refuse unless a verified login exception establishes tool and listener ownership; dev-server opens stay separate |
| Printed-only URL or bypassed launcher | Unsupported automatic interception until a specific mechanism is proven |
| Native device flow | Open its ordinary browser request unchanged; do not switch authentication modes |
| Two logins needing the same local endpoint | Refuse the second without disturbing the first |
| Existing ordinary tmux shell, one unambiguous laptop | Required request-time routing test before claiming tmux support |
| Several laptops sharing a pane | Explicit ambiguity failure; no guessed routing |
| Containers, nested SSH, sudo, custom schemes, local HTTPS | Track separately; no automatic expansion of the first release |

BROWSER and a guarded PATH shim for xdg-open are implemented interception points,
not universal contracts. Existing private callbacks are pinned by socket identity
to support service-owned listeners; late listeners still need captured ancestors. Some
libraries use bundled or absolute launcher paths, detach the child, or discard
its output. Keep existing browser overrides and desktop openers working. A tiny
compatibility shim may be justified by evidence; a general provider plugin
framework is not. Never change the CLI's OAuth state, PKCE, redirect URI or stores.

## Build sequence and acceptance gates

### 1. Feasibility and minimal automatic installation

Inspect existing tools before adding machinery: aft's SSH process ownership,
system forwarding, editor browser/port forwarding, and dedicated browser relays.
Record what each already solves and the remaining terminal-independent gap.
For the initial CLIs, pin version/source and observe launcher, redirect discovery,
listener timing, detach behavior and completion signals. Keep source inspection
separate from executed authentication evidence.

Build the smallest disposable bootstrap experiment and reversible installer
fragment together. Exercise normal SSH and Mosh, existing authentication,
forwarding denied, helper absent, and two laptops/sessions. Prove remote route
identity without changing sshd, replacing SSH commands or requiring a new key.
Installation is part of this feasibility gate, not a final packaging task.
Record minimum macOS/system-SSH and Linux/shell versions that actually pass.

Gate: normal connection plus fixture browser request reaches the correct laptop,
with no per-connection command, no unexpected auth prompt, and no effect on an
ordinary remote command, scp/sftp or terminal startup when in is unavailable.
If the candidate cannot do this, revise the architecture before continuing.

### 2. Complete one login through the same automatic path

Use a controlled fake CLI/provider and disposable actual SSH server to prove the
request lifecycle. Add focused tests for late listener startup, exact callback
bytes including POST, family mismatch, conflicting ownership, ambiguous session,
resource bounds, interrupted delivery and cleanup. Test browser preconnect and
multiple connections without treating either as login success. Use no live
credentials or provider logins in CI.

Gate: fixed and random port fixture logins complete through the installed normal
session path; cancellation or expiry releases owned listeners; unrelated SSH
connections and forwards survive. Include one real macOS browser run early.

### 3. Validate real tools and session behavior

Run Wrangler, Claude Code and a third unrelated loopback CLI on a real macOS/Linux
pair. A human approves provider consent; verify the remote CLI's authenticated
state using its normal behavior without reading credentials. Record exact CLI,
OS, browser, SSH and Mosh versions. Validate ordinary tmux reattachment, Mosh
roaming/reconnect, port conflicts and concurrent distinct-port requests.

Gate: each claimed supported path succeeds without wrappers or manual tunnels.
Record limits explicitly. Cross-builds and fake logins are not provider evidence.

### 4. Ship a reversible installation

Harden the installation proven in stage 1: idempotent setup, atomic owned changes,
coexistence with user hooks, upgrade/version mismatch handling, rollback on partial
failure, and uninstall that removes only owned files/blocks. Existing config
changes made after installation must survive. Avoid broad shell PATH shadowing.
Add checksum-verified distribution, setup/troubleshooting docs and a compatibility
matrix. Manage a macOS service only if the proven architecture uses one.

Gate: install, use, upgrade and uninstall work on the real target pair. Add a
component only for an observed failure with clear ownership and cleanup. Avoid
abstractions without multiple demonstrated uses. Keep the normal path invisible
and failures understandable.

## Command name

Keep the product and binary named in. Bare `in setup` is invalid Bash syntax;
use an installer or explicit executable path for one-time management. The current
source build is `./bin/in`. Service managers and browser hooks execute it directly.
This does not add a command to daily login use.
