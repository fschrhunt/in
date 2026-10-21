# Setup and usage

The current version is experimental. Use macOS or Linux locally, Linux remotely,
and Bash or Zsh. Native tests ran on two Apple Silicon Macs, including a real
browser callback to Linux. The remote helper needs access to its account's
`/proc/net/tcp` and `tcp6` socket tables and the requesting processes' `/proc`
metadata and file-descriptor links. Installation needs no root privileges or sshd changes.

## Install from source

Run `./x build` in the source checkout on each machine. Then:

```sh
# Linux server
./bin/in setup remote

# Laptop: an existing Host alias from your SSH configuration
./bin/in setup laptop my-server
```

Setup copies the binary to `~/.local/share/in/bin/in`. Future management can use
that full path. Bare `in` is a keyword in Bash, so examples use a path.

Restart the local and remote shells to load the hooks. Local setup attaches to
an existing SSH master if one is running; otherwise a normal SSH connection
starts the bridge automatically. A terminal already inside a remote shell needs
to load the new shell initialization or reconnect once after installation.

After setup, normal commands work unchanged:

```sh
ssh my-server
wrangler login
```

Or use `mosh my-server`. The installed Mosh function adds
`--experimental-remote-ip=remote`, because Mosh's default startup disables SSH
connection sharing. Existing Mosh aliases/functions are left in place; if you
already have one, it must select this mode for Mosh integration to work.

With ET 7 or newer, use `et my-server` for a host opted in by laptop setup. The installed `et` function adds ET's
native private reverse Unix socket. The remote browser agent starts only when a
browser request arrives. An older ET client keeps its normal terminal behavior
without browser forwarding. Connections to other ET hosts retain ordinary ET behavior without a bridge.
Existing custom `et` aliases/functions are untouched;
they need an encrypted forwarded private Unix socket, `IN_SESSION_SOCKET` and a
local owner for that terminal session to enable this path.

## What setup changes

The remote setup installs a small browser launcher and a shell fragment. In shells
with `SSH_CONNECTION` or `IN_SESSION_SOCKET` it supplies BROWSER only when unset or already managed by in,
and adds a dedicated PATH directory containing only an xdg-open shim. Re-sourcing
the fragment does not duplicate that entry. Files, options and direct dev URLs
passed to xdg-open retain the native opener; no callback forwarding is started.
A local tmux session alone does not enable the hook. An old pane created locally
may need a new shell after attaching over SSH.
An existing custom BROWSER remains authoritative and may bypass in.

Laptop setup adds an Include block to `~/.ssh/config` for the selected host. The
included fragment enables PermitLocalCommand and enables ControlMaster auto when
connection sharing is off. It supplies a private ControlPath when none exists,
and a 60-second ControlPersist when persistence is off. Existing ControlPath and
nonzero persistence settings are retained. Setup refuses a host that already
has LocalCommand, rather than guessing how to compose shell commands. It does
not replace RemoteCommand or ProxyCommand in your normal session.

The fragments are sourced from `.zshrc`, or from `.bashrc` and `.bash_profile`.
Each block has an exact ownership record. Update both machines together: wire
version 3 rejects older peers explicitly. When the installed binary changes, setup first asks that older binary to stop
its own bridge protocol. SSH masters and ET terminals remain alive; reconnect ET
to start its updated bridge. Installation can be repeated to update
the owned binary and fragments; edited generated files or edited managed blocks
cause a refusal. Unrelated configuration and later edits outside those blocks
survive uninstall. Symlinked initialization/configuration files must be resolved
explicitly with the custom path options below.

One coordinator and one small remote relay remain per connected SSH master until
stopped or disconnected. They retain an authenticated SSH channel after a terminal
exits so Mosh can continue to use it. The local ET function owns its bridge for
the terminal's lifetime and starts its remote agent on browser demand. There is no launchd/systemd service and no
startup scan of all hosts. Between requests only small control keepalives run; callback listeners
exist only for active requests. Opting a host in allows its remote account to
request browser opens on this laptop.

## Status, stopping and uninstall

```sh
~/.local/share/in/bin/in status
~/.local/share/in/bin/in stop
~/.local/share/in/bin/in uninstall
```

Status reports laptop coordinator processes and the last local diagnostic; it
is not proof that a provider login succeeded. Stop ends in's relays/coordinators
and their callback leases, waiting for owned processes to stop. It leaves your
SSH master and a running ET terminal alone.
To re-enable a stopped bridge on an existing master, repeat laptop setup.
Uninstall on both machines removes exact owned blocks and unchanged generated
files. Open new shells afterward; an existing shell retains its old environment
and Mosh function until it exits. Private locks and diagnostics may remain under
the application directory; uninstall never recursively deletes that directory.

## Local development boundary

