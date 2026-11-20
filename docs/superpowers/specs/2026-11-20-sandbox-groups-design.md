# Sandbox groups: the sandbox runs what the selected specs need

Status: designed in chat 2026-11-19/20. Decisions marked *(user)* were made in chat; *(Claude's call)* marks the
builder's own, open to review. Extends 14e (optional apps: paperless and OpenCloud behind a cluster Secret label) to
the whole sandbox. It comes before app delivery's last sandbox runs (`docs/superpowers/specs/2026-11-18-app-delivery-design.md`),
which kept failing on an overloaded sandbox.

## Intent

*(user)* The sandbox (and the testbed) only test what is being built: new features, upgrades, fixes. Nobody uses them.
So a run deploys only what its specs need: the core, plus the groups of components the selected specs exercise and
their dependencies. Everything else stays off. The whole suite still runs, on GitHub Actions' release sandbox before
`master`.

## Why: measured 2026-11-19

During one `just dev --tests delivery` run (66 minutes, the whole platform deployed) the k3d node held 10–11.6 GiB of
its 12.66 GiB. From the moment it passed about 10 GiB, the page cache had no room and the node re-read its files
without pause: **2.5 TB read and 135 GB written in 66 minutes** (0.6–1 GB/s), against about 0.5 MB/s idle. That is the
100% disk the workstation showed, and it explains the sandbox's other failures that day: k3s losing its leases, new
pods' DNS lookups failing, the CI losing webhooks. The pods' memory by namespace while the specs ran (`kubectl top`,
7.5 GiB in all, k3s not counted): monitoring-system 1575 MiB, argocd 956, grafana 481, backstage 402, kyverno 301,
istio-system 294, opentelemetry 270, loki 268, forgejo 244, csi-hostpath 221, cert-manager 202, tempo 176,
external-secrets 169, trivy-operator 163, argo-workflows 154, falco 151, argo-rollouts 149, kargo 148, matrix 133,
velero 129. A delivery run needs none of Grafana, Loki, Tempo, the OpenTelemetry Collectors, Kyverno, the Trivy
Operator, Falco, Velero, Matrix or the apps: about 2.5–3 GiB.

The sandbox also starts again whenever WSL starts (Docker's restart policy), loading the workstation when nobody tests.

## Goals

1. **Groups.** Every system and platform component and every app the sandbox can run belongs to the core or to one
   named group; a group may need other groups. One file says which (`test/scopes.yaml`, beside the labels it already
   maps).
2. **Selection.** `just dev` deploys the core plus the groups the selected specs need (their labels) and those groups'
   needs; `--full` and `just dev-fresh` deploy every group; `--apps` keeps asking for optional apps by name and now
   accepts group names.
3. **Removal.** A group the run does not deploy is removed from the sandbox (its Applications, then its namespaces), as
   14e does for optional apps.
4. **Specs.** A spec's labels decide its groups; a spec run without its group is skipped with a message naming it, and
   fails when the run asked for the group (14e's rule, generalised).
5. **Release unchanged.** GitHub Actions' release, retarget and on-demand sandboxes deploy every group and run the whole
   suite, so `master` is still proven on everything together.
6. **Stopping.** `just dev-stop` stops the sandbox so it stays stopped across WSL restarts; the next `just dev` rebuilds
   it from scratch (the existing rule for a stopped sandbox) with only the groups it needs.
7. **Measured.** A `just dev --tests ci` run (the in-cluster CI's builds: the heaviest load `dev` has; app delivery's
   `--tests delivery` repeats it when it rebases) stays under 75% of the node's memory and shows no thrashing (block
   reads under 50 MB/s on average over the run), recorded in the sandbox guide.

8. **CRDs first.** Every CRD comes from `system/crds`; no `SkipDryRunOnMissingResource` or sync wave remains whose only
   reason was a missing CRD; a fresh sandbox's first sync is measured before and after.

## Non-goals

- Changing what production or the testbed deploy: groups are the sandbox's (`values-dev.yaml`); the testbed keeps its
  layers (12a-2).
- Shrinking components' own resources (Prometheus' retention, limits): a later tuning if a group alone is too large.
- Running groups on demand while specs run: a run's groups are fixed when `just dev` starts.

## Anti-goals

- A spec that silently passes because its component is absent: absent means skipped with a reason, or failed when asked.
- A group list kept in two places that can drift: `test/scopes.yaml` is the only list; `values-dev.yaml`'s generators
  are checked against it (`lab test scope --check`, which `just cli check` runs).
- Losing the whole-platform proof: the release sandbox still deploys everything.

## The groups *(Claude's call, from the components today)*

| Group | Components | Needs |
| --- | --- | --- |
| core (always) | `system/crds` (new: every component's CRDs, below), `argocd-config`, `istio-system`, `cert-manager`, `csi-hostpath`, `platform/external-secrets`, `openbao`, `generated-secrets`, `infra-secrets`, `kanidm`, and every Application's `system/egress` and `system/secret-store` | |
| metrics | `system/monitoring-system` (prometheus-operator, Prometheus, Alertmanager) | |
| observability | `system/loki`, `tempo`, `opentelemetry`, `kiali`, `platform/grafana` | metrics |
| policy | `system/kyverno` | |
| security | `system/trivy-operator`, `falco`, `public-scan` | metrics |
| backup | `system/velero`, `platform/secrets-rotation` | git |
| git | `platform/cloudnative-pg`, `forgejo` | |
| ci | `platform/zot`, `argo-workflows`, `argo-events`, `ci` | git |
| backstage | `platform/backstage` | ci |
| one per app | `apps/<name>.yaml` (paperless and OpenCloud keep their 14e behaviour) | |

App delivery adds `delivery` (`system/argo-rollouts`, `platform/kargo`, `test/fixtures/apps/delivery-test.yaml`;
needs ci, backstage, metrics) when it rebases on this step.

**Every CRD in one component, `system/crds`** *(user, 2026-11-20: all of them in this step, not piecemeal)*.
Components in every group, the core included, carry other components' kinds (Kyverno `PolicyException`s,
prometheus-operator monitors, cert-manager `Certificate`s, ESO `ExternalSecret`s, Istio policies), and Argo CD refuses a
whole sync holding a kind whose CRD is missing. Until now each such dependency was patched where it bit
(`SkipDryRunOnMissingResource` on 33 files, 12 sync waves, a 20-minute first sync with retries); managing CRDs apart from
their controllers is the common GitOps practice that removes the cause (upstream projects ship CRD-only charts for it,
and this repository already installs the Gateway API's CRDs apart). So:

- `system/crds` holds the CRDs of every component that ships some: cert-manager, Istio, external-secrets, kaniop,
  Kyverno, prometheus-operator, the OpenTelemetry Operator, Velero, the Trivy Operator, CloudNativePG, Argo Workflows,
  Argo Events, external-dns, the CSI snapshot CRDs, rook-ceph (and Kargo and Argo Rollouts when app delivery rebases).
  Argo CD's own CRDs stay with `system/argocd`, which the bootstrap applies before Argo CD exists; the Gateway API's stay
  in k3s's manifests.
- Each CRD file is generated from the component's own pinned chart (`scripts/vendor-crds`: the chart rendered with its
  CRDs, the `CustomResourceDefinition`s kept), so the CRDs always match the controller's version by construction; a basic
  check (`crds`) fails when a component's chart changed and its CRDs were not regenerated (a Renovate bump fails it until
  someone runs the script, which the guide says).
