# Argo CD reads the cluster Secret (step 14g) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the bootstrap's yq rewrites of the rendered ApplicationSets.

**Architecture:** OpenTofu's Argo CD bootstrap writes Argo CD's in-cluster cluster Secret, which carries the repository, the revision, the backup endpoint and one label per optional app. Both ApplicationSets read it through a `clusters` generator, which wraps the existing matrix.

**Tech Stack:** Argo CD v3.5.5 ApplicationSets (Go templates), Helm, POSIX sh plus yq and jq, OpenTofu and Terragrunt, Go (testify offline, Ginkgo e2e).

**Spec:** `docs/superpowers/specs/2026-11-09-argocd-cluster-secret-design.md`

## Global Constraints

- **The Secret:**
  - `name: in-cluster` in namespace `argocd`.
  - Labels `argocd.argoproj.io/secret-type: cluster` and `homelab/bootstrap: "true"`; the second is the selector of every homelab `clusters` generator.
  - `stringData`: `name: in-cluster`, `server: https://kubernetes.default.svc`.
- **Annotations,** camelCase with no prefix, each always present:
  - `repository`
  - `revision`
  - `backupS3Url` (empty string outside production)
- **Optional apps:** label `homelab/app-<name>: "true"` for each optional app asked, and none otherwise.
- **Templates:** every template reads the Secret with dot access (`{{ .metadata.annotations.repository }}`), never `index`. Both ApplicationSets keep `missingkey=error`.
- **Bootstrap order:** the bootstrap applies the Secret before the CRDs and the ApplicationSets, server-side, with field manager `argocd-controller`.
- **Removal:** nothing deletes the Secret. The module has no destroy action.
- **Repositories:**
  - The sandbox's and the testbed's repository is `config.json`'s `seed_repository`.
  - Production's is `http://forgejo-http.forgejo:3000/ops/homelab`, or its seed before the switch.
