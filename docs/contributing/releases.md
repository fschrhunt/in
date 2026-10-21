# Release setup

in has no published release or installer. The module path reserves
`github.com/arocomputer/in` as the intended location; this setup does not create
that GitHub repository or configure a remote.

## Local verification

Run `./x check` and `./x release-check`. The latter cross-builds Linux/macOS on
amd64/arm64, checks archive contents and SHA-256 manifests, then removes its
temporary output. These checks do not validate runtime behavior on all targets.
Archives contain the executable, license, README, documentation and policies.
Source builds report `dev`; release builds embed their `vX.Y.Z` version.

For local versioned packaging, add a nonempty `## X.Y.Z` section to
[CHANGELOG.md](../../CHANGELOG.md), then run `./x dist vX.Y.Z`. It refuses to
overwrite `dist/vX.Y.Z` and never creates a tag or publishes anything.
Checksums detect corruption; binaries are unsigned and not notarized.

## GitHub setup when the repository exists

Use `main` as the default branch. Enable Actions and private vulnerability
reporting. Configure required checks `check (ubuntu-latest)`,
`check (macos-latest)`, and `release-check`, and maintainer review.
CODEOWNERS routes review requests; it does not enforce approval.
Action versions follow the pinned revisions in aft; monthly Dependabot proposals
keep them reviewable. There are no external Go dependencies to update yet.

The copied release workflow prepares a draft for an existing stable tag reachable
from main. Leave `RELEASE_ENABLED` unset until a release is ready. Before enabling
it, configure the `release` environment and tag protections, and update all
availability statements to match what will actually ship.

The workflow verifies tag ancestry, checks the selected commit on Linux/macOS,
and builds four archives with read-only permissions. A separate job verifies
checksums and the selected source, then creates a draft with a write token.
It does not publish the draft. Existing releases are never overwritten; inspect
incomplete drafts before removing or retrying them. Never move published tags.
No GitHub settings, secrets, tags or releases are created by local checks.
