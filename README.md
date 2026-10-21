# in

Remote CLI login, in your local browser.

**In development.** This repository currently contains the build infrastructure
and [implementation plan](docs/plan.md). The login bridge is not implemented.
The [architecture audit](docs/architecture-audit.md) records the main risks and
the experiments required before committing to the runtime design.

The intended experience: install once on your laptop and server, connect using
ordinary SSH or Mosh, and run your usual CLI commands. When a CLI opens a login
page, in opens your laptop browser and routes any localhost callback back to the
remote CLI. No login wrapper, manual tunnel, or device-flow flag to remember.
The CLI retains its authentication protocol and credential storage.
in will reuse your existing SSH setup through system OpenSSH; it will not require
new keys or read your private keys. Reconnection may still require your normal
SSH authentication.

macOS locally and Linux remotely are the first runtime targets. Support across
CLIs will be measured; universal compatibility is not yet established.

## Development

Go 1.26 or newer and a POSIX shell build the scaffold. Full checks also need
Python 3.9 or newer, with no third-party Python packages.

```sh
./x build
./bin/in --help
./x check
./x release-check
```

See [contributing](CONTRIBUTING.md), [the plan](docs/plan.md),
[release setup](docs/contributing/releases.md), and [security](SECURITY.md).
No installer or published release is available yet. `in` is a shell reserved
word; invoke the scaffold as `./bin/in`, as above. Normal daily login commands
will not require invoking it.

[MIT](LICENSE) · Copyright (c) 2026 Fischer Hunt
