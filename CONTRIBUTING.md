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
| `./x docs` | Check local documentation links and guide metadata when present |
| `./x build` | Build `bin/in` |
| `./x check` | Check formatting, vet, tests, docs, and native build |
| `./x release-check [vX.Y.Z]` | Build and inspect four temporary release archives |
| `./x dist vX.Y.Z` | Write local archives, checksums and changelog notes; never publish |

CI runs checks on Linux and macOS, plus cross-platform packaging on Linux.
Cross-compilation is not runtime validation. The scaffold has no login tests yet;
add tests for actual bridge behavior as it is implemented, not metadata smoke tests.

Explain the problem, resulting behavior, and actual validation in each PR.
Update affected docs and [the changelog](CHANGELOG.md). Keep tests offline and
isolated from real SSH configuration and credentials. See [release setup](docs/contributing/releases.md)
for GitHub settings and [security](SECURITY.md) for reporting guidance.
Contributions are licensed under [MIT](LICENSE).
