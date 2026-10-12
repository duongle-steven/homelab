# Development sandbox

The sandbox is a local kind cluster for testing changes on the `dev` branch before they are merged into `master`.

## Prerequisites

- Docker and Nix (see [development shell](../concepts/development-shell.md))
- About 6 GiB of free RAM for Docker (the sandbox uses about 4 GiB)
- Ports `80` and `443` free on the host

## Build

```sh
git switch dev
nix develop
make dev
```

`make dev` creates the kind cluster with Cilium, bootstraps ArgoCD, waits for it, and runs the smoke test.

!!! note

    The first build takes about 15 minutes. Running `make dev` again on an existing sandbox takes under a minute.

## Explore

- <https://argocd.127-0-0-1.nip.io> (user `admin`, password: `kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d`)
- <https://draw.127-0-0-1.nip.io>

On Windows with WSL, open these from the Windows browser.

Certificates are issued by a local CA that is recreated with the cluster. To remove the browser warning, export it and import it into your trust store (on Windows: double-click `homelab-dev-ca.crt` → Install Certificate → Local Machine → Trusted Root Certification Authorities):

```sh
kubectl -n cert-manager get secret homelab-dev-ca -o jsonpath='{.data.ca\.crt}' | base64 -d > homelab-dev-ca.crt
```

## Make changes

ArgoCD in the sandbox syncs from the `dev` branch on GitHub:

```sh
git commit -m "feat: harness the power of the sun"
git push origin dev
```

Only the components listed in `system/argocd/values-dev.yaml` are synced. A component that needs sandbox-specific settings gets a `values-dev.yaml` next to its `values.yaml`. Check how ArgoCD will render it with:

```sh
scripts/render-chart apps/excalidraw dev
```

## Clean up

```sh
make dev-clean
```

## Differences from production

- Certificates come from a local CA instead of Let's Encrypt
- Only accessible from the host machine
- No Alertmanager, Loki, backups, or platform services (yet)