- **Branches:** `sandbox`, `testbed`, `master`. `revision` (`just dev --revision`) overrides the branch.
- **R2's endpoint never goes in git.**
- **`config.json`** is still read through the git files generator with `pathParamPrefix: config`.
- **`values-seed.yaml`** keeps only the metrics switches.
- **Commits** end with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01DkFEsNwJMWzH561hFWiUzi
  ```
  Never commit to `dev` or `master`. Run `just dev` only from `/home/nixos/homelab`, never beside the testbed.

## Review Focus

1. **`just dev --apps paperless` followed by a plain `just dev`:** the `homelab/app-paperless` label must go. The server-side apply drops labels its manager no longer sets, so the Application goes too. Task 6 checks it on the sandbox.
2. **`just dev --revision <sha>` followed by a plain `just dev`:** the `revision` annotation must return to `sandbox`. Task 6 checks it, and Task 2's test covers the script.
3. **A fresh cluster:** the Secret must exist before any ApplicationSet. Otherwise the generators match nothing, and a later Secret only creates Applications, which is harmless. Re-applying with a renamed selector would delete them all. Task 2's order test pins it.
4. **A switched production while Forgejo is down:** it must never go back to the seed. Task 1's cases cover it.
5. **A template naming an annotation the Secret does not write:** this must fail at once, not at the cluster. Task 4's test checks that every `.metadata.annotations.X` in the rendered ApplicationSets is one the Secret script writes.

---

### Task 1: choose-values.sh decides the repository as well

**Files:**
- Modify: `infra/_modules/argocd/choose-values.sh`
- Test: `test/offline/ci_scripts_test.go` (`TestArgoCDChooseValues`)

**Interfaces:**
- Consumes: nothing.
- Produces:
  - stdin JSON `{values, environment, seed, kubeconfig, repository, seed_repository}`. `forgejo_repository` is gone; `repository` replaces it.
  - stdout `{values, environment, repository}`.
  - "Switched" means the cluster Secret's `repository` annotation equals `repository`.

- [ ] **Step 1: Update `TestArgoCDChooseValues`.**
  - The fake kubectl answers `*"get secret in-cluster"*` with `$ROOT_REPO`, the annotation, in place of the `applicationset` case.
  - The case `"first install, no Argo CD CRDs"` becomes `"first install, no cluster Secret"`: an empty answer and `seedValues`.
  - Inputs:
    - `repository = forgejo`.
    - `seed_repository = "https://github.com/example/homelab"`.
  - Expected outputs:
    - `seedValues = {"values":"values-seed.yaml","environment":"seed","repository":"https://github.com/example/homelab"}`.
    - `forgejoValues = {"values":"values-dev.yaml","environment":"dev","repository":"` + forgejo + `"}`.
  - Keep the other cases, including `"switched, Forgejo down: stay"` and `"cluster unreachable"` (fails).

- [ ] **Step 2: Run the test and confirm it fails.**
  Run `cd test && go test ./offline -run TestArgoCDChooseValues`. Expected: FAIL, since the output has no `repository`.

- [ ] **Step 3: Change `choose-values.sh`.**
  - Read the switch from `kubectl -n argocd get secret in-cluster --ignore-not-found -o jsonpath='{.metadata.annotations.repository}'`. A missing namespace or Secret answers empty, which is a first install; any other error fails.
  - Print `repository`:
    - `seed_repository` while on the seed;
    - otherwise `repository`.
  - Without a seed, never call kubectl (the sandbox case).

- [ ] **Step 4: Run the test and confirm it passes.**
  Same command. Expected: PASS.

- [ ] **Step 5: Commit** with the message `feat(argocd): choose-values.sh picks the repository too, from the cluster Secret`. Task 2 changes `main.tf`'s query in the same branch, before anything runs it.

### Task 2: the bootstrap writes the cluster Secret first

**Files:**
- Create: `infra/_modules/argocd/cluster-secret.sh`
- Modify:
  - `infra/_modules/argocd/bootstrap.sh`
  - `infra/_modules/argocd/main.tf`
  - `infra/sandbox/argocd/terragrunt.hcl`, `infra/testbed/argocd/terragrunt.hcl` and `infra/production/argocd/terragrunt.hcl`
- Test: `test/offline/argocd_cluster_secret_test.go` (new)

**Interfaces:**
- Consumes: Task 1's stdin and stdout.
- Produces:
  - `sh infra/_modules/argocd/cluster-secret.sh` prints the Secret's YAML. It reads:
    - env `REPOSITORY` (required);
    - `BRANCH` (required);
    - `REVISION` (optional, overrides `BRANCH`);
    - `BACKUP_S3_URL` (optional);
    - `OPTIONAL_APPS` (space-separated, deduplicated).
  - `bootstrap.sh` applies it first.
  - Module variables:
    - `repository` (string, required);
    - `branch` (string, required);
    - the existing `seed_repository`, `revision`, `backup_s3_url` and `optional_apps`.
  - `local.environment` gains `REPOSITORY` (from `data.external.values.result.repository`) and `BRANCH`. `triggers_replace` adds the chosen repository and the branch.

- [ ] **Step 1: Write `TestArgoCDClusterSecret`.**
  - Run `cluster-secret.sh` with `REPOSITORY=https://github.com/example/homelab BRANCH=sandbox OPTIONAL_APPS="paperless paperless"`.
  - Assert, with yq on the output:
    - kind `Secret`, name `in-cluster`, namespace `argocd`;
    - both labels from Global Constraints;
    - annotations `repository`, `revision=sandbox` and `backupS3Url=""`;
    - exactly one optional label, `homelab/app-paperless: "true"`;
    - `stringData.server == https://kubernetes.default.svc`.
  - With `REVISION=abc123`, `revision` is `abc123`.
  - Without `REPOSITORY`, or without `BRANCH`, it exits non-zero.

