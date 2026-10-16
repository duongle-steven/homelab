# Configuration

Open the [development shell](../../concepts/development-shell.md), which includes all the tools needed:

```sh
nix develop
```

Everything that depends on where your homelab runs lives in `infra/production/cluster/config.json`; edit it:

- `domain`: your domain (apps are served as `<app>.<domain>`)
- `timezone`: for automatic reboots
- `seed_repository`: the Git repository (your fork) the first install copies into Forgejo
- `vip`: the Kubernetes API address; `network.gateway`, `network.prefix_length`: your router and LAN prefix
- `service_vips`: the LAN addresses of the Gateway and wireguard, outside your router's DHCP range

Charts read these from there (`scripts/check-env-config` checks that none is written into a chart or app file). Until step 7c, also change `organization` in `external/versions.tf` to your Terraform Cloud organization.

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

At the end it will show what has changed. After examining the diff, commit and push the changes.
