# Implementation plan

Status: proposed; infrastructure is implemented, runtime work has not begun.

## Product contract

Install in once on the laptop and remote server. Continue using ordinary `ssh`
or `mosh` and unmodified CLI commands. When a remote program requests browser
authentication, the laptop browser opens and any loopback callback reaches the
original remote listener. The CLI completes its normal login and stores its own
credentials remotely. No per-login wrapper, manual tunnel, or device flag.

macOS laptop and Linux server come first. Wrangler, Claude Code and another
unrelated loopback CLI are compatibility probes, not separate products. Do not
assume their current implementations or commands before inspecting them.
Native device flows may pass through naturally; in does not select them.

## Command name constraint

`in` is a reserved word in common POSIX-family shells, including bash and zsh.
The product and binary can retain the name, and `./bin/in` or an absolute
executable path works. A bare `in setup` command is not portable shell syntax.
Design one-time setup around an installer or explicit executable path; service
managers and browser hooks invoke the executable directly. Do not introduce a
daily alias or wrapper to work around the name. Decide the documented management
interface before shipping installation.

## Smallest proposed architecture

Use one Go binary with laptop and remote roles, system OpenSSH as the encrypted
transport, and a remote browser helper. Begin with the standard library and add
dependencies only for demonstrated needs. No hosted relay, token broker, general
plugin framework, root daemon, browser extension or CLI-specific login wrapper
in the initial design. Revisit those exclusions only when research demonstrates
that an essential requirement cannot be met otherwise.

A laptop user service accepts authenticated requests over a private control
channel. A remote browser helper uses browser environment hooks and common URL
launchers in opted-in sessions. It forwards the original authorization URL
without changing OAuth parameters. If a loopback redirect is discoverable, bind
the matching laptop loopback endpoint before opening the browser and carry raw
callback traffic to the original remote endpoint. Prefer byte forwarding over
an HTTP callback server that reconstructs requests.

A browser hook does not require a resident remote daemon. Start with an on-demand
helper; introduce a remote supervisor only if session lifetime or listener
discovery requires one. Session routing and transport bootstrap remain research
gates, not solved implementation details.

## Stage 1: establish coverage and automatic bootstrap

Inspect workspace tooling, especially aft's system-SSH invocation and tunnel
ownership practices. Reuse small proven pieces only if they fit; do not depend
on aft or import its workspace/tmux features. Compare existing browser relays,
SSH forwarding helpers and remote-development tools before adding new machinery.

For Wrangler, Claude Code and a third unrelated CLI, record pinned versions,
source references and observed behavior:

- How browser launch occurs: BROWSER, another environment variable, xdg-open,
  a direct executable, or a URL printed without a launch request.
- Whether the authorization URL exposes the full redirect URI, or an intermediate
  service conceals it. Whether the listener exists before the browser hook runs.
- Fixed versus dynamically selected ports, IPv4/IPv6 binding, callback paths,
  listener lifetime, subprocess behavior and success/exit semantics.
- Whether callback completion requires custom schemes, local HTTPS, multiple
  connections or additional browser-to-loopback requests.

Prototype ordinary ssh/mosh startup integration without replacing their command
names. Assess SSH configuration hooks, multiplexing, session-scoped private
sockets, and independent transport startup. Mosh bootstraps through SSH but does
not carry SSH forwards in its UDP terminal connection; its bridge needs a
separately maintained connection. Verify behavior across disconnects and roaming.
Do not assume that an arbitrary existing SSH connection exposes a usable master.

Deliverable: an evidence-backed compatibility matrix and selected bootstrap
mechanism. Gate: demonstrate how a normal remote shell gets the browser helper
and a route to the correct laptop with no per-login or per-connection command.
If this is not achievable for a session type, document the boundary before building.

## Stage 2: prove the bridge with a controlled fixture

Build a local-only fake CLI and authorization service that exercise fixed and
random loopback ports and record completion without real credentials. Prove
browser request delivery, listener readiness, byte-preserving callback forwarding,
and original CLI completion over an actual disposable SSH environment.

