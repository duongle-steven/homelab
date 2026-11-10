# Argo CD reads what OpenTofu knows from the cluster Secret (step 14g)

## Intent

The bootstrap (`infra/_modules/argocd`) renders `system/argocd` and then rewrites the rendered ApplicationSets with yq
for values that are not in git or are known only when OpenTofu runs:

- `set-revision.sh` points every homelab source and generator at one commit. GitHub Actions uses this through
  `just dev --revision`.
- `set-sandbox-apps.sh` adds the sandbox's optional apps (paperless, opencloud) to the `apps` generator.
- `set-backup-endpoint.sh` sets `global.backupS3Url`, R2's endpoint, which holds the account id and is never in git.
- `choose-values.sh` picks `values-seed.yaml` or `values.yaml`. The seed file restates both ApplicationSets' generators
  and sources only to change the repository. `bootstrap.sh` then replaces its placeholder with `sed`.

This step replaces those rewrites with the GitOps Bridge pattern:

- OpenTofu writes these values as annotations and labels on Argo CD's in-cluster cluster Secret.
- The ApplicationSets read them through a `clusters` generator.

The rendered chart then is what Argo CD runs. A value that changes is one Secret update, not a re-render plus a yq
rewrite.

It is on the roadmap (step 14g). It does not answer a failure: it removes four rewrite scripts and most of
`values-seed.yaml`.

## Spike (sandbox, Argo CD v3.5.5, 2026-11-09)

The spike was a throwaway on the running sandbox: a cluster Secret and two ApplicationSets named `spike-*`, all deleted
afterwards. It found:

- A `clusters` generator selecting the Secret by label, matrixed with a nested matrix (git directories × git files
  `config.json`), passes the Secret's parameters into both inner git generators. `repoURL` and `revision` templated from
  the Secret's annotations work, and so does `valuesObject` (`global.backupS3Url`, `global.baseDomain` from
  `config.json`).
- Changing an annotation updated the generated Applications within 4 seconds. Adding or removing a label added or
  removed an app at once.
- A brace glob in a git files path (`apps/{paperless,opencloud}.yaml`, built from an annotation) matched nothing. One
  generator per optional app, whose `clusters` selector also requires a label `homelab/app-<name>: "true"`, works.
- `index .metadata.annotations "key"` yields `""` for a missing key, silently. Sprig's `required` is not available.
- Dot access with `missingkey=error` makes the ApplicationSet report `ErrorOccurred` for a missing key and keep its
  existing Applications unchanged. This only works for keys without `/` or `-`, e.g.
  `.metadata.annotations.backupS3Url`.
- A Secret that the selector no longer matches makes the ApplicationSet delete every Application it generated. `root`'s
  `preserveResourcesOnDeletion` keeps their resources, but every Application goes until the Secret is back.

## Goals

- **`set-revision.sh`, `set-sandbox-apps.sh` and `set-backup-endpoint.sh` go.** The bootstrap no longer rewrites
  rendered manifests.
- **The bootstrap writes Argo CD's in-cluster cluster Secret before it applies the ApplicationSets:**
  - Name `in-cluster`, namespace `argocd`.
  - Label `argocd.argoproj.io/secret-type: cluster`.
  - `server: https://kubernetes.default.svc`, name `in-cluster`. Existing `destination.name: in-cluster` keeps
    working.
  - One label that every homelab `clusters` generator selects on. The name is the builder's choice, e.g.
    `homelab/environment: <env>`.
  - Annotations, camelCase without a prefix, so templates read them with dot access and fail closed:
    - `repository`: the homelab repository Argo CD syncs: GitHub for the sandbox and the testbed, the seed or
      `ops/homelab` on Forgejo for production.
    - `revision`: `sandbox`, `testbed` or `master`, or the commit `just dev --revision` asks for.
    - `backupS3Url`: production's R2 endpoint. Elsewhere it is an empty string, so the key always exists. The
      sandbox's endpoint stays in `system/velero/values-dev.yaml`.
  - Label `homelab/app-<name>: "true"` for each sandbox optional app that `just dev` asks for, and none otherwise.
- **The ApplicationSets read the Secret:**
  - Every homelab generator and source in `root` and `apps`, in every values file, takes `repoURL` and `revision` /
    `targetRevision` from the annotations.
  - Velero's location already falls back to `values-dev.yaml`'s endpoint when `backupS3Url` is empty
    (`default .Values.backupStorageLocation.s3Url`).
  - The outer matrix is `clusters` × the existing matrix (git directories or files × git files `config.json`).
  - `global.backupS3Url` comes from the annotation in every environment.
  - The sandbox's `apps` gets one extra generator per optional app, selecting on its label.
  - Other repositories' revisions, such as app-template's chart, stay literal.
- **`values-seed.yaml` keeps only what differs on a first install:**
  - The metrics and ServiceMonitors stay off, because there are no Prometheus CRDs yet (`TestArgoCDServiceMonitors`).
  - It no longer restates the generators, the sources or the seed placeholder.
  - `bootstrap.sh` drops the `sed` and its placeholder check.
- **`choose-values.sh` keeps its decision but returns more:**
  - The seed stays until Argo CD's credential reads `ops/homelab`'s `master`, and never comes back once switched.
  - It also returns the repository to write: `seed_repository` or the Forgejo URL.
  - It reads "already switched" from the cluster Secret's `repository` annotation instead of `root`'s generator.
  - A cluster from before 14g has no Secret yet. Falling back to `root`'s generator there keeps production from going
    back to the seed. Whether that fallback is needed is left to the builder: production has never been deployed.
