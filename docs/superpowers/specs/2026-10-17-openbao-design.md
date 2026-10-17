# Step 8a: OpenBao holds the cluster's secrets, read per namespace through External Secrets

Status: design approved in chat 2026-10-16, written spec awaiting review. Branch: `feat/openbao` (PR into `dev`).

## Goal

OpenBao, in the cluster, is where the homelab's secrets live. Each namespace reads only its own branch
(`secret/<namespace>/*`) through a `SecretStore` and External Secrets (ESO); generated secrets are pushed into it once and
never overwritten. OpenBao unseals itself with a key OpenTofu creates and keeps outside the cluster, and configures itself
from git on first start. Step 8 continues with 8b (OpenTofu's secrets, such as Cloudflare's, into OpenBao; `external/`
removed) and 8c (OpenTofu state on Cloudflare R2 with state encryption).

## Context

- Today `platform/global-secrets` generates random secrets with ESO `Password` generators into Kubernetes Secrets in
  namespace `global-secrets`, and a `ClusterSecretStore` `global-secrets` (Kubernetes provider) lets every namespace's
  `ExternalSecret` read any of them. Consumers: Forgejo and Argo CD (`forgejo.admin`), Grafana (`grafana.admin`),
  paperless (`paperless.admin`, `paperless.secret-key`), zot (`registry.ci`), woodpecker (`woodpecker.agent`). Read but
  created elsewhere: `external` (`wireguard-private-key`, `tailscale-auth-key`; `external/`, production only),
  `forgejo.woodpecker`, `forgejo.renovate` (nobody yet; step 9). kaniop creates Kanidm's OAuth2 client secrets in each
  app's namespace, outside this.
- Upstream `nixos` runs OpenBao through bank-vaults (`vault-operator`, which initialises it and keeps its unseal keys and
  root token in a Kubernetes Secret, and `secrets-webhook`, which replaces `bao:` references in Secrets). Its policy `apps`
  lets a namespace's `default` service account read `secret/<namespace>/*`. It has no OpenBao backup yet (a TODO).
- Decided 2026-10-16: keep ESO (OpenBao behind it); no bank-vaults; the unseal key comes from OpenTofu (kept in state,
  outside the cluster); per-namespace access like upstream; a shared chart renders each namespace's `SecretStore`.
- Spike (2026-10-16, sandbox, OpenBao 2.7.1): `seal "static"` with a 32-byte key from a mounted Secret auto-unseals;
  self-initialisation (`initialize` stanzas) enables KV v2, Kubernetes auth, policies and roles on first start (no root
  token is returned, no recovery keys exist); a policy templated on the Kubernetes auth accessor (taken from an earlier
  self-init response) lets namespace A read `secret/A/*` and refuses B; after a restart OpenBao unseals itself, keeps its
  data and does not self-initialise again.
