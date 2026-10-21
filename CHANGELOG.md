# Changelog

## Unreleased

- Share callback/privacy logic behind `callbackTransport.Open`. Bind SSH relays to
  logical IDs stored per ControlPath; retain up to 128 exact remote
  tuple aliases for surviving Mosh shells. Refresh only after an authenticated hook.
- Discover literal parent SSH `-S` and `-o ControlPath` overrides, preserving Kitty's
  default connection sharing and existing client configuration.
- Add an installed ET 7+ function for opted-in hosts with a private reverse Unix session socket and
  standard-library HTTP/2 callback streams. A remote agent starts on browser demand;
  stopping in releases bridge resources while the ET terminal keeps running.
- Route tmux from the sole attached client's owned process environment and enable
  remote hooks for `SSH_CONNECTION` or `IN_SESSION_SOCKET`. Upgrade both ends for
  wire version 3. Add isolated native client proof coverage.
- Fence stop/start races and stop older wire protocols before an upgrade.
- Add a repeatable Mac/Linux OAuth proof with isolated profiles, state and PKCE
  validation, fresh-process authentication checks and verified cleanup.

- Capture PATH-resolved xdg-open requests as well as BROWSER, while preserving
  native file/dev opens, explicit browser overrides and local sessions.
- Pin existing OAuth callback sockets by identity so background services work
  without command-specific adapters. Reject socket replacement; retain ancestor
  checks for late listeners. A new socket can reclaim an idle cancelled login's
  port immediately.

- Limit forwarding to explicit OAuth authorization-code requests and rclone's
  verified direct login entry point; refuse dev-server browser opens without
  reserving laptop ports, opening the browser or disturbing the server.

- Implement the browser/callback bridge using existing authenticated SSH masters,
  private remote browser sockets and bounded raw callback channels.
- Add reversible laptop/remote setup, Mosh startup integration, current tmux routing,
  status, stopping, uninstall and checksum-verified binary installation.
- Preserve original authorization URLs and callback bytes; refuse occupied ports
  and ambiguous remote listeners. Reap owned routes on listener shutdown or expiry.
- Validate disposable real SSH/Mosh callbacks, late listeners, preconnects, distinct
  laptop ownership, existing masters, tmux and unaffected file transfers.
- Validate native Apple Silicon builds and a real macOS browser callback to Linux.
  Exercise actual Wrangler 4.151.2, Claude Code 2.1.305 and rclone 1.75.2 listeners
  through SSH with isolated configuration; also observe Codex 0.163.0 and OpenCode
  2.1.2 browser launch and callback paths. No completed provider login is claimed.
- Add Linux integration CI, focused unit/installer tests and four-target archives.
- Pin late callback listeners to the requesting CLI process ancestry, including detached
  browser openers. Cancel active remote helpers when their transport relay stops.
- Record remaining provider consent, Intel macOS and transport recovery validation.
