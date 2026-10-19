# External resources

!!! info

    These resources are optional, the homelab still works without them but will lack some features like trusted certificates and offsite backup

Although I try to keep the amount of external resources to the minimum, there's still need for a few of them.
Below is a list of external resources and why we need them (also see some [alternatives](#alternatives) below).

| Provider        | Resource  | Purpose                                                                                                     |
| --------        | --------  | -------                                                                                                     |
| Cloudflare      | DNS       | DNS and [DNS-01 challenge](https://letsencrypt.org/docs/challenge-types/#dns-01-challenge) for certificates |
| Cloudflare      | Tunnel    | Public services to the internet without port forwarding                                                     |
| ntfy.sh         | Relay     | Instant push to iOS for the self-hosted ntfy (poll requests only, no message content)                       |

## Create credentials

You'll be asked to provide these credentials on first build.

### Cloudflare

`infra/production/cloudflare` (OpenTofu, `infra/_modules/cloudflare`) creates the Tunnel, its DNS record
`homelab-production-tunnel.<domain>` and two DNS tokens (cert-manager's DNS-01 challenge and external-dns), for the
`domain` and `zone` in `infra/production/cluster/config.json`. Their secrets are the unit's sensitive output `secrets`
(`infra/production/secrets` writes them into namespace `infra-secrets`, from where they reach OpenBao;
[secrets management](../../concepts/secrets-management.md#secrets-from-opentofu)).

1. Buy a domain on Cloudflare, or [transfer it to Cloudflare](https://developers.cloudflare.com/registrar/get-started/transfer-domain-to-cloudflare); set `domain` and `zone` in `config.json`.
2. Enable **Zero Trust** (any team name, the Free plan) so the account can create tunnels.
3. Enable **R2** (R2 Object Storage in the dashboard; it asks for a payment method even on the free tier, which
   OpenTofu's state stays far below).
4. `terragrunt plan` in `infra/production/cloudflare`, then `terragrunt apply` (`make infra env=production` applies every
   production unit).

### Cloudflare login

OpenTofu takes the account and an OAuth token from `cf`, Cloudflare's CLI (in the dev shell), and `cf` refreshes the
token. Log in once, with no `CLOUDFLARE_API_TOKEN` in the environment (`cf` would use it instead):

```sh
env -u CLOUDFLARE_API_TOKEN cf auth login --no-browser
```

On the consent page, enable **Account API Tokens** (it is off by default): OpenTofu manages the DNS tokens and `lab`
creates the expiring R2 credentials with it. `cf auth whoami` must list `account_api_tokens:create`; otherwise run
`cf auth login --force` and enable it.

`--no-browser` prints a link and a code (handy under WSL). `CLOUDFLARE_ACCOUNT_ID` picks the account when the login
sees several. `make -C infra check` needs no login.

### State passphrase

Every OpenTofu state is encrypted (OpenTofu's state encryption, PBKDF2 and AES-GCM) with `HOMELAB_STATE_PASSPHRASE`,
which `.envrc` loads from `~/.config/homelab/state.env`:

```sh
sh -c 'umask 077; mkdir -p ~/.config/homelab; printf "export HOMELAB_STATE_PASSPHRASE=%s\n" "$(head -c 32 /dev/urandom | base64)" > ~/.config/homelab/state.env'
direnv allow
```

Keep a copy in a password manager. Losing it makes every state unreadable, including OpenBao's unseal key and the
WireGuard private key, and with the unseal key every secret in OpenBao.

A state written before encryption (an older sandbox, or production before step 8c) is refused; run the first command
once with `HOMELAB_STATE_MIGRATE=1` (for example `HOMELAB_STATE_MIGRATE=1 make dev`), which reads it unencrypted and
writes it encrypted.

### Rotating the passphrase

1. Set `HOMELAB_STATE_PASSPHRASE` to the new passphrase and `HOMELAB_STATE_PASSPHRASE_PREVIOUS` to the old one.
2. Run `scripts/rotate-state-passphrase production` (and `sandbox`). It reads every unit's state with the old
   passphrase before it writes any, so a wrong old passphrase changes nothing; for production it names the Cloudflare
   account first. An interrupted rotation can be rerun with the same variables.
3. Update the password manager and `state.env`, then unset `HOMELAB_STATE_PASSPHRASE_PREVIOUS`.

### Where state lives

Production's state is on R2 through its S3 API (OpenTofu's `s3` backend): bucket `homelab-production-tfstate` (created
on the first `init` by `lab infra state ensure`), one object per unit (`<unit>/tfstate.json`) and a lock file
(`<unit>/tfstate.json.tflock`) while a command runs. Its credentials last one hour and come from `lab infra state
credentials` (an account token for that bucket only, created with the `cf` login), so no token is written to disk; `lab`
keeps the current one in `$XDG_RUNTIME_DIR` (memory-backed, user-only) and reuses it while it lasts 15 more minutes.
`lab` runs with `go run`: after a fresh clone or a dependency bump, build it once (`go -C cli build ./cmd/lab`; `make
infra` does) so the first credential is not cut off by a cold build. An interrupted command can leave the lock behind:
run `terragrunt force-unlock <ID>` in the unit with the ID OpenTofu prints. The sandbox's state stays in
`.state/sandbox/` (git-ignored, readable by the user only). Both are encrypted.

### ntfy

Nothing to create: ntfy runs in the cluster ([alerts](../../how-to-guides/alerts.md)); iOS notifications go through
ntfy.sh's free relay.

## Alternatives

To avoid vendor lock-in, each external provider must have an equivalent alternative that is easy to replace:

- Cloudflare DNS:
    - Update cert-manager and external-dns to use a different provider
    - [Alternate DNS setup](../../how-to-guides/alternate-dns-setup.md)
- Cloudflare Tunnel:
    - Use port forwarding if it's available
    - Create a small VPS in the cloud and utilize Wireguard to route traffic via it
    - Access everything via VPN
    - See also [awesome tunneling](https://github.com/anderspitman/awesome-tunneling)
- ntfy.sh (iOS relay):
    - Android only, or an Apple push setup of your own (see ntfy's docs on `upstream-base-url`)
    - Any other [integration supported by Grafana Alerting](https://grafana.com/docs/grafana/latest/alerting/alerting-rules/manage-contact-points/integrations/#list-of-supported-integrations)
