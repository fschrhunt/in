# Contributing

Use the fewest moving parts that solve the problem. Read [AGENTS.md](AGENTS.md)
and the [implementation plan](docs/plan.md) before adding runtime behavior.

## Local checks

Go 1.26+, Python 3.9+ (standard library), and a POSIX shell are required for all
checks. Build alone needs Go and a shell.

| Command | Purpose |
| --- | --- |
| `./x fmt` | Format Go source |
| `./x fmt --check` | Check formatting without changing files |
| `./x test` | Run race-enabled Go tests and infrastructure tests |
| `./x integration` | Exercise installed hooks over disposable SSH/Mosh/tmux on Linux |
| `./x clients` | Exercise native ET/Kitty sessions in isolated Linux homes; requires ET, Kitty, Xvfb, bubblewrap and integration prerequisites |
| `./x prove --laptop HOST --server HOST` | Prove complete fixture OAuth logins on a real Mac/Linux pair using existing SSH |
| `./x compatibility` | Observe installed CLI launch shapes in isolated configuration; no provider consent |
| `./x docs` | Check local documentation links and guide metadata when present |
| `./x build` | Build `bin/in` |
| `./x check` | Check formatting, vet, tests, docs, and native build |
| `./x release-check [vX.Y.Z]` | Build and inspect four temporary release archives |
| `./x dist vX.Y.Z` | Write local archives, checksums and changelog notes; never publish |

CI runs checks on Linux and macOS, plus cross-platform packaging on Linux.
Cross-compilation is not runtime validation. Integration tests use a real disposable SSH server and a loopback fixture adapter
to represent two machines on one kernel. See [compatibility](docs/compatibility.md)
for limits and [testing](docs/testing.md) for the repeatable native proof and
real-provider release checks. Avoid metadata smoke tests.

Explain the problem, resulting behavior, and actual validation in each PR.
Update affected docs and [the changelog](CHANGELOG.md). Keep tests offline and
isolated from real SSH configuration and credentials. See [release setup](docs/contributing/releases.md)
for GitHub settings and [security](SECURITY.md) for reporting guidance.
Contributions are licensed under [MIT](LICENSE).
