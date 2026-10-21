# Architecture

One Go binary provides setup/management, a laptop coordinator, a remote browser
relay and callback stream helpers. The coordinator uses `callbackTransport.Open`
for each stream. SSH and ET implement that small contract; browser admission,
loopback leases, listener ownership, privacy and cleanup are shared. SSH uses
system OpenSSH for authentication and encryption. ET carries its own private
session socket inside its encrypted terminal tunnel. No private key material
passes through in.

## Startup and ownership

A host-scoped LocalCommand starts the coordinator asynchronously. The coordinator
joins the existing master using its ControlPath and opens a remote `in relay`
command over standard SSH stdin/stdout. Fallback network authentication is disabled
for these internal channels; a missing master fails rather than initiating a new
login. Setup also explicitly attaches to an already running master, since the
installed OpenSSH bypasses LocalCommand for a multiplexed client. The hook reads
literal `-S` and `-o ControlPath` overrides from its owning SSH process, including
Kitty's default shared socket, without changing client configuration.

The relay creates a mode-0600 Unix socket inside a mode-0700 account directory.
The laptop stores a random logical session ID per ControlPath. A newly authenticated master binds its exact `SSH_CONNECTION` tuple to
that ID. The remote account retains up to 128 exact tuple aliases so a surviving
Mosh shell can still find the laptop after master replacement and a fresh login.
Inactive aliases are intentionally unavailable to another ID; ambiguous or
invalid bindings fail closed. A recycled tuple is not reassigned to another ID,
because an old Mosh shell may still carry that tuple. The authenticated hook refreshes the relay on a
new master. It does not start unattended authentication or replay a previous URL.
A lock prevents competing relay owners. No SSH control or agent socket is
forwarded to the remote host.

A remote browser helper selects a relay through `SSH_CONNECTION` or
`IN_SESSION_SOCKET`. In tmux it requires one attached client, checks that client's
process ownership, and reads only those two routing variables from its live
`/proc` environment. It refuses a missing pane or ambiguous clients and never
prints the process environment.

The installed ET 7+ function adds a native reverse Unix socket to an otherwise
ordinary ET terminal. The remote shell receives `IN_SESSION_SOCKET`; the remote
agent starts on the first browser request. Standard-library HTTP/2 runs over that
private socket inside ET's encrypted tunnel. Two paired request streams allow
each callback direction to half-close independently. No extra TLS, key exchange
or general client plugin layer is added. `in stop` closes ET bridge resources
without ending the terminal. Existing custom `et` functions and aliases remain
untouched. A future terminal integration needs authenticated command streams or
an encrypted forwarded private Unix socket, plus a local owner for its terminal
session; setting an environment variable alone is insufficient.

## Request and callback

The remote hook sends one bounded, versioned JSON request containing a random
request ID, the original URL and a bounded listener reference. The laptop validates its shape and reserves the
advertised loopback endpoint. It acknowledges ownership before opening the browser,
so a synchronous CLI can return from the hook and start its callback listener.
The authorization URL is passed unchanged to the local opener.