- [ ] **Step 2: Write `TestArgoCDBootstrapAppliesSecretFirst`.**
  - Run `bootstrap.sh` against the real chart, `CHART=$root/system/argocd`, with `VALUES=values-dev.yaml ENVIRONMENT=dev REPOSITORY=… BRANCH=sandbox KUBECONFIG=/dev/null`.
  - Use a fake `kubectl` from `fakeBin` that appends every applied stdin's kinds to a file. `create namespace … -o yaml` prints a Namespace, and `wait` and `get application` succeed.
  - Assert that the first applied object of kind `Secret` named `in-cluster` comes before every `CustomResourceDefinition` and `ApplicationSet`.

- [ ] **Step 3: Run both tests and confirm they fail.**
  Run `cd test && go test ./offline -run 'TestArgoCDClusterSecret|TestArgoCDBootstrapAppliesSecretFirst'`. Expected: FAIL, since the script does not exist.

- [ ] **Step 4: Write `cluster-secret.sh` and update the module and units.**
  - `cluster-secret.sh` builds the YAML with `jq -n` piped to `yq -P`. The optional labels go through `sort -u`.
  - `bootstrap.sh` calls it and pipes the result to `apply` right after the namespace is created. It still calls the three `set-*.sh` scripts until Task 4.
  - `main.tf`:
    - adds `repository` and `branch`;
    - sends `repository` and `seed_repository` in the `data.external.values` query and drops the `forgejo_repository` `yamldecode`.
  - The units:
    - sandbox: `repository = jsondecode(file("${get_terragrunt_dir()}/../cluster/config.json")).seed_repository` and `branch = "sandbox"`;
    - testbed: the same, with `branch = "testbed"`;
    - production: `repository = "http://forgejo-http.forgejo:3000/ops/homelab"` and `branch = "master"`.

- [ ] **Step 5: Run the tests and confirm they pass.**
  Same command, plus `TestArgoCDChooseValues` and `just infra check`. Expected: PASS.

- [ ] **Step 6: Commit** with the message `feat(argocd): the bootstrap writes Argo CD's in-cluster cluster Secret first`.

### Task 3: readers of the generators accept the nested matrix

**Files:**
- Modify:
  - `cli/internal/guard/proof.go`
  - `scripts/env-values`
  - `scripts/check-env-config`
  - `test/e2e/testbed_test.go` (`testbedList`)
- Test: `cli/internal/guard/proof_test.go`, plus a new case for the nested shape

**Interfaces:**
- Consumes: nothing.
- Produces:
  - Each reader finds the git generators at any depth, under any number of `matrix` levels. Today's shape still works, so this task changes no behavior.
  - `scripts/env-values` resolves `{{ .metadata.annotations.backupS3Url }}` to `$BACKUP_S3_URL`, which is empty offline. Any other `.metadata.annotations.*` key fails with `"<key> is not an annotation env-values knows"`.

- [ ] **Step 1: Add the nested-shape test for proof.go.**
  - Add a case to the existing `GatedPaths` test file: a fixture commit whose `values-testbed.yaml` root generator is `matrix: {generators: [{clusters: {…}}, {matrix: {generators: [{git: {directories: […]}}, {git: {files: [config.json]}}]}}]}`.
  - Assert that `GatedPaths` returns the same paths as the flat fixture.

- [ ] **Step 2: Run it and confirm it fails.**
  Run `just cli test`. Expected: FAIL, with no directories found.

- [ ] **Step 3: Update the four readers.**
  - **`proof.go`:** make the generator type recursive (`Matrix.Generators []generator`) and walk it.
  - **`env-values`:** find the config path with `[.. | select(has("pathParamPrefix")) | .files[0].path][0]`.
  - **`check-env-config`:** apply the same recursive search to the git generator holding `pathParamPrefix: config` and the one holding `directories`.
  - **`testbedList`:** read `[.argocd-apps.applicationsets.root.generators | .. | select(has("directories")) | .directories[].path]`.

- [ ] **Step 4: Run the checks and confirm they pass.**
  Run `just cli test`, `scripts/check-env-config`, and `cd test && go vet ./e2e && go test ./offline`. Expected: all PASS, since the shape is unchanged.

