# in

Remote login, in your browser.

Install once on your laptop and server. Keep using `ssh`, `mosh` and your CLI's
normal login command. in opens the laptop browser and carries localhost callbacks
back to the remote CLI, using your existing SSH authentication. Forwarding is
login-only: dev-server browser opens do not create tunnels. Browser requests can
come through BROWSER or xdg-open, and existing private callback listeners can run
in the CLI or its background service. No CLI-name allowlist is needed for explicit
OAuth redirects.

**Experimental.** Tested with real SSH/Mosh sessions, two Apple Silicon Macs,
and a macOS browser reaching a Linux callback listener. Completed provider
logins still need validation. See the [compatibility record](docs/compatibility.md).

## Start

Build on each machine with Go 1.26 or newer:

```sh
./x build
```

On the Linux server:

```sh
./bin/in setup remote
```

On the laptop, using an existing SSH host alias:

```sh
./bin/in setup laptop my-server
```

Open fresh terminal shells to load the integration. Then use your usual commands:

```sh
ssh my-server
wrangler login
```

Mosh uses a small installed shell function to select the startup mode that keeps
SSH connection sharing available. Your command remains `mosh my-server`.
No private keys are read by in, and no new keys or agent forwarding are needed.

Read [setup and usage](docs/usage.md) before installing: setup makes explicit,
reversible changes to shell initialization and the selected host's SSH options.
It preserves existing browser overrides and refuses conflicting SSH login hooks.

## Development

```sh
./x check
./x integration
./x release-check
```

The integration suite uses disposable keys and a real SSH server, Mosh and tmux.
Python 3.9+ and those executables are needed for integration; normal binaries
have no Go or Python runtime dependency. Go source has no third-party modules.

See [architecture](docs/architecture.md), [the plan](docs/plan.md),
[contributing](CONTRIBUTING.md), [releases](docs/contributing/releases.md), and
[security](SECURITY.md). No public release has been published.

`in` is a Bash keyword, so management commands use an explicit executable path.
Daily login commands never need to invoke it.

[MIT](LICENSE) · Copyright (c) 2026 Fischer Hunt
