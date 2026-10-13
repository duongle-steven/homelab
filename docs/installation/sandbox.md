# Development sandbox

The sandbox is a local kind cluster for testing changes on the `dev` branch before they are merged into `master`.

## Prerequisites

- Linux, or Windows with WSL 2 (NAT or mirrored networking). macOS does not work: the development shell includes Linux-only packages, and Docker runs in a VM there, so the host cannot reach the kind network.
- Docker and Nix (see [development shell](../concepts/development-shell.md))
- About 8 GiB of free RAM for Docker (the sandbox uses about 7 GiB, see [resource usage](#resource-usage))
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
- <https://grafana.127-0-0-1.nip.io> ("Sign in with Dex", or user `admin` with `kubectl -n grafana get secret grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d`)
- <https://registry.127-0-0-1.nip.io> (zot; log in with Kanidm, then create an API key under your user menu and `docker login registry.127-0-0-1.nip.io -u <username> -p <key>`)
- <https://git.127-0-0-1.nip.io> (Gitea; user `gitea_admin`, password: `kubectl -n gitea get secret gitea-admin-secret -o jsonpath='{.data.password}' | base64 -d`; SSH on `172.30.255.200:22` from WSL)
- <https://budget.127-0-0-1.nip.io>, <https://home.127-0-0-1.nip.io>, <https://drop.127-0-0-1.nip.io>, <https://speedtest.127-0-0-1.nip.io>

On Windows with WSL, open these from the Windows browser.

Everything goes through the Gateway API `Gateway` `homelab` (Cilium) at `172.30.255.200`; a localhost proxy forwards `127.0.0.1:80/443` to it. The Argo CD CLI works through it with `--grpc-web`.

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

What each group may do:

| Group | Grafana | Argo CD | zot |
|---|---|---|---|
| `homelab-admins` | Admin, server admin | admin | read, push, delete |
| `homelab-editors` | Editor | view, sync, actions, logs, exec into `apps/*` | read, push |
| `homelab-users` | Viewer | read-only | read |

Argo CD and Grafana keep their local `admin` account; zot has a `ci` account for automation (password in `global-secrets/registry.ci`).

Use the `totpSecret` there in any authenticator app to log in by hand. Kanidm asks for the TOTP code before the password.

If an account has credentials in Kanidm but none in that Secret (for example a run stopped mid-way), the tests cannot log in. Recreate the account so kaniop issues a new reset token:

```sh
kubectl -n kanidm delete kanidmpersonaccount dev-admin  # ArgoCD recreates it
kubectl -n kanidm patch secret identity-test-credentials --type json -p '[{"op":"remove","path":"/data/dev-admin"}]'
```

Group membership in the sandbox is declared in `platform/kanidm/values-dev.yaml`; people added by hand with `scripts/onboard-user` are removed from those groups on the next reconcile.

## Make changes

Work on a feature branch created from `dev`, and run the basic checks before opening a pull request into `dev`:

```sh
git switch dev && git pull
git switch -c feat/harness-the-sun
# ...edit, then for every changed chart or app file:
scripts/render-chart platform/dex
scripts/render-chart platform/dex dev
# offline unit tests (the other tests need a cluster)
(cd test && go vet ./... && go test -run 'TestSmokeAppsForEnv|TestIdentity(HasGroup|TOTPWaitsForNextStep)|TestGatewayRouteReady' ./...)
mkdocs build --strict
git push -u origin feat/harness-the-sun   # then open a PR into dev
```

ArgoCD in the sandbox syncs from the `dev` branch on GitHub, so a change reaches the sandbox once its PR is merged. Then run `make dev` to run the smoke, identity, SSO and Gateway tests against it. `dev` is merged into `master` through periodic pull requests.

Only the components listed in `system/argocd/values-dev.yaml` are synced: system and platform charts in the `root` ApplicationSet, apps in the `apps` ApplicationSet.

- **Apps** are app-template values files, `apps/<name>.yaml`. The `apps` ApplicationSet pins the app-template version and passes the environment: write hosts as `<host>.{{ .Values.global.domain }}`, and attach public apps to `{{ .Values.global.publicGateway }}` (the Cloudflare Tunnel Gateway in production, the LAN Gateway in the sandbox). `apps/matrix/` is the only app that is still a chart.
- **System and platform components** are wrapper charts; one that needs sandbox-specific settings gets a `values-dev.yaml` next to its `values.yaml`.

Check how ArgoCD will render something with:

```sh
scripts/render-chart apps/excalidraw.yaml dev
scripts/render-chart platform/dex dev
```

## Resource usage

Measured on a fresh sandbox with all components running (working-set memory per namespace from Prometheus; the node total from `docker stats`):

| Namespace | RAM |
|---|---|
| kube-system (Kubernetes, Cilium and its Gateway Envoy, CoreDNS) | 2102 MiB |
| argocd | 613 MiB |
| monitoring-system | 601 MiB |
| grafana | 569 MiB |
| cert-manager (with trust-manager) | 170 MiB |
| external-secrets | 146 MiB |
| kanidm | 119 MiB |
| zot | 110 MiB |
| gitea | 103 MiB |
| homepage | 96 MiB |
| actualbudget | 58 MiB |
| pairdrop | 44 MiB |
| local-path-storage | 29 MiB |
| speedtest | 21 MiB |
| dex | 13 MiB |
| excalidraw | 12 MiB |
| **Node total** | **6.95 GiB** |

## Clean up

```sh
make dev-clean
```

## Differences from production

- Certificates come from a local CA instead of Let's Encrypt
- Only accessible from the host machine
- No Alertmanager, Loki, backups, Woodpecker or Renovate
- Gitea runs on SQLite with an in-memory cache, without SSO and without its repository import job
- No Cloudflare Tunnel: excalidraw uses the LAN Gateway `homelab` instead of `homelab-public`