- [ ] **Step 5: Commit** with the message `refactor: generator readers find git generators at any matrix depth`.

### Task 4: the ApplicationSets read the Secret; the rewrites go

**Files:**
- Modify:
  - `system/argocd/values.yaml`, `values-dev.yaml`, `values-testbed.yaml` and `values-seed.yaml`
  - `infra/_modules/argocd/bootstrap.sh`
  - `infra/testbed/release-paths.yaml` (keep `values-seed.yaml` listed: it still changes the first install)
  - `renovate.json5` (its comment on restated pins)
- Delete:
  - `infra/_modules/argocd/set-revision.sh`, `set-sandbox-apps.sh` and `set-backup-endpoint.sh`
  - `test/offline/argocd_revision_test.go`'s `TestArgoCDRevision`, `TestArgoCDBackupEndpoint` and `TestSetSandboxApps`, with their helpers
- Test: `test/offline/argocd_cluster_secret_test.go` (`TestArgoCDReadsClusterSecret`, `TestArgoCDOptionalApps`)

**Interfaces:**
- Consumes:
  - Task 2's Secret keys and the label `homelab/bootstrap`.
  - Task 3's readers.
- Produces: `scripts/render-chart system/argocd [dev|testbed|seed]` renders ApplicationSets whose homelab sources read the Secret.

- [ ] **Step 1: Write `TestArgoCDReadsClusterSecret`.**
  - For each env `""`, `dev`, `testbed` and `seed`, render `system/argocd`. For `root` and `apps`, assert:
    - `.spec.generators[0].matrix.generators[0].clusters.selector.matchLabels == {"homelab/bootstrap": "true"}`;
    - every map at any depth whose `repoURL` is not app-template's `https://bjw-s-labs.github.io/helm-charts` has `repoURL == "{{ .metadata.annotations.repository }}"`;
    - its `revision` / `targetRevision` is `"{{ .metadata.annotations.revision }}"`;
    - app-template's `targetRevision` stays a literal version;
    - the rendered output holds no `github.com/duongle-steven/homelab` and no `seed-repository.invalid`.
  - `global.backupS3Url` in root's first source is `"{{ .metadata.annotations.backupS3Url }}"`.
  - Every `.metadata.annotations.<key>` string in the render is in the set `cluster-secret.sh` writes (Review Focus 5).

- [ ] **Step 2: Write `TestArgoCDOptionalApps`.**
  - Render `dev`. The `apps` ApplicationSet has one generator for each of `paperless` and `opencloud`.
  - Each is a matrix whose `clusters.selector.matchLabels` is `{"homelab/bootstrap": "true", "homelab/app-<name>": "true"}`, crossed with git files `apps/<name>.yaml` and `config.json`.
  - The always-on generator lists neither.
  - `prod` has no `homelab/app-` selector.

- [ ] **Step 3: Run both tests and confirm they fail.**
  Run `cd test && go test ./offline -run 'TestArgoCDReadsClusterSecret|TestArgoCDOptionalApps'`. Expected: FAIL.

- [ ] **Step 4: Change the values files and the bootstrap.**
  - In each values file, the anchors `&repoURL` and `&revision` become the two annotation templates.
  - Each ApplicationSet's matrix nests inside `matrix: {generators: [{clusters: {selector: {matchLabels: {homelab/bootstrap: "true"}}}}, {matrix: <the existing matrix>}]}`.
  - `values.yaml` adds `global.backupS3Url` to `root`'s shared `valuesObject`.
  - **`values-dev.yaml`:**
    - Replace the optional-apps comment with the two labelled generators.
    - Restate `valuesObject` with `backupS3Url`.
    - Velero falls back to its `values-dev.yaml` endpoint when the URL is empty.
  - **`values-seed.yaml`:** drop `argocd-apps` entirely, keeping the metrics block.
  - **`bootstrap.sh`:** drop the three `set-*.sh` calls, the `sed` and the placeholder check, and with them `SEED_REPOSITORY` from `main.tf`'s environment.
  - Delete the three scripts and their tests.

