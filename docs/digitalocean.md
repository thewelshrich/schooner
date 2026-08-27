# DigitalOcean

Schooner can provision an Ubuntu Droplet, prepare it as a Box, and later destroy
the verified provider resource. Provisioning creates billable infrastructure.

## Connect a credential profile

Connect a named profile with an interactively entered Personal Access Token:

```bash
schooner provider connect digitalocean personal --default
schooner provider list
```

For CI or another non-interactive environment, export `DIGITALOCEAN_TOKEN`.
Schooner never accepts the token as a command-line flag and never saves an
environment-provided token implicitly.

A Full Access token is the simplest option. Custom-scoped tokens need account
and catalogue reads, Droplet create/read/delete, SSH-key create/read/delete,
VPC read, and tag create permissions.

## Provision a Box

Run the guided add flow and choose DigitalOcean, or provide the complete
billable configuration explicitly:

```bash
schooner box add

schooner box add work-cloud \
  --provider digitalocean \
  --profile personal \
  --region fra1 \
  --size s-1vcpu-1gb \
  --image ubuntu-24-04-x64 \
  --yes \
  --accept-new-host-key
```

Schooner generates a dedicated local Ed25519 identity for provider-created
boxes. The guided flow separately offers public keys discovered beneath
`~/.ssh` and keys already registered with the DigitalOcean account. Selected
local public keys are registered only long enough for Droplet creation;
private keys are never read or uploaded.

Creation is correlated and recoverable, so retrying the same interrupted
`box add` does not blindly create another Droplet. Supplying only the previous
uncompleted name resumes the recorded selections:

```bash
schooner box add work-cloud
schooner box add work-cloud --yes --accept-new-host-key
schooner box ssh work-cloud
```

## Remove or destroy

Local removal and infrastructure destruction are deliberately separate:

```bash
schooner box remove work-cloud --yes   # local inventory only
schooner box destroy work-cloud --yes  # permanent Droplet deletion
```

`box destroy` verifies the provider resource against Schooner's recorded
identity before requesting deletion.

Disconnect GitHub source access before either command when it is configured:

```bash
schooner source disconnect github --box work-cloud --yes
```

Box removal and destruction never make automatic GitHub calls. Source binding
metadata is retained so a key can still be revoked by the former Box name;
Box-file cleanup remains pending until the same machine is re-adopted.

Disconnecting a profile removes its locally stored secret but retains safe
metadata so referenced boxes can be reconnected without changing accounts:

```bash
schooner provider disconnect digitalocean/personal --yes
```
