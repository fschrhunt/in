# Security policy

in is in initial development. There are no published releases or supported
maintenance branches yet; fixes target the current development branch.

Do not post credentials, authorization URLs, callback query strings, private
host details or vulnerability details in public issues. Once a GitHub repository
exists and private reporting is enabled, use its Security → Report a vulnerability
feature. Otherwise request a private reporting channel without disclosing details.
No response-time guarantee is currently offered.

The planned bridge trusts your existing SSH host verification and login identity.
A compromised remote account must be treated as capable of requesting browser
opens; the design must constrain that capability and authenticate requests.
Loopback binding alone does not authenticate local users or processes.
Processes running as the same user, and root on either machine, are outside
the proposed isolation boundary. Session IDs prevent routing mistakes; they do
not isolate mutually hostile processes sharing an account. Keep the SSH master
control socket local and expose only the bridge's limited request interface.

The proposed confidentiality contract covers in's logs, diagnostics and stored
state. The relay necessarily carries callback bytes transiently in memory.
Upstream CLI output, browser history and upstream launcher arguments remain
outside in's control; in cannot promise to redact them.

Unintended public listeners, cross-session routing, credential logging, command
injection and forwarding beyond approved destinations are relevant reports.
The runtime is not implemented and has not undergone a security audit.