- [ ] **Step 5: Run the checks and confirm they pass.**
  Run `scripts/ci-check all` in `direnv exec .`. Expected: rc 0. This covers `TestArgoCDServiceMonitors`, `TestArgoCDRootPreservesResources`, `check-env-config`, `release_paths_test` and `TestJustDevRevision`.

- [ ] **Step 6: Commit** with the message `feat(argocd): the ApplicationSets read the cluster Secret; the yq rewrites go`.

### Task 5: an e2e spec, the scopes and the docs

**Files:**
- Create: `test/e2e/argocd_cluster_secret_test.go`
- Modify:
  - `test/scopes.yaml`, so `infra/_modules/argocd/**` and `system/argocd/**` select the new spec's labels. Check the current group with `lab test scope --check`.
  - `test/offline/fixtures/e2e-selection.json`, only if `TestE2EKeepsSelection` asks for it.
  - `CLAUDE.md`:
    - the Sandbox section's `set-revision.sh` and `set-sandbox-apps.sh`;
    - roadmap 14f-2 and 14g marked `(done)`;
    - the bootstrap description.
  - `docs/how-to-guides/ci.md:94`.
  - `docs/installation/sandbox.md`, `docs/installation/testbed.md` and any page naming the scripts. Find them with `grep -rln "set-revision\|set-sandbox-apps\|set-backup-endpoint\|values-seed" docs CLAUDE.md`, excluding `docs/superpowers/`.

**Interfaces:**
- Consumes: Task 2's Secret and Task 4's templates.
- Produces: the spec `Describe("ArgoCDClusterSecret", Label("sandbox", "testbed", "argocd"), …)`.

- [ ] **Step 1: Write the spec.**
  - `It("carries what the ApplicationSets read")`: the Secret `argocd/in-cluster` has both labels, and the annotations `repository`, `revision` and `backupS3Url` all exist.
  - `It("is what every Application syncs")`: every Application in `argocd` whose source path is not app-template's has source `repoURL` equal to the annotation `repository` and `targetRevision` equal to `revision`, or to the commit `revision` names.
  - Give it a `SpecTimeout(5*time.Minute)`.
  - Use `kube.GetJSON`.

- [ ] **Step 2: Update the scopes and the docs.** Run `just cli check` and `cd test && go test ./offline -run TestE2E`. Expected: PASS.

- [ ] **Step 3: Commit** with the message `test: ArgoCDClusterSecret; docs follow the cluster Secret`.

### Task 6: prove it on the sandbox and the testbed

**Files:** none, unless a run finds a fault. A fix goes in with its own test first.

- [ ] **Step 1: Rebuild the sandbox from scratch.**
  Run `just dev-fresh` from `/home/nixos/homelab`, with the testbed stopped. Expected: the whole suite passes. Argo CD's first install now starts from the Secret.

- [ ] **Step 2: Add and drop optional apps (Review Focus 1).**
  - Run `just dev --apps paperless --tests 'argocd'`. Expected: Application `paperless` exists, and the Secret has `homelab/app-paperless`.
  - Run `just dev --tests 'argocd'`. Expected: the label is gone, and `kubectl -n argocd get application paperless` is NotFound.

- [ ] **Step 3: Pin a revision, then go back (Review Focus 2).**
  - Run `just dev --revision $(git rev-parse HEAD) --tests 'argocd'`. Expected: the `revision` annotation is the SHA.
  - Run a plain `just dev --tests 'argocd'`. Expected: `revision` is `sandbox`.

- [ ] **Step 4: Prove the testbed.**
  - Stop the sandbox (`k3d cluster stop homelab-dev`).
  - Run `just testbed`. Expected: the proof specs pass, and the mark `testbed` is written.
  - Run `just test e2e --env testbed --labels argocd`. Expected: `ArgoCDClusterSecret` passes. It is not labelled `proof`, so `just testbed` does not run it.
  - Then run `just testbed-stop`.

- [ ] **Step 5: Record the runs** for the PR description: commands, counts and durations.