Define a small bounded control protocol with explicit request/session IDs,
authenticated ownership, deadlines and supported destinations. Permit only
expected web URL schemes and loopback callback targets; never execute URL text
through a shell. Protect control sockets with owner-only permissions. Ensure a
remote request cannot choose arbitrary local files, commands or network targets.

The laptop binds only loopback and handles localhost/IPv4/IPv6 deliberately.
Port ownership lasts through completion or expiry. Never silently change a
callback port or redirect URI. Reject conflicting or ambiguous ownership.
Clean up only owned resources on cancellation, timeout, disconnect and failure.
Use a bounded lease when the original CLI's completion is not observable.

Tests each protect one behavior: unchanged callback bytes, dynamic-port readiness,
conflicting port refusal, invalid request rejection, session isolation, timeout
and owned-resource cleanup. No live provider logins in CI and no credentials,
authorization URLs or callback queries in diagnostics or snapshots.

## Stage 3: make normal sessions automatic

Provide reversible one-time setup for macOS user services, remote browser hooks,
and SSH transport/bootstrap integration. Show concrete managed changes and keep
unrelated shell/SSH configuration intact. Uninstall removes only owned changes.
The installer must not require typing in before ssh, mosh or a login command.

Scope browser-launch interception to participating remote sessions and preserve
normal launcher behavior elsewhere. BROWSER support is not universal; validate
common launcher shims and document bypasses. Avoid blanket process tracing or
polling every listener as an unproven default.

Handle two laptops, multiple servers, multiple remote sessions, nested SSH and
tmux reattachment. Never guess which laptop owns a browser request; stale session
metadata must not route to an old connection. Define reconnect and suspended-laptop
behavior. Keep quiet during normal operation and report actionable failures.

Gate: ordinary ssh and mosh followed by a fixture login work without a wrapper,
including cleanup and disconnect recovery. The bridge uses SSH credentials only
through system SSH, never copies them, and respects host verification.

## Stage 4: validate real CLI coverage

Use the Stage 1 matrix to exercise current Wrangler, Claude Code, and a third
unrelated loopback CLI on a real macOS/Linux pair. Record exact versions and
whether each path was source-inspected, fixture-tested or actually authenticated.
Provider approval remains an interactive human step; do not call cross-builds
or fake logins evidence of live provider success.

Cover existing and multiplexed SSH, Mosh, fixed/random callback ports, IPv4/IPv6,
port conflicts, concurrent login attempts, cancellation and expired requests.
Validate opaque redirect handling before claiming it works. Printed-only URLs,
custom URI schemes and CLIs bypassing launch hooks must have explicit support
boundaries; a browser relay alone cannot guarantee every authentication flow.

Gate: successful unwrapped login from the remote terminal through the laptop
browser, leaving the remote CLI authenticated with no manually configured tunnel.

## Stage 5: ship the installation experience

Add checksum-verified installation and upgrades using the four archive targets,
macOS service lifecycle support, focused diagnostics, and complete setup,
troubleshooting and uninstall documentation. Include Mosh transport reconnection
and session-routing guidance based on validated behavior.

Make the first release only after the ordinary-command experience passes on a
real laptop/server pair. Keep a visible compatibility matrix and distinguish
unsupported cases from broken installation. Avoid promising universal coverage.

## Open design decisions

1. Can SSH configuration bootstrap both ordinary SSH and Mosh without wrapping
   either command and without a second interactive authentication prompt?
2. How do persistent shells receive the correct current laptop identity without
   inheriting a stale tmux environment?
3. Is redirect-URI inspection sufficient across the initial CLIs? If not, can
   process-scoped listener discovery solve the gap safely and without root?
4. Can a single remote browser hook cover enough launch libraries, and what
   happens when a CLI prints a URL or invokes an absolute browser executable?
5. How will listener leases end when the browser helper is detached and cannot
   observe completion of its originating CLI?
6. What explicit behavior is acceptable when the required laptop callback port
   is occupied? Arbitrary port substitution changes the provider's redirect.

Resolve these through small experiments before committing to background agents
or broad interception mechanisms.