- Every component stops installing its CRDs (its chart's toggle where it has one, `skipCrds` in the ApplicationSets'
  Helm sources for charts that keep them in `crds/`).
- `system/crds` syncs first everywhere (it is core in the sandbox, a root Application in production, wave -1); CRDs are
  never pruned by it (`Prune=false`), so deleting it or a mistake in it never deletes every object of a kind.
- The workarounds that existed only for CRD order go: `SkipDryRunOnMissingResource` where a CRD was the reason, the sync
  waves that ordered CRDs before their users, and their explanations in `CLAUDE.md`. Workarounds with another reason stay
  (ESO's webhooks' `failurePolicy: Ignore` waits for an endpoint, not a CRD; monitoring-system's waves order the operator
  before Prometheus).
- With `policy` or `metrics` off, the exceptions and monitors sit inert. The CRDs cost etcd a few MB and no memory.
- Measured: a fresh sandbox's first sync to every Application synced and healthy, before and after (Goal 8).

**Which groups a run gets** *(Claude's call)*: the groups of every label on every spec the selection picks (so a
selected spec never misses its component), plus, when `just dev` compares with a base, the group of every component the
branch changes (14e's rule for paperless and OpenCloud, generalised: a changed component is deployed even if no spec
needs it), plus `--apps`. The apps that were always deployed (Actual Budget, excalidraw, homepage, Matrix, ntfy,
pairdrop, speedtest) become off unless one of those three asks; the smoke spec checks the apps that are deployed.

