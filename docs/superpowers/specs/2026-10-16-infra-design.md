# Step 7a: `infra/` with OpenTofu and Terragrunt, bootstrap

Status: design approved in chat 2026-10-16, written spec awaiting review. Branch: `feat/infra` (PR into `dev`).

## Goal

The sandbox is built, bootstrapped and removed by Terragrunt units under `infra/`, in upstream `nixos`'s layout,
and production's Argo CD bootstrap moves there too. `make dev` and `make dev-clean` keep working and run every
sandbox test. Step 7 continues with 7b (environment settings such as the domain in `infra/<env>/`) and 7c (a
`cloudflare` unit replacing `external/`).

## Context

- Today: `metal/Makefile` creates the k3d sandbox (Gateway API CRDs, `k3d cluster create`, kubeconfig, a socat
  proxy recreated on every run because the node address can change) and production nodes with Ansible
  (`boot.yml`, `cluster.yml`, kubeconfig written to `metal/kubeconfig.yaml`). `system/bootstrap.yml` (Ansible,
  `system/Makefile`) renders the `system/argocd` chart (`values-dev.yaml` in the sandbox; in production
  `values-seed.yaml` while Forgejo is not running, else `values.yaml`) and applies it server-side with field manager
  `argocd-controller`. Argo CD then manages itself (`system/argocd` is in the root ApplicationSet).
