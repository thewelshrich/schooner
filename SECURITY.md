# Security Policy

Schooner manages SSH connections, remote development machines, credentials,
software installation, and provider-created infrastructure. Please report
suspected vulnerabilities privately so they can be investigated before public
disclosure.

## Supported versions

Security fixes are made against the latest published release and the `main`
branch. Older releases do not receive separate security updates.

| Version | Supported |
| --- | --- |
| Latest published release | Yes |
| `main` | Yes |
| Older releases | No |

## Reporting a vulnerability

Use GitHub's **Report a vulnerability** form in the repository's
[Security advisories](https://github.com/thewelshrich/schooner/security/advisories)
area. Do not open a public issue or discussion for a suspected vulnerability.

Include, when available:

- the affected Schooner version and platform;
- the security boundary or operation involved;
- the smallest reproducible sequence of commands;
- the impact you believe is possible;
- any suggested mitigation; and
- redacted logs or diagnostic output.

Remove tokens, private keys, credentials, hostnames, IP addresses, repository
URLs, usernames, and other identifying infrastructure details unless they are
strictly necessary to understand the report. GitHub's private report is still
not an appropriate place to send working production credentials.

The maintainer aims to acknowledge a report within five business days. Response
and remediation time depends on severity and complexity; this is a volunteer
project and does not offer a guaranteed response-time agreement. The maintainer
will coordinate disclosure with the reporter and will credit the reporter when
requested and appropriate.

## Scope

Reports are especially useful when they concern:

- SSH host-trust validation or managed SSH configuration;
- credential or secret storage and redaction;
- release, installer, updater, or artifact integrity;
- unintended remote command execution;
- authorization of destructive provider or machine operations;
- path traversal, archive extraction, or unsafe filesystem behavior; or
- privilege boundaries between the local CLI, a Box, and external providers.

General bugs, feature requests, and support questions belong in the repository's
normal issue channels described in [SUPPORT.md](SUPPORT.md).
