# Configuration

Open the [development shell](../../concepts/development-shell.md), which includes all the tools needed:

```sh
nix develop
```

Everything that depends on where your homelab runs lives in `infra/production/cluster/config.json`; edit it:

- `domain`: your domain (apps are served as `<app>.<domain>`)
- `zone`: the Cloudflare zone holding it (the domain itself, or its parent for a subdomain such as `testbed.example.com`)
- `timezone`: your time zone (the charts receive it as `global.timezone`)
- `seed_repository`: the Git repository (your fork) the first install copies into Forgejo
- `vip`: the Kubernetes API address; `network.gateway`, `network.prefix_length`: your router and LAN prefix
- `service_vips`: the LAN addresses of the Gateway and wireguard, outside your router's DHCP range

Charts read these from there (`scripts/check-env-config` checks that none is written into a chart or app file). Two settings still need a hand edit, because their charts cannot take them from `config.json`: the domain in `apps/matrix/values.yaml` (Matrix) and the time zone in `system/kured/values.yaml` (automatic reboots). The nodes read the time zone from `config.json`.

!!! example

    ```json title="infra/production/cluster/config.json"
    --8<--
    infra/production/cluster/config.json
    --8<--
    ```

Then list the machines in `infra/production/metal/hosts.json`: the address each node will have (not its current
one: the machines have no operating system yet), the **lowercase, colon separated** MAC address of the interface it
boots from, and its disk (installation erases it; the rest of the disk after the system becomes Ceph's OSD). Set
`init_host` in `infra/production/cluster/config.json` to the node that starts the cluster.

!!! example

    ```json title="infra/production/metal/hosts.json"
    {
      "homelab-1": {"ip": "192.168.0.11", "mac_address": "02:00:00:00:00:01", "disk": "/dev/sda"},
      "homelab-2": {"ip": "192.168.0.12", "mac_address": "02:00:00:00:00:02", "disk": "/dev/sda"},
      "homelab-3": {"ip": "192.168.0.13", "mac_address": "02:00:00:00:00:03", "disk": "/dev/sda"}
    }
    ```

Then review the diff (`git diff`), commit and push the changes.
