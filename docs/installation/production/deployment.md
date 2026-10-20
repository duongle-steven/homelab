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

    `make infra env=production` deploys. It refuses anything but `origin/master` checked out without changes whose
    check `checks` passed and whose pull request's check `sandbox` passed (`lab infra forgejo verify`), applies
    `infra/production`, provisions Forgejo's org `ops` (`lab infra forgejo sync`: the users `deploy` and `argocd`,
    `master`'s protection, their tokens in OpenBao), pushes the commit to `ops/homelab` (`lab infra forgejo push`),
    then applies the Argo CD bootstrap again. Argo CD does not manage itself, so a change to `system/argocd` reaches
    the cluster this way too. The first install is the same command: Argo CD reads the seed repository until its
    read-only token reads `ops/homelab`'s `master`, then switches to Forgejo and never back (not even while Forgejo
    restarts). `lab infra forgejo push` alone deploys without touching the infrastructure. Only `deploy` can push to
    `master`, and nobody can force-push or delete it, Forgejo's site admins (`forgejo_admin`, `homelab-admins`)
    included: roll back with a revert through a pull request. `force=1` applies the infrastructure only and deploys
    nothing (a state migration from another commit).

!!! example

    <script id="asciicast-xkBRkwC6e9RAzVuMDXH3nGHp7" src="https://asciinema.org/a/xkBRkwC6e9RAzVuMDXH3nGHp7.js" async></script>

It will take a while to download everything,
you can read the [architecture document](../../reference/architecture/overview.md) while waiting for the deployment to complete.
