# Development sandbox

The sandbox is a local kind cluster for testing changes on the `dev` branch before they are merged into `master`.

## Prerequisites

- Docker and Nix (see [development shell](../concepts/development-shell.md))
- About 7 GiB of free RAM for Docker (the sandbox uses about 5 GiB, see [resource usage](#resource-usage))
- Ports `80` and `443` free on the host

## Build

```sh
git switch dev
nix develop
make dev
```

`make dev` creates the kind cluster with Cilium, bootstraps ArgoCD, waits for it, and runs the smoke test.

!!! note

    The first build takes about 25 minutes. Running `make dev` again on an existing sandbox takes about a minute.

## Explore

`make` uses the sandbox kubeconfig automatically. For `kubectl` in your own shell, point it at the same file first:

```sh
export KUBECONFIG=$PWD/metal/kubeconfig.yaml  # from the repository root
```

- <https://argocd.127-0-0-1.nip.io> (user `admin`, password: `kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d`)
- <https://draw.127-0-0-1.nip.io>

On Windows with WSL, open these from the Windows browser.

Certificates are issued by a local CA that is recreated with the cluster. To remove the browser warning, export it and import it into your trust store (on Windows: double-click `homelab-dev-ca.crt` → Install Certificate → Local Machine → Trusted Root Certification Authorities):

```sh
kubectl -n cert-manager get secret homelab-dev-ca -o jsonpath='{.data.ca\.crt}' | base64 -d > homelab-dev-ca.crt
```

### Identity

Kanidm (<https://auth.127-0-0-1.nip.io>) and Dex (<https://dex.127-0-0-1.nip.io>) run in the sandbox with three groups:

| Group | Test account |
|---|---|
| `homelab-admins` | `dev-admin` |
| `homelab-editors` | `dev-editor` |
| `homelab-users` | `dev-viewer` |
| (none, refused) | `dev-outsider` |

The identity tests give each test account a password and TOTP the first time they run, and store them in the `identity-test-credentials` Secret in namespace `kanidm`:

```sh
kubectl -n kanidm get secret identity-test-credentials -o jsonpath='{.data.dev-admin}' | base64 -d
```

Use the `totpSecret` there in any authenticator app to log in by hand. Kanidm asks for the TOTP code before the password.

Group membership in the sandbox is declared in `platform/kanidm/values-dev.yaml`; people added by hand with `scripts/onboard-user` are removed from those groups on the next reconcile.

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

## Resource usage

Measured with all sandbox components running (RAM per namespace):

| Namespace | RAM |
|---|---|
| kube-system (Kubernetes, Cilium, CoreDNS) | 1944 MiB |
| argocd | 580 MiB |
| monitoring-system | 488 MiB |
| ingress-nginx | 207 MiB |
| kanidm | 117 MiB |
| cert-manager | 106 MiB |
| external-secrets | 103 MiB |
| local-path-storage | 26 MiB |
| excalidraw | 12 MiB |
| dex | 11 MiB |
| **Node total** | **4.95 GiB** |

## Clean up

```sh
make dev-clean
```

## Differences from production

- Certificates come from a local CA instead of Let's Encrypt
- Only accessible from the host machine
- No Alertmanager, Loki, backups, Grafana, zot or the other platform services (yet)