No command or port is watched. Browser launch is the trigger. Generic forwarding
requires a non-loopback authorization URL with `response_type=code`, `client_id`
and a loopback `redirect_uri`. Direct localhost opens are refused unless verified
as rclone's known authorization entry point, with that rclone ancestor holding
the listener. The remote hook sends this narrowly scoped recognition result and
pins ownership to that process; the laptop also checks the URL shape before
binding. Missing recognition defaults to refusal, including requests from older
helpers. Ordinary non-loopback web opens have no callback route. See the
[local development boundary](usage.md#local-development-boundary).

For each browser TCP connection, the coordinator opens a transport stream: an
SSH command on the same master or paired ET HTTP/2 requests. The remote helper
inspects the requested Linux
socket-table endpoint, requiring an unambiguous private listener owned by the
remote account. For an explicit OAuth redirect, the hook pins the socket inode
already listening at that endpoint, along with its resolved numeric address. This
supports foreground CLIs, subprocesses
and background services without knowing their executable names. The helper checks
that same socket before and after connecting, before sending any callback bytes.
A closed or replaced socket invalidates the request.

If the listener has not started yet, the hook instead snapshots ancestor PIDs and
Linux start times, stopping at the supported SSH/Mosh/ET/tmux terminal server. A late
listener must be held by one of those still-live ancestors. A detached opener may
change POSIX session IDs without losing its CLI parent. A background service must
start its callback listener before requesting the browser.

This trusts the opted-in remote account to supply the correct OAuth destination;
it does not prove which process initiated a service's authentication attempt.
It prevents destination replacement, not a same-account client deliberately
naming another service. Laptop selection still comes exclusively from the browser
request's current SSH/tmux session, never the listener's process environment.
It retries establishment for up to ten seconds without consuming
an accept for a readiness probe. Then it reports readiness and relays raw bytes.
The same listener checks and byte handling apply to both transports.

The browser request and CLI response retain their bytes, including POST bodies
and half-close behavior. There is no HTTP parsing, token exchange, credential
storage or OAuth success detection. After the stream ends, a short bounded socket
inspection can identify the original listening inode's disappearance and release
the route when no other callback streams remain. Otherwise a three-minute lease
closes it. A preconnect EOF alone never counts as OAuth completion. A new request for the
same resolved address and port can reclaim an idle lease when it pins a different socket: the
unambiguous socket lookup proves that the original listener has closed. Requests
for the same live socket and routes with active responses remain protected.
This lets a cancelled login be retried without waiting out the lease, including
localhost/numeric aliases. Browser-process startup and route replacement share a
lock so a reclaimed request cannot subsequently spawn its delayed opener.
Already-open browser tabs cannot be revoked by the bridge. As with any reused
localhost port, an old tab may later reach the new listener; OAuth state and PKCE
validation remain the CLI's responsibility. in never replays callback bytes.

## Bounds and cleanup

Wire version 3 carries session bindings and socket references; mixed versions fail explicitly. Update
both installations together. Control messages are limited to 128 KiB, URLs to 64 KiB, concurrent routes to 32,
and concurrent callback streams per route to eight. A relay admits at most 32
active browser submissions and callback watches combined. A connection coordinator
remembers up to 128 request IDs for three minutes to reject duplicate submissions.
The hook does not blindly retry a request after an unknown acknowledgement.
Payloads are never persisted in logs or error messages. The last diagnostic is
replaced atomically and limited to 1 KiB; there is no growing request history.

Coordinator cancellation closes its listeners, cancels callback channels and
waits for owned work. Cancellation explicitly closes subprocess pipes: an SSH
master can retain duplicated descriptors after its client process dies. Each
callback helper also watches its remote relay through a private socket; relay
shutdown cancels even a helper blocked on a silent CLI. Remote relays end when
their control stream ends. Advisory
locks are released by the OS. Stopping in never sends an exit command to the
user's SSH master. Stop waits up to eight seconds for owned process locks to be
released; uninstall reports a failure rather than deleting files before teardown.
Process restart/disconnect does not reopen saved URLs. Control heartbeats run
every 15 seconds; 90 seconds without a valid reply ends a detached relay.

Remote setup supplies BROWSER and a dedicated PATH directory containing only an
xdg-open shim. Both activate only in SSH shells without an explicit user browser
override. The shim delegates dev URLs, files and options to the native opener with
its own PATH entry and BROWSER removed, preserving arguments without evaluation.
A captured login failure is never retried through another opener. Absolute or
bundled openers and printed-only URLs remain outside these interception points.

An old Mosh shell can regain forwarding after a fresh authenticated SSH hook on
the same logical binding. Until then a dead master provides no callback channel;
in never silently creates a new SSH login. ET's private session tunnel can resume
with its terminal. Neither path resubmits a URL or callback after uncertain delivery.

## Setup transactions

Runtime startup registers its socket while holding the setup lock and requires a
current installation manifest. Stop advances a private startup generation while holding that lock; delayed
children from the previous generation cannot register afterward. Uninstall holds
that lock through teardown, so a
late coordinator cannot register after its files are removed. Stop is admitted
separately from the relay's request capacity.

Setup stages generated files and exact shell/SSH blocks, validates existing
ownership and file snapshots, then uses same-directory atomic replacements.
Write failures restore earlier changes when they still contain the bytes in
wrote; concurrent user changes are preserved and incomplete rollback is reported.
This is normal-error rollback, not a crash-atomic filesystem transaction.

Uninstall groups blocks by file, preserves surrounding user edits, and accepts
already removed blocks/files so a partial uninstall can be retried. Edited owned
blocks (including edited markers) or generated files require review. Install never treats arbitrary files
in its application directory as disposable.
