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

!!! example

    <script id="asciicast-xkBRkwC6e9RAzVuMDXH3nGHp7" src="https://asciinema.org/a/xkBRkwC6e9RAzVuMDXH3nGHp7.js" async></script>

It will take a while to download everything,
you can read the [architecture document](../../reference/architecture/overview.md) while waiting for the deployment to complete.
