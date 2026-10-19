# Deployment

Open the development shell if you haven't already; `metal/` (Ansible, the PXE server) needs its own, which adds
Ansible to the default one:

```sh
nix develop .#metal
```

Build the lab:

```sh
make
```

Yes it's that simple!

!!! note

    OpenTofu installs Argo CD (`infra/production/argocd`), and Argo CD does not manage itself: a change to
    `system/argocd` (Argo CD, its ApplicationSets) reaches the cluster with `make infra env=production`. The first
    install syncs from the seed repository; the first `make infra env=production` after Forgejo runs switches Argo CD to
    `ops/homelab` on Forgejo.

!!! example

    <script id="asciicast-xkBRkwC6e9RAzVuMDXH3nGHp7" src="https://asciinema.org/a/xkBRkwC6e9RAzVuMDXH3nGHp7.js" async></script>

It will take a while to download everything,
you can read the [architecture document](../../reference/architecture/overview.md) while waiting for the deployment to complete.