- **`config.json` stays in git and is read through the git files generator as now.** Domain, time zone and addresses
  are public or private-LAN data: the domain is in DNS and Certificate Transparency logs, and the addresses are
  RFC 1918. Moving them into the Secret would hide nothing, since OpenTofu reads them from the same file, and would
  break `scripts/env-values`' offline rendering.

## Non-goals

- Moving `config.json`'s settings into the Secret (see above).
- Changing which components or apps each environment syncs: values-dev and values-testbed keep their allow-lists.
- Letting Argo CD manage itself again, or managing the cluster Secret with Argo CD.
- `applicationsSync: create-update` or any other ApplicationSet deletion policy change: an app directory removed from git
  still deletes its Application.

## Anti-goals

These would make the step a failure even if it works:

- A missing annotation silently rendering an empty repository or revision. Dot access with `missingkey=error` must cover
  `repository` and `revision`.
- Anything that can leave the cluster Secret absent or unmatched on a running cluster, since every Application would be
  deleted:
  - OpenTofu destroying it.
  - The bootstrap applying ApplicationSets before it.
  - A label rename without both sides in one change.
- Production going back to the seed after it switched to Forgejo, including across this upgrade.
- The account id of R2 in git (it stays in OpenTofu's state).

## Constraints

- **Order:** the bootstrap must write the Secret before the ApplicationSets on a new cluster, and before any
  ApplicationSet whose selector changed.
- **Ownership:** the Secret is written with the same server-side apply and field manager as the rest of the bootstrap
  unless the builder finds a reason not to. Nothing else owns it.
- **Rule:** charts and app files hold no IP address or domain (`scripts/check-env-config` checks this). A repository
  URL in values files is fine, as now.
- **Readers that follow the generators' shape must follow the new shape:**
  - `scripts/check-env-config`: every ApplicationSet reads its environment's `config.json`.
  - `cli/internal/guard/proof.go`: the testbed's list, from `values-testbed.yaml`'s generators.
  - `cli/internal/scope/charts.go`: the app-template pin.
  - `infra/testbed/release-paths.yaml`, which lists `values-seed.yaml`.
  - `renovate.json5`'s app-template regex.
  - `scripts/env-values`, which `bootstrap.sh` and `scripts/render-chart` call. It resolves every `{{ .x }}` in
    `valuesObject` against `config.json`, so it must also resolve the Secret's keys, offline as well (`render-chart`
    has no cluster).
  - `test/e2e/testbed_test.go`'s `testbedList`, which reads `generators[0].matrix.generators[0]`.
- **Where values come from:**
  - The sandbox's and the testbed's `repository` annotation comes from `config.json`'s `seed_repository`, the
    GitHub repository. This was my call: the rule keeps settings that depend on where the homelab runs in
    `config.json`, and step 16 names that repository as configuration.
  - The values files' literal GitHub URL goes, since every homelab `repoURL` reads the annotation.
  - `choose-values.sh` no longer finds the Forgejo URL in `values.yaml`'s generator. It gets it from the module, a
    variable set by production's unit.
- **Re-applies:** OpenTofu re-applies when the revision, the optional apps or the backup endpoint change, as now. The
  builder decides whether the Secret gets its own resource, so these changes skip re-rendering the chart, or stays in
  `bootstrap.sh`.
- **Existing clusters:** the sandbox and the testbed are rebuilt freely. An existing sandbox gets the Secret on the next
  `just dev`, which re-applies because the chart files change.

## Tests

- **Offline (`test/offline`), replacing `TestArgoCDRevision`, `TestArgoCDBackupEndpoint` and `TestSetSandboxApps`:**
  - The rendered ApplicationSets take every homelab `repoURL` / `revision` / `targetRevision` from the Secret's
    annotations, in every values file. Other repositories stay literal.
  - Optional apps come only through their labelled generators.
  - The bootstrap writes the Secret before the ApplicationSets.
  - The Secret carries every annotation the templates read.
- **`TestArgoCDChooseValues`** keeps its cases and checks the repository returned. The "switched" case reads the
  annotation.
- **`TestArgoCDServiceMonitors`** still holds for the seed.
- **e2e (`test/e2e`), labelled `sandbox` and `argocd` (and `testbed` if it fits):**
  - The cluster Secret exists with its keys and its selector label.
  - Every Application's homelab source uses its annotations' repository and revision.
- **Sandbox runs:**
  - `just dev` with and without `--apps paperless`, and `--revision <sha>`.
  - `just dev-fresh` once, for a first install with the Secret.
- **Testbed:** `just testbed` once, since its values file changes.
- **Production:** not deployed. The seed switch stays rendered and tested offline only, as now.

## Left to the builder

- The selector label's name and value.
- Whether the Secret is its own `terraform_data` or part of `bootstrap.sh`.
- Whether `choose-values.sh` needs the fallback for a cluster from before 14g.
- How `values-dev.yaml` and `values-testbed.yaml` restate the nested matrix, since Helm replaces lists as before.
- Where the docs change: `docs/installation/sandbox.md`, `docs/how-to-guides/ci.md`, the architecture page on Argo CD
  if one describes the bootstrap, and `CLAUDE.md`:
  - The Sandbox section names `set-revision.sh` and `set-sandbox-apps.sh`.
  - Roadmap step 14g is marked done, and so is 14f-2.
