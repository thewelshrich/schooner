# Private GitHub source access

Schooner can give a Box access to private GitHub repositories without copying
local credentials or sending an OAuth token to the Box. One locally authorized
GitHub Source Account is shared across Boxes, and every connected Box owns a
dedicated Ed25519 SSH keypair.

## Connect

```bash
schooner source connect github --box work-api
```

When authorization is required in an interactive terminal, Schooner prints the
GitHub device URL and one-time code. It also tries to open the URL with `open`
on macOS or `xdg-open` on Linux; browser failure does not hide the URL or stop
authorization. The GitHub App requests only the account-level permission needed
to read and write Git SSH keys.

The access and refresh tokens are stored together as a versioned envelope in
the operating-system credential store. If that store is unavailable, Schooner
warns and keeps the credential only for the current process. It never writes a
plaintext fallback. Expiring access tokens are refreshed and rotated before
use, and a reauthorization resolving to another GitHub account is rejected
while any Box remains bound.

On the Box, Schooner creates:

```text
~/.local/state/schooner/source/github.com/id_ed25519
~/.local/state/schooner/source/github.com/id_ed25519.pub
~/.local/state/schooner/source/github.com/known_hosts
```

The directory is mode `0700`; the private key is mode `0600`. Creation is
atomic, interrupted partial states are reconciled, and symlinks or unsafe
material fail closed. Private material and managed paths never cross the
runtime protocol or appear in command output. The `known_hosts` file is built
from GitHub's HTTPS metadata only after every key matches its advertised
SHA-256 fingerprint.

GitHub receives a public key titled `Schooner / <box-name>`. The title is for
display only; Schooner reconciles and revokes by fingerprint and verified key
ID. Duplicate titles are harmless, and a lost create response is recovered by
listing keys before retrying. The binding remains `connecting` until the Box
successfully verifies SSH access; status never promotes an unverified key, and
a later `source connect` safely repeats verification.

If first-time authorization succeeds but setup fails before Schooner can save a
recoverable Box binding, the new Source Account and credential are removed. If
the credential store prevents that cleanup, a later `source disconnect` with
no binding retries it once Schooner confirms that no other Box is connected.

## Non-interactive use

JSON output or an invocation without an interactive terminal may use and
refresh an existing Source Account. It never opens a browser, prints a new
device code, or prompts. When fresh authorization is needed it exits with a
versioned `authentication_required` error whose reason is
`credentials_missing`.

Explicit non-interactive `source connect` is therefore useful only after a
credential was established in an interactive invocation. It can reconcile or
register a Box key without prompting when that stored credential is usable.

## Inspect and recover

```bash
schooner source status --box work-api
schooner source status --box former-box-name
```

Status reports `not_connected`, `connected`, `action_required`,
`cleanup_pending`, `conflict`, or `unknown`, plus separate local, Box, and
GitHub observations. A GitHub or Box outage returns the facts still available
with a warning. Persisted `connecting`, `disconnecting`, and `cleanup_pending`
checkpoints let later status, connect, or disconnect invocations reconcile an
interruption without duplicating a key or deleting an unverified one.

GitHub organizations may require SAML SSO authorization for the new SSH key.
Schooner returns `authentication_required` with reason `github_saml_sso` and a
safe organization name when Git exposes one. Authorize the
`Schooner / <box-name>` key in GitHub, then retry; Schooner does not automate
SAML authorization.

## Disconnect

```bash
schooner source disconnect github --box work-api
schooner source disconnect github --box work-api --yes
```

Disconnect lists GitHub keys and verifies the recorded fingerprint before any
deletion. It revokes the GitHub key first, then removes the Box files. If
revocation succeeds but the Box cannot be cleaned, the command succeeds with a
security warning and retains `cleanup_pending`; a later status or disconnect
retries only the already-authorized Box cleanup.

After the final Box is fully disconnected, Schooner deletes the Source Account
metadata and keyring credential. `box remove` and `box destroy` never perform
this workflow automatically. They warn first and retain source metadata so the
GitHub key can later be revoked using the former Box name. Private-key cleanup
then waits until that same machine is re-adopted.
