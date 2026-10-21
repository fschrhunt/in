# Changelog

## Unreleased

- Audit the architecture and move automatic SSH/Mosh bootstrap and reversible
  installation to the first gate. Define callback lifecycle, session ownership,
  failure isolation and evidence-based compatibility boundaries.
- Correct the draft-release flag and job name in the copied release workflow.
- Make reuse of existing SSH authentication and minimal setup explicit design
  requirements; keep a resident laptop service conditional on bootstrap findings.
- Add the proposed implementation plan for automatic remote CLI browser login.
- Set up a Go scaffold, shared local/CI checks, documentation checks, Linux/macOS
  CI, and four-target release archive preparation.
- Login bridging, installation and automatic session integration are not implemented.
