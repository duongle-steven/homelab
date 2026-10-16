# External resources

!!! info

    These resources are optional, the homelab still works without them but will lack some features like trusted certificates and offsite backup

Although I try to keep the amount of external resources to the minimum, there's still need for a few of them.
Below is a list of external resources and why we need them (also see some [alternatives](#alternatives) below).

| Provider        | Resource  | Purpose                                                                                                     |
| --------        | --------  | -------                                                                                                     |
| Terraform Cloud | Workspace | State of `external/` (ntfy, extra secrets) until step 8                                                     |
| Cloudflare      | DNS       | DNS and [DNS-01 challenge](https://letsencrypt.org/docs/challenge-types/#dns-01-challenge) for certificates |
| Cloudflare      | Tunnel    | Public services to the internet without port forwarding                                                     |
| ntfy            | Topic     | External notification service to receive alerts                                                             |

## Create credentials

You'll be asked to provide these credentials on first build.

### Create Terraform workspace

Terraform is stateful, which means it needs somewhere to store its state. Terraform Cloud is one option for a state backend with a generous free tier, perfect for a homelab.

1. Sign up for a [Terraform Cloud](https://cloud.hashicorp.com/products/terraform) account
2. Create a workspace named `homelab-external`, this is the workspace where your homelab state will be stored.
3. Change the "Execution Mode" from "Remote" to "Local". This will ensure your local machine, which can access your lab, is the one executing the Terraform plan rather than the cloud runners.

If you decide to use a [different Terraform backend](https://www.terraform.io/language/settings/backends#available-backends), you'll need to edit the `external/versions.tf` file as required.

### Cloudflare

`infra/production/cloudflare` (OpenTofu, `infra/_modules/cloudflare`) creates the Tunnel, its DNS record
`homelab-production-tunnel.<domain>` and two DNS tokens (cert-manager's DNS-01 challenge and external-dns), for the
`domain` and `zone` in `infra/production/cluster/config.json`. Their secrets are the unit's sensitive output `secrets`
(they reach the cluster through OpenBao, step 8).

1. Buy a domain on Cloudflare, or [transfer it to Cloudflare](https://developers.cloudflare.com/registrar/get-started/transfer-domain-to-cloudflare); set `domain` and `zone` in `config.json`.
2. Enable **Zero Trust** (any team name, the Free plan) so the account can create tunnels.
3. Create an API token (My Profile → API Tokens → Create Custom Token) scoped to your account and the zone:

    | Type | Permission | Access |
    |---|---|---|
    | Account | Cloudflare Tunnel | Edit |
    | Account | Account API Tokens | Edit |
    | Account | Account Settings | Read |
    | Zone | Zone | Read |
    | Zone | DNS | Edit |

4. Keep it outside the repository; `.envrc` loads it:

    ```sh
    mkdir -p ~/.config/homelab && printf 'Token: ' && read -rs t \
      && printf 'export CLOUDFLARE_API_TOKEN=%s\n' "$t" > ~/.config/homelab/cloudflare.env \
      && chmod 600 ~/.config/homelab/cloudflare.env
    ```

5. `cd infra/production/cloudflare && terragrunt plan`, then `terragrunt apply` (`make` applies every production unit).

The unit's state (`.state/`, git-ignored) holds the tokens and the tunnel secret until step 8 moves state to
encrypted R2.

### ntfy

- Choose a topic name like <https://ntfy.sh/random_topic_name_here_a8sd7fkjxlkcjasdw33813> (treat it like you password)

## Alternatives

To avoid vendor lock-in, each external provider must have an equivalent alternative that is easy to replace:

- Terraform Cloud:
    - Any other [Terraform backends](https://www.terraform.io/language/settings/backends)
- Cloudflare DNS:
    - Update cert-manager and external-dns to use a different provider
    - [Alternate DNS setup](../../how-to-guides/alternate-dns-setup.md)
- Cloudflare Tunnel:
    - Use port forwarding if it's available
    - Create a small VPS in the cloud and utilize Wireguard to route traffic via it
    - Access everything via VPN
    - See also [awesome tunneling](https://github.com/anderspitman/awesome-tunneling)
- ntfy:
    - [Self-host your own ntfy server](https://docs.ntfy.sh/install)
    - Any other [integration supported by Grafana Alerting](https://grafana.com/docs/grafana/latest/alerting/alerting-rules/manage-contact-points/integrations/#list-of-supported-integrations)
