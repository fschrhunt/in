# Working on in

in makes remote CLI browser login work with a local browser. Keep it small.

- Preserve unrelated changes and never expose secrets.
- Prefer direct implementations; no general plugin framework without evidence.
- Comment modules and functions with concise purpose and contract notes.
  Update affected comments and documentation with each behavior change.
- Write focused, offline tests for meaningful behavior. Use temporary homes,
  fake browser commands and controlled listeners; never personal credentials.
- Do not push, tag, release, or modify personal shell/SSH configuration unless
  authorized. Local builds and checks do not install or publish anything.

## Code map and checks

`cmd/in/main.go` dispatches management commands and internal entry points.
`ssh.go` owns laptop coordinators and channels on existing SSH masters;
`remote.go` owns the remote browser socket, tmux routing and callback helpers.
`bridge.go`, `url.go`, `listener.go` and `owner.go` own loopback routing, bounded
leases and process ownership evidence.
`opener.go` captures PATH-resolved browser requests and delegates native file/dev opens.
`setup.go` owns reversible integration; `state.go` and `wire.go` handle private
files and the small control protocol. Tests live beside the behavior they protect.

`docs/usage.md`, `docs/architecture.md` and `docs/compatibility.md` describe current
behavior. `docs/plan.md` retains the target and remaining gates; the original audit
is historical evidence. Do not mistake CLI launch probes for completed logins.
`x` is the local/CI entry point. Run `./x check`, `./x integration` for transport or
setup changes, and `./x release-check` for distribution. Integration uses disposable
SSH keys/configuration and requires Linux, sshd, Mosh and tmux. No personal setup.

## Design contracts

Normal SSH/Mosh and CLI commands must remain the everyday interface after setup.
Delegate SSH authentication and host configuration to system OpenSSH. Never read
private keys, copy credentials, require new keys or enable agent forwarding.
Reuse authenticated connections where possible; do not promise silent new
connections for authentication methods that need user interaction. Add services
or configuration only when an observed requirement justifies them.
Do not select a different authentication flow or rewrite OAuth state, PKCE,
redirect URIs or credential storage. Catch browser requests rather than wrapping
commands. Forward login callbacks only; leave dev-server opens outside forwarding.
Require explicit OAuth authorization-code evidence or verified direct-flow support. Callback forwarding must bind loopback only and expire promptly.
Never log authorization URLs, callback query strings, codes or tokens. Explicitly
handle port conflicts, ambiguous sessions and unobservable browser launches;
do not report success when compatibility has not been demonstrated.
Keep terminal startup and unrelated SSH commands usable when in is unavailable.
Separate request acknowledgement, listener readiness and CLI authentication.
Do not block a browser hook waiting for a listener that depends on hook exit.
Use current session ownership; refuse ambiguous tmux/multi-laptop routing.
Keep callback routes bounded, support multiple connections, and never replay
callback bytes after uncertain delivery. Never forward the SSH master socket.