- Found while planning (ESO 2.12 source, `providers/v1/vault/client_push.go`): a `PushSecret` reads the path before it
  writes (403 is an error, only 404 means "absent"), writes `custom_metadata` to `secret/metadata/<path>` first, and the
  data write that follows is then an `update` for OpenBao (KV v2's existence check looks at the metadata). So `writer`
  needs `create`, `read` and `update` on `secret/data/*` and `secret/metadata/*`: the spike's create-only policy cannot
  push through ESO. "Never overwritten" therefore comes from `updatePolicy: IfNotExists`, not from the policy; the
  writer's only holder is namespace `global-secrets`, which already holds every generated value in plain Secrets.
- Amended after the PR's first review (2026-10-17): the writer reached every branch, which 8b's OpenTofu secrets would
  share; it is now limited to `generated/` paths. Audiences, the key guard, the namespace wait as a separate step
  (OpenTofu hides a provisioner's output when its environment is sensitive) and the rotation order came from the same
  review's minors.
- Versions (2026-10-16): OpenBao 2.7.1, chart `openbao` 0.30.4 (`https://openbao.github.io/openbao-helm`).

## Decisions

| Topic | Decision |
|---|---|
| Server | `platform/openbao`: the official chart, standalone, 1 replica, raft storage on a PVC, no injector, no UI route |
| Unseal | `seal "static"`, key from Secret `openbao/openbao-unseal` (file mount), key id `homelab-1` |
| Unseal key | OpenTofu unit `infra/<env>/openbao` (`random_bytes` 32, kept in state), writes the Secret once the namespace exists |
| Configuration | Self-initialisation in the chart's config (git): KV v2 `secret/`, Kubernetes auth, policies `apps`, `writer`, `admin`, roles `apps`, `writer`, `admin` |
| Emergency access | Role `admin` bound to service account `openbao-admin` (namespace `openbao`): `kubectl create token` then log in; no root token exists |
| Reading | Every Application renders `system/secret-store` (extra source, like `system/egress`): `SecretStore` `openbao` in its namespace, Kubernetes auth role `apps` with the namespace's `default` service account |
| Key layout | `secret/<namespace>/<name>`, properties as today (`password`, …); generated secrets under `secret/<namespace>/generated/<name>` |
| Generating | `platform/global-secrets` keeps the `Password` generators; a `PushSecret` per entry writes it to each destination (`updatePolicy: IfNotExists`, checked hourly so a lost destination is pushed again) through a `SecretStore` with role `writer` |
| Writer policy | `create`, `read`, `update` on `secret/data/+/generated/*` and `secret/metadata/+/generated/*` (what ESO's push needs); no `delete`; it never reaches what OpenTofu writes (8b) |
| Audience | Every role requires audience `openbao`; the `SecretStore`s request it (`serviceAccountRef.audiences`) |
| Key protection | One `random_bytes`; production's `protect` adds a `prevent_destroy` guard whose trigger is the key, so a plan that would replace or destroy it fails, and flipping `protect` never changes the key (turning it off needs the guard removed from state first) |
| Shared secrets | One generated value pushed to several branches (Forgejo's admin password to `secret/forgejo/generated/admin` and `secret/argocd/generated/forgejo-admin`) |
| Old store | `ClusterSecretStore` `global-secrets` removed; every `ExternalSecret` moves to `SecretStore` `openbao` |

## Design

### OpenTofu unit `openbao`

`infra/_modules/openbao`: `random_bytes` (32) and a `terraform_data` that waits for namespace `openbao` (created by Argo CD
when it syncs `platform/openbao`) and applies Secret `openbao-unseal` (key `key`, the raw bytes) server-side. The key never
changes unless the resource is replaced (rotation is a later concern: OpenBao's `previous_key`). `infra/sandbox/openbao`
depends on `../argocd`; `infra/production/openbao` likewise, with `prevent_destroy`. Its state now holds the unseal key:
`.state` stays user-only (7c's hook), and 8c moves state to R2 with encryption.

### `platform/openbao`

Wrapper chart over `openbao` 0.30.4: `server.standalone` with the HCL config (listener without TLS inside the mesh;
ztunnel's mTLS carries it), raft storage on a PVC (the chart's `data-openbao-0`, never pruned: a StatefulSet's PVCs are not Argo CD's), the unseal Secret mounted read-only,
`injector.enabled: false`, `ui: false`. The config's `initialize` stanzas, in order:

1. `mounts`: `sys/mounts/secret` (kv v2), `sys/auth/kubernetes`, `auth/kubernetes/config`, a read of `sys/auth/kubernetes`
   (its accessor);
2. `policies`: `apps` (templated with the accessor: `read` on `secret/data/{{identity.entity.aliases.<accessor>.metadata.service_account_namespace}}/*`),
   `writer` (see Decisions), `admin` (everything);
3. `roles`: `apps` (service account `default` in any namespace), `writer` (`default` in `global-secrets`), `admin`
   (`openbao-admin` in `openbao`).

Also: the `openbao-admin` ServiceAccount; `system:auth-delegator` for the server's service account (TokenReview); the
namespace's `allow` AuthorizationPolicy (External Secrets' controller on 8200; same namespace); egress needs nothing new
(the API server is in the baseline).

### `system/secret-store`

A chart every Application renders as an extra source (like `system/egress`, same `exclude`), producing in the
Application's namespace:

```yaml
apiVersion: external-secrets.io/v1
kind: SecretStore
metadata:
  name: openbao
spec:
  provider:
    vault:
      server: http://openbao.openbao.svc.cluster.local:8200
      path: secret
      version: v2
      auth:
        kubernetes:
          mountPath: kubernetes
          role: apps
          serviceAccountRef:
            name: default
```

It renders only once ESO's CRDs exist (capability gate, like the Argo CD templates of 7b; `scripts/render-chart` passes the
API).

### `platform/global-secrets`

Each entry gains `destinations` (namespace/name paths); the generator and its Kubernetes Secret stay; a `PushSecret`
(`refreshInterval` 1h, `updatePolicy: IfNotExists`, `deletionPolicy: None`: an existing value is never replaced, a
missing one, such as after OpenBao lost its data, is pushed again) pushes the Secret to each destination through
`SecretStore` `openbao-writer` (role `writer`, rendered by the chart itself). The `ClusterSecretStore` and its RBAC are
removed.

| Entry | Destinations |
|---|---|
| `forgejo.admin` | `forgejo/generated/admin`, `argocd/generated/forgejo-admin` |
| `grafana.admin` | `grafana/generated/admin` |
| `paperless.admin`, `paperless.secret-key` | `paperless/generated/admin`, `paperless/generated/secret-key` |
| `registry.ci` | `zot/generated/ci` |
| `woodpecker.agent` | `woodpecker/generated/agent` |

### Consumers

Every `ExternalSecret` that read `ClusterSecretStore` `global-secrets` reads `SecretStore` `openbao` with the new key
(`key: admin`, `property: password`, …): `platform/forgejo`, `system/argocd` (`forgejo-repository`), `platform/grafana`,
`platform/zot`, `apps/paperless.yaml`, `platform/woodpecker`, `platform/renovate`, `apps/wireguard.yaml`,
`apps/tailscale.yaml`. The keys nobody creates yet keep their new paths (`woodpecker/forgejo`, `renovate/forgejo`,
`wireguard/private-key`, `tailscale/auth-key`): 8b and step 9 write them.

## Error handling

- The unseal Secret missing: the OpenBao pod waits (mount not optional); the OpenTofu unit times out with a message if
  namespace `openbao` never appears.
- A self-initialisation request failing: OpenBao logs it and stops initialising; nothing reads secrets; the sandbox tests
  fail at once. Fix the config and recreate OpenBao's storage (the sandbox: `make dev-clean`).
- A namespace reading another's branch: OpenBao refuses; ESO marks the `ExternalSecret` not ready.
- Losing the cluster: the unseal key survives in OpenTofu's state; backups of OpenBao's data (raft snapshots) come in step
  10, which must restore before self-initialisation runs on a new install.

## Testing

- Offline: renders of every changed component; `scripts/check-mesh-policies` (namespace `openbao`'s allow), `scripts/check-secret-stores`
  (every `ExternalSecret` reads `SecretStore` `openbao` with keys under its namespace, generators aside; only
  `platform/global-secrets` pushes, through `openbao-writer`; no `ClusterSecretStore`), `make -C infra check`.
- `test/secrets_test.go` (sandbox): OpenBao is initialised and unsealed; after its pod is deleted it comes back unsealed
  with the same data; a temporary pod in one namespace logs in with role `apps` and reads its own branch but not another
  namespace's, and cannot write even its own; the `admin` role (service account `openbao-admin`) lists the policies;
  Grafana's admin Secret (synced from OpenBao) equals the generated one in
  `global-secrets`.
- `make dev-clean; make dev`: every existing test passes (SSO, Forgejo seed, zot CI login read secrets through OpenBao now).

## Docs

- `docs/concepts/secrets-management.md`: OpenBao, per-namespace branches, generating, the unseal key, emergency access.
- `CLAUDE.md`: roadmap 8 split into 8a (done), 8b, 8c; rules: a component reads `SecretStore` `openbao` with keys under its
  namespace; a shared secret is pushed to each reader's branch; the production checklist: OpenBao's data has no backup
  until step 10, and its unseal key lives in `.state` until 8c.