Which spec labels need which groups is part of the same file: each `test/scopes.yaml` label lists its groups (for
example `sso` needs the groups of the apps whose sign-in it tests, `logs` needs observability, `delivery` needs
delivery). The builder sets the list from the specs; a label that needs nothing beyond the core lists none.

## Design

- `test/scopes.yaml` gains `groups:` (name → components, needs) and a `groups` field per label. `lab test scope`
  gains `--groups`, printing the groups a selection needs (with their needs, the core left out), and `--check` checks
  that every component is in exactly one group or the core and that `system/argocd/values-dev.yaml` gates each group's
  components by the group's label.
- The cluster Secret's label becomes `homelab/group-<name>` (one per deployed group), written by
  `infra/_modules/argocd/cluster-secret.sh` from `HOMELAB_SANDBOX_GROUPS` (the rename of `HOMELAB_SANDBOX_APPS`; 14g's
  three-release label rename is for production's Secret, which has no group labels: the sandbox is rebuilt, so one
  release does).
- `system/argocd/values-dev.yaml`: the core in the generators as today; each group one `matrix` generator selecting
  `homelab/group-<name>: "true"`, listing its directories (`root`) or app files (`apps`).
- `just dev` computes the groups from the selection (`lab test scope --groups`) plus `--apps`, prints them, passes them
  to the bootstrap, and deletes the namespaces of every group left out (14e's loop over every group).
- Specs: `skipUnlessDeployed` takes a group; each top-level `Describe` of a non-core component calls it with its group
  (a small table from label to group, read from `test/scopes.yaml`, so the specs and `just dev` agree).
- `just dev-stop`: `k3d cluster stop` and the localhost proxy stopped; the docs say to run it before closing WSL.

## Testing

- `cli/internal/scope`: groups and needs resolved for a selection; `--check` refuses a component in two groups, in none,
  or ungated in `values-dev.yaml`.
- Offline: `values-dev.yaml`'s generators per group; the cluster Secret's group labels.
- Sandbox: the measured run of Goal 7; `just dev --tests logs` deploys metrics and observability and no security, backup,
  git or app group.
- `TestE2EKeepsSelection` and `TestArgoCDOptionalApps` follow the new layout.

## Documentation

`docs/how-to-guides/sandbox-tests.md` (groups, `--apps`, `just dev-stop`, the measurement), `docs/installation/sandbox.md`,
`CLAUDE.md` (the Sandbox section and 14e), `docs/superpowers/deferred-minors.md`.

## Left to the builder

The exact groups of each label, each chart's way to stop installing its CRDs, the generator layout, the skip helper's shape, and whether a group's removal also
deletes its cluster-scoped leftovers (CRDs and webhooks stay today for optional apps; a webhook left with
`failurePolicy: Ignore` is harmless).
