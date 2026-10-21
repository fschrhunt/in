# Proving login works

Use capability tests for the shared transport, then a small set of real-provider
checks before claiming provider compatibility. Testing every CLI on every change
is unnecessary. A passing fixture cannot establish that every CLI exposes its
browser request or uses an observable callback.

## Everyday checks

Run `./x check`. For transport, session routing or setup changes, also run
`./x integration`. These use isolated fixtures to cover callback bytes, startup
order, port conflicts, privacy, ambiguous sessions, SSH/Mosh/tmux and failures.
CI runs these checks and `./x release-check`; it needs no personal accounts.

## Native OAuth proof

From the Linux checkout, run:

```sh
./x prove --laptop mini --server server --report /tmp/in-proof.json
```

Replace `mini` with the existing SSH alias from Linux to your Mac, and `server`
with the alias from that Mac back to this Linux machine. Both connections must
already work without interactive authentication. The script checks the return
connection reaches this checkout's temporary files. Linux needs Go, Python 3.9+,
SSH and SCP; the Mac needs Python 3.9+ and system SSH. No Go installation is needed
on the Mac: the script builds both binaries from the current Linux checkout.

The script installs into private temporary directories, includes your existing
SSH configuration in a separate test config and creates its own SSH master. It
never edits personal shell or SSH files, reads private keys, forwards an agent,
or uses real provider credentials. Existing hooks that cannot coexist with in
remain a setup failure, rather than being overwritten for the test.

The proof must:

1. Start with an unauthenticated fixture profile and invoke its normal login
   command over ordinary SSH, using the installed browser hooks.
2. Receive the unmodified authorization request on the Mac.
3. Reject a callback with incorrect state, then deliver the correct callback
   from Mac loopback through in to the Linux listener.
4. Exchange the one-use code with its original redirect URI and PKCE verifier.
5. Save fixture credentials with owner-only permissions and make an authenticated
   request from a fresh remote process using those saved credentials.
6. Repeat for dynamic ports, a late listener, a cancelled login followed by a
   same-port retry using `localhost`, and a separate listener process using
   `xdg-open`.
7. Stop in, verify its callback ports are closed and ordinary SSH still works,
   then uninstall temporary integration and remove test-owned resources.

The fake provider is private on Linux. A separate SSH request lets the scripted
Mac browser reach **only the provider** and obtain its redirect. The callback
itself goes to Mac loopback and must travel through in. This avoids an Internet
service, real credentials or manual consent while exercising a complete OAuth
exchange and credential persistence. The fake CLI validates state; the provider
validates PKCE. in continues to relay bytes without implementing OAuth.

Add `--browser` to open a final fixture callback in the Mac's actual default
browser. It requires a logged-in desktop session and leaves a “Login complete”
tab you can close. Authorization is still simulated; this proves the browser's
callback navigation, not a real provider's consent page.

The script returns nonzero on a failed assertion, timeout, missing prerequisite
or cleanup failure. Output and the optional JSON report contain fixed check names,
source commit, dirty-tree flag and duration, never authorization URLs, state,
codes or tokens. Normal browser-launch arguments and the browser history can
contain fixture URLs, just as they do for real CLI logins. A dirty report refers to working-tree code, not just its commit.
On interruption it attempts cleanup. Failed cleanup retains recovery files and
reports their temporary paths. An unreachable Mac or forced process kill
can require manual removal of the test's `/tmp/in-proof.*` directory and its owned
processes; never stop unrelated SSH masters to clean up a proof run.

## Real CLI release checks

`./x compatibility` observes installed CLI launch behavior in isolated profiles.
It does not complete authentication. See [compatibility](compatibility.md) for
executed versions and limits.

Before claiming a provider is supported, use a fresh isolated profile on a real
Mac/Linux pair, run the CLI's normal login, approve consent in the Mac browser,
and verify authenticated status from a new CLI process. Record exact CLI, OS,
SSH and browser versions. Never accept an already-authenticated profile, browser
launch, HTTP callback response, or zero exit status alone as proof of login.
Profile isolation and status commands are CLI-specific; there is no honest
universal script for those steps. Keep that small release check separate from
the generic runtime and deterministic regression suite.
