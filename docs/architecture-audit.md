# Architecture audit

Reviewed 2026-10-21. This audits the proposal, not a working runtime. No live
provider login or automatic SSH/Mosh bootstrap was performed. The revised
[plan](plan.md) turns unresolved assumptions into experiments and release gates.

## Findings and changes

| Finding | Plan correction |
| --- | --- |
| Automatic bootstrap was listed as research but the build order still postponed installation | Make ordinary SSH/Mosh startup and minimal reversible installation the first gate |
| Browser-hook acknowledgement, listener readiness and login success were conflated | Define distinct lifecycle stages; return from the hook without waiting for OAuth or a listener that depends on hook exit |
| Tunnel setup was treated as destination readiness | Test a late listener and bounded connection establishment; no synthetic callback or replay |
| Redirect URI was implicitly treated as the remote listener address | Track advertised and actual endpoints separately; require evidence for non-default mappings |
| A stateless browser helper was expected to own cancellation and completion | Transfer ownership to the coordinator at acknowledgement; use finite leases when CLI completion is unobservable |
| tmux/session mapping was a list item rather than a correctness boundary | Require current request-time ownership and refuse ambiguous multi-laptop routing |
| Invisible integration could break unrelated terminal behavior | Require missing-helper, forwarding-denied, remote-command and scp/sftp tests; preserve user SSH hooks |
| Installation and compatibility surface could grow without a clear limit | One binary, conditional service, capability-based coverage, measured resource limits and no automatic expansion into nested/container sessions |
| The confidentiality promise was broader than the tool can enforce | Distinguish in's logs from CLI/browser output and acknowledge transient callback bytes in memory |
| The shell-name note overstated zsh's restriction | Record the demonstrated Bash parse failure and use an explicit executable path for management |
| The scaffold copy accidentally changed draft to drin in release.yml | Restore the draft job/flag, verify the CLI option and run workflow lint |

An independent read-only architecture review reinforced the bootstrap, lifecycle
and session-ownership findings. One suggested requirement, proving the remote
listener ready before opening the browser, needs qualification: waiting inside
the launcher can deadlock, and probing a listener can consume an accept. The
revised plan requires local reservation before opening and bounded remote
connection establishment, with a fixture for both listener startup orders.

## Evidence checked

### OpenSSH integration constraints

The [OpenSSH configuration manual](https://man.openbsd.org/ssh_config) documents
LocalCommand as synchronous and unable to access its parent SSH session.
SendEnv/SetEnv depend on server acceptance. ExitOnForwardFailure covers setting
up forwards, not successful connections to the final destination. Config values
usually use the first obtained value, so appending a fragment does not guarantee
it takes effect. Shell-expanded configuration tokens need careful quoting.

These are constraints on bootstrap, not proof that the candidate design works.
The installed Linux OpenSSH 10.2p1 manual was also checked. The oldest supported
macOS OpenSSH version still needs a real test; current OpenBSD docs are not a
macOS compatibility guarantee.

### Callback endpoint discovery

Cloudflare's current
[callback implementation](https://github.com/cloudflare/workers-sdk/blob/main/packages/workers-auth/src/callback-server.ts)
starts its server before calling its browser opener. It separately accepts a
callback host/port and a registered redirect URI, and warns when they differ.
Therefore extracting redirect_uri alone does not establish the actual remote
listener for every supported CLI configuration. This is source evidence, not a
live Wrangler result. Snapshot: Git blob
`3f94abe049fa3767c5f8b8182c4e5b091cc244d6`; the linked main branch can change.

The new lifecycle fixture must also cover the opposite startup ordering, even
though the inspected Cloudflare path starts listening first.

### Browser launch interception

The [open library](https://github.com/sindresorhus/open/blob/main/index.js) can
choose its bundled xdg-open by absolute path. Its non-waiting Linux path detaches
the child and discards stdio. A PATH shim and child stderr alone therefore cannot
establish universal interception or reliable error reporting. Which library and
version each target CLI uses remains part of the compatibility investigation.

### Callback transport

[RFC 8252 sections 7.3 and 8.3](https://datatracker.ietf.org/doc/html/rfc8252#section-7.3)
describe loopback callback addresses, dynamic ports, IPv4/IPv6 considerations and
short-lived listeners. Its acceptance of dynamic port values does not authorize
in to change a redirect chosen by the CLI. The proposal preserves that URL and
uses SSH for the network hop between the two machines.

### Mosh and persistent shells

[Mosh](https://mosh.org/) uses SSH for startup and a UDP connection for the
terminal. Its roaming behavior does not establish that a separate SSH callback
transport has recovered. The plan requires independent failure/reconnect tests.

The [tmux manual](https://man.openbsd.org/tmux#GLOBAL_AND_SESSION_ENVIRONMENT)
describes environment updates at the session level and environments supplied to
new processes. It also permits multiple clients on one session. Updating tmux's
session environment cannot rewrite an existing shell's inherited environment;
the initiating laptop can be ambiguous when clients share a pane. The revised
plan treats that as an explicit routing boundary.

### Existing tools and naming

[VS Code Remote SSH](https://code.visualstudio.com/docs/remote/ssh#_forwarding-a-port-creating-ssh-tunnel)
already supports port forwarding within its editor workflow. It may choose a
different local port when one is occupied. That is useful for a development
server but is not sufficient for preserving a CLI's original OAuth redirect.
The broader comparison with dedicated browser relays is still outstanding.

Local parser checks found `bash -n -c 'in setup'` fails, while the installed zsh
accepts it. Explicit executable paths work around Bash's keyword without
changing the product name or daily workflow.

## What remains unproven

- A user-level automatic bootstrap that associates ordinary SSH and Mosh shells
  with the correct laptop while preserving existing SSH hooks and authentication.
- Listener discovery when redirect details are opaque or advertised and actual
  endpoints differ, without broad process tracing or a provider plugin framework.
- A generic lease policy that handles preconnects, multiple connections and
  detached launchers without keeping callback routes open unnecessarily.
- Reliable error delivery when the invoking CLI suppresses browser-helper output.
- Full CLI/version coverage and the oldest supported macOS/system-SSH combination.

These are the next experiments, not features to hide behind implementation.

## Validation of this revision

Repository checks, documentation links, actionlint and four-target archive builds
passed locally. The release workflow's draft flag was checked against GitHub
CLI help. GitHub Actions and live login flows were not run; these checks validate
the scaffold and documents, not the proposed runtime.