There is no port scanner or command monitor. Starting a dev server does not cause
an action. Laptop setup does not replace the local browser launcher, and remote
setup does not enable its hook in a local shell without `SSH_CONNECTION` or
`IN_SESSION_SOCKET`.

Automatic forwarding is login-only. The generic path requires a non-loopback
authorization URL with one `response_type=code`, a nonempty `client_id`, and an
explicit loopback `redirect_uri`. A redirect parameter alone is insufficient.

Direct localhost pages need verified login support. The current exception is
rclone's `http://127.0.0.1:53682/auth?state=...` entry point: the browser launcher
must descend from the live rclone executable that owns that private listener.
The route remains pinned to that process. Matching the URL shape alone is not
enough. Other direct localhost opens are refused before opening the laptop browser
or reserving a port, including dev pages with `state` or `redirect_uri` parameters.
The remote hook returns a fixed diagnostic; the original server remains untouched.
Use your existing dev-server forwarding workflow for those pages.

Ordinary non-loopback web opens retain their browser handoff and create no tunnel.
There is no general plugin framework or per-command wrapper. New direct login
exceptions require observed tool behavior and focused tests.

## Failures and limits

- **Bridge unavailable:** check status on the laptop. Restarting laptop setup can
  attach to an existing authenticated master. A new master runs the hook normally.
  in does not initiate unattended replacement authentication when the transport
  disconnects. A surviving Mosh shell can use a replacement master after a fresh
  authenticated SSH login on the same ControlPath. Until
  then browser requests fail; old URLs are not replayed. ET needs its session
  socket and local terminal bridge to remain available.
- **Callback port occupied:** in refuses the request without opening the browser.
  It does not evict another process or change the CLI's redirect URI. A callback
  lease closes when listener shutdown is observed, otherwise after three minutes.
  A retry can reclaim an idle lease when a new socket at the same endpoint proves
  the old listener closed; active callback responses are never displaced.
- **Listener not ready:** the remote helper waits up to ten seconds before sending
  callback bytes. It does not replay bytes after delivery has begun.
- **Ambiguous localhost listener:** the remote helper refuses when both loopback
  families have separate matching listeners. Only private listeners owned by the
  current remote account qualify; wildcard listeners are not supported. Existing
  sockets are pinned at browser-request time, including sockets in background
  services. A closed or replaced socket is refused. A late-starting listener must
  belong to a captured ancestor of the browser hook, identified by PID/start time.
  Services must start their callback listener before requesting the browser.
- **tmux ambiguity:** exactly one client must be attached to the session. The hook
  reads only `SSH_CONNECTION` and `IN_SESSION_SOCKET` from the owned client's
  live process environment. A missing pane or two attached clients is refused.
- **Hidden/bypassed callback:** in supports explicit OAuth authorization-code redirects and the
  verified rclone direct login entry point. It does not infer hidden endpoints, scrape terminal
  output, intercept absolute/bundled launchers, or change device flows. A remote callback address different from the
  advertised address is not automatically discovered.
- **Detached launchers:** acknowledgement means in accepted responsibility, not
  that OAuth succeeded. Later launch/transport errors are recorded locally and
  produce a macOS notification. The originating CLI may suppress helper stderr.

See [compatibility](compatibility.md) for observed CLI versions. Containers,
nested SSH, account switching, local HTTPS, privileged callback ports and custom
URI schemes have no verified automatic path. Connection-specific SSH overrides
are supported by generated ControlPaths and literal parent SSH `-S` or
`-o ControlPath` overrides, including Kitty's default sharing. Unsupported or
ambiguous overrides fail closed. A native `tailscale ssh` wrapper can rewrite a
destination to its canonical MagicDNS name with a trailing dot; opt in the
actual SSH destination on the laptop, not merely another alias for the same host.

## Isolated and custom setup

`IN_HOME` selects another private application directory. Paths must fit Unix
socket limits and cannot contain whitespace, percent, colon, double quotes or backslash.
Use the same value for setup and later management.

```sh
./bin/in setup remote --shell-file /path/to/remote.rc
./bin/in setup laptop --shell-file /path/to/local.rc --ssh-config /path/to/ssh.conf my-server
```

For a custom remote install, add `--remote /absolute/path/to/in` to laptop setup.
The remote process must receive the matching IN_HOME, for example from its remote
shell initialization. Options precede the host alias. IN_BROWSER_EXEC selects a
single local opener executable for fixtures or an explicit browser preference;
its sole argument is the unchanged URL, with no shell parsing or added flags.

## Release archives

No public release exists yet. Once published, `install.sh --version vX.Y.Z`
downloads the matching archive and checksum manifest, verifies both checksum and
binary version, and atomically installs `~/.local/bin/in`. It does not perform
shell/SSH setup. Local archives can be installed with `--archive FILE --checksums
FILE`; `--dir DIR` selects an alternative binary directory. Checksums from the
same release detect corruption, not a compromised release publisher.