- `external/` (Terraform, state on upstream's Terraform Cloud workspace) stays until 7c.
- Upstream `nixos`: `infra/root.hcl`, `infra/_modules/<module>`, `infra/<env>/<unit>/terragrunt.hcl`, kubeconfig at
  `infra/<env>/kubeconfig.yaml`; its `argocd` module uses `helm_release` and Argo CD does not manage itself.
- Decided 2026-10-16: keep the ApplicationSet + wrapper chart model (OpenTofu bootstraps, Argo CD pulls everything
  in the cluster from git); state stays local until step 8 (R2, encryption); production nodes stay on Ansible
  until step 11.
- Tools: the dev shell has OpenTofu 1.11.14 (latest 1.13.1, in nixpkgs-unstable) and no Terragrunt (latest
  1.1.6; nixpkgs-unstable has a 1.1.x).

## Decisions

| Topic | Decision |
|---|---|
| Layout | `infra/root.hcl`, `infra/_modules/{k3d,argocd}`, `infra/sandbox/{cluster,argocd}`, `infra/production/argocd` |
| State | Local, `.state/<env>/<unit>/terraform.tfstate` (git-ignored) |
| Kubeconfig | `infra/<env>/kubeconfig.yaml` (git-ignored), replacing `metal/kubeconfig.yaml` |
| Sandbox cluster | Module `k3d`: `terraform_data` + `local-exec` (no stable k3d provider) |
| Production cluster | Unchanged: `make -C metal` (Ansible) until step 11, writing `infra/production/kubeconfig.yaml` |
| Argo CD bootstrap | Module `argocd`: render the chart and apply it server-side once (re-applied only when the chart or values change); no `helm_release` |
| Production safety | `prevent_destroy` on production's bootstrap |
| Tools | OpenTofu and Terragrunt latest from nixpkgs-unstable in the dev shell |

## Design

### Layout

```
infra/
  root.hcl                     # local state, env/unit locals, kubeconfig path
  Makefile                     # check: terragrunt hcl validate, tofu fmt -check
  _modules/
    k3d/                       # sandbox cluster, Gateway API CRDs, proxy; k3d config (from metal/k3d-dev.yaml)
    argocd/                    # bootstrap Argo CD from system/argocd
  sandbox/
    cluster/config.json        # (6d-1)
    cluster/terragrunt.hcl     # module k3d
    argocd/terragrunt.hcl      # module argocd, depends on ../cluster, values-dev.yaml
  production/
    cluster/config.json        # (6d-1)
    argocd/terragrunt.hcl      # module argocd, seed/values selection, prevent_destroy
```

`cluster/` keeps its `config.json` (read by Argo CD's ApplicationSets and later by OpenTofu); in the sandbox it
also holds the unit that creates the cluster.

### Module `k3d`

Inputs: cluster name, k3d config path, Gateway API version, kubeconfig path, proxy bind address. Resources:

- `terraform_data.cluster`: create runs what `metal/Makefile`'s `k3d` target runs (download the Gateway API
  manifest, `k3d cluster create --config`, mount it into k3s's auto-deploy manifests, write the kubeconfig);
  destroy deletes the cluster and the kubeconfig. Replaced when the k3d config or the Gateway API version changes.
- `data.external.node`: the node's address (`docker inspect`).
- `terraform_data.proxy`: the socat proxy for `127.0.0.1:80/443/22`, replaced when the node address changes (so an
  unchanged sandbox applies with no changes); it keeps the existing port guard and its error message. Destroy
  removes it.

### Module `argocd`

Inputs: kubeconfig, values files (relative to `system/argocd`), and for production a seed rule. One
`terraform_data` whose create runs a script that:

1. creates namespace `argocd`;
2. in production, chooses `values-seed.yaml` when no Forgejo pod is running (as `system/bootstrap.yml` does), else
   `values.yaml`;
3. renders `system/argocd` (`helm dependency build`, `helm template --include-crds`) and applies it with
   `kubectl apply --server-side --field-manager argocd-controller --force-conflicts`.

`triggers_replace` hashes the chart directory's files and the chosen values files, so later applies do nothing
unless they change; Argo CD's own sync applies every later change from git. Destroy does nothing in the sandbox
(the cluster is deleted) and is refused in production (`prevent_destroy`).

### Make

- Root `Makefile`: `KUBECONFIG = infra/$(env)/kubeconfig.yaml` (`env` `sandbox` for `make dev`, `production`
  otherwise); `dev` = `sandbox-push` → `terragrunt run --all apply` in `infra/sandbox` (non-interactive) →
  `sandbox-refresh` → `smoke-test`; `dev-clean` = `terragrunt run --all destroy` in `infra/sandbox`; the default
  target = `make -C metal` → `terragrunt run --all apply` in `infra/production` → `external` → tests.
- `metal/Makefile` loses its `dev`, `k3d`, `proxy` and `clean` targets; `metal/k3d-dev.yaml` moves into the module;
  the k3s role writes `infra/production/kubeconfig.yaml`.
- `system/bootstrap.yml` and `system/Makefile` are removed.

### Dev shell

`flake.nix`: `unstable.opentofu` and `unstable.terragrunt` (pinned by `flake.lock`; update `nixpkgs-unstable` if its
Terragrunt is older than the latest release and a newer one is available there).

## Error handling

- Ports 80/443/22 taken, or the k3d node missing: the proxy step fails with today's messages.
- A failed bootstrap leaves the unit's resource tainted; the next apply retries it.
- Docker's `172.28.0.0/16` taken: `k3d cluster create` fails (CLAUDE.md already says how to find the culprit).

## Testing

- Offline (`make -C infra check`, added to the basic checks): `terragrunt hcl validate`, `tofu fmt -check
  -recursive`, and `tofu validate` of each module.
- `make dev-clean; make dev`: a fresh sandbox built entirely by Terragrunt passes every test (98).
- A second `make dev` on the running sandbox: `terragrunt run --all plan` reports no changes before the apply.
- `make dev-clean` leaves no k3d cluster, proxy container, kubeconfig or Docker network `k3d-homelab-dev`.
- Production: `terragrunt run --all plan` in `infra/production` against a throwaway kubeconfig pointing at the
  sandbox shows the bootstrap it would apply (no production cluster exists).

## Docs

- `docs/installation/sandbox.md`, `docs/installation/post-installation.md`: kubeconfig path, `make dev`/`dev-clean`
  via Terragrunt.
- `CLAUDE.md`: roadmap 7 split into 7a (done), 7b, 7c; the sandbox section names `infra/sandbox` instead of
  `metal/Makefile`/`metal/k3d-dev.yaml`; basic checks gain `make -C infra check`; the production checklist notes
  that production's bootstrap through Terragrunt was only planned.
