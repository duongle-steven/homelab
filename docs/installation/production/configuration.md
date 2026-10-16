# Configuration

Open the [development shell](../../concepts/development-shell.md), which includes all the tools needed:

```sh
nix develop
```

Everything that depends on where your homelab runs lives in `infra/production/cluster/config.json`; edit it:

- `domain`: your domain (apps are served as `<app>.<domain>`)
- `zone`: the Cloudflare zone holding it (the domain itself, or its parent for a subdomain such as `staging.example.com`)
- `timezone`: your time zone (the charts receive it as `global.timezone`)
- `seed_repository`: the Git repository (your fork) the first install copies into Forgejo
- `vip`: the Kubernetes API address; `network.gateway`, `network.prefix_length`: your router and LAN prefix
- `service_vips`: the LAN addresses of the Gateway and wireguard, outside your router's DHCP range

Charts read these from there (`scripts/check-env-config` checks that none is written into a chart or app file). Three settings still need a hand edit, because their charts cannot take them from `config.json`: the domain in `apps/matrix/values.yaml` (Matrix), and the time zone in `system/kured/values.yaml` (automatic reboots) and `metal/roles/pxe_server/defaults/main.yml` (node installation). Also change `organization` in `external/versions.tf` to your Terraform Cloud organization: `external/` keeps ntfy and the extra secrets there until step 8.

!!! example

    ```json title="infra/production/cluster/config.json"
    --8<--
    infra/production/cluster/config.json
    --8<--
    ```

Then edit the inventory:

- IP address: the desired one, not the current one, since your servers have no operating system installed yet
- Disk: based on `/dev/$DISK`, in my case it's `sda`, but yours can be `sdb`, `nvme0n1`...
- Network interface: usually it's `eth0`, mine is `eno1`
- MAC address: the **lowercase, colon separated** MAC address of the above network interface

!!! example

    ```yaml title="metal/inventories/prod.yml"
    --8<--
    metal/inventories/prod.yml
    --8<--
    ```

Then review the diff (`git diff`), commit and push the changes.
