# Architecture

One Go binary provides setup/management plus three internal roles: laptop
coordinator, remote browser relay, and a short-lived callback stream helper.
System SSH handles all authentication and encryption. No private key material
passes through in.

## Startup and ownership

A host-scoped LocalCommand starts the coordinator asynchronously. The coordinator
joins the existing master using its ControlPath and opens a remote `in relay`
command over standard SSH stdin/stdout. Fallback network authentication is disabled
for these internal channels; a missing master fails rather than initiating a new
login. Setup also explicitly attaches to an already running master, since the
installed OpenSSH bypasses LocalCommand for a multiplexed client.

The relay creates a mode-0600 Unix socket inside a mode-0700 account directory,
named from a digest of SSH_CONNECTION. That value identifies a transport, not a
shell: shells on the same master correctly share the same laptop destination.
Different laptops have different authenticated transport tuples. A lock prevents
two owners from replacing one another's socket. Only a lock owner removes a stale
socket. No SSH control socket or agent socket is forwarded to the remote host.

A remote browser helper chooses that socket from its current SSH_CONNECTION. In
tmux it requires one attached client and reads the session's current connection
value rather than trusting an old pane environment. This does not identify an
initiating client when several are attached; that case fails explicitly.

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

For each browser TCP connection, the coordinator opens another command channel
on the same SSH master. The remote stream helper inspects the requested Linux
socket-table endpoint, requiring an unambiguous private listener owned by the
remote account. For an explicit OAuth redirect, the hook pins the socket inode
already listening at that endpoint, along with its resolved numeric address. This
supports foreground CLIs, subprocesses
and background services without knowing their executable names. The helper checks
that same socket before and after connecting, before sending any callback bytes.
A closed or replaced socket invalidates the request.

If the listener has not started yet, the hook instead snapshots ancestor PIDs and
Linux start times, stopping at the supported SSH/Mosh/tmux terminal server. A late
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
This small command helper is needed for late listeners and listener ownership;
there is no custom general-purpose transport multiplexer.

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

Wire version 2 carries socket references; mixed versions fail explicitly. Update
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
Process restart/disconnect does not reopen saved URLs.

Remote setup supplies BROWSER and a dedicated PATH directory containing only an
xdg-open shim. Both activate only in SSH shells without an explicit user browser
override. The shim delegates dev URLs, files and options to the native opener with
its own PATH entry and BROWSER removed, preserving arguments without evaluation.
A captured login failure is never retried through another opener. Absolute or
bundled openers and printed-only URLs remain outside these interception points.

There is currently no automatic bridge transport reconnect. Ordinary Mosh roaming
may leave its terminal alive after the SSH bridge has died. Reconnect/rebind work
must preserve the authenticated relationship to the old shell and account for
interactive SSH authentication; it is not silently approximated by host/IP guesses.

## Setup transactions

Runtime startup registers its socket while holding the setup lock and requires a
current installation manifest. Uninstall holds that lock through teardown, so a
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
