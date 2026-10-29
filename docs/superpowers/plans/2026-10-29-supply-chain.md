# Supply Chain Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Running images are watched for CVEs and baked-in secrets, lab's image is signed with its SBOM and checked by
Kyverno, and the workflows are checked by zizmor.

**Architecture:**
- A new component `system/trivy-operator` holds the operator, its accepted findings and three Prometheus alerts.
- `lab-image.yaml` signs keyless with cosign and attests a Syft SBOM.
- `system/kyverno` gains a production-only `ImageValidatingPolicy`.
- A new basic check `workflows` runs zizmor offline.

**Tech Stack:**
- trivy-operator chart 0.36.x (the latest at the time of the work);
- cosign 3.1.x and Syft 1.55.x in GitHub Actions, through their pinned actions;
- zizmor 1.31.1 from `unstable`;
- Kyverno 1.19 `policies.kyverno.io/v1` `ImageValidatingPolicy`;
- Go tests in `test/` (terratest).

**Spec:** `docs/superpowers/specs/2026-10-29-supply-chain-design.md`

## Global Constraints

- Latest stable, pinned exactly. Actions are pinned by commit digest with the version in a comment, as in `checks.yaml`.
- Trivy Operator scans only `vulnerability` and `exposedSecret`. Config audit, RBAC, infra assessment, compliance and
  SBOM generation are off.
- `ignoreUnfixed: false`, severities `HIGH,CRITICAL`.
- Three alerts, `warning` and `for: 1h`: `ImageCriticalVulnerability`, `ImageCriticalVulnerabilityUnfixed`,
  `ImageExposedSecret`. HIGH findings alert on nothing.
- An accepted finding has a reason and an expiry. An expired one no longer applies.
- `scanJobsConcurrentLimit: 1` and memory limits in the sandbox.
- lab's signature: issuer `https://token.actions.githubusercontent.com`, subject
  `https://github.com/duongle-steven/homelab/.github/workflows/lab-image.yaml@refs/heads/master`. Keyless: no new
  secret in `cli/internal/rotate/catalog.yaml`.
- The lab policy is rendered in production only, in Audit.
- Every new namespace gets:
  - an `allow` AuthorizationPolicy;
  - an egress entry if it calls out;
  - Pod Security restricted or a `PolicyException` with `homelab/reason`;
  - a `test/scopes.yaml` entry;
  - `homelab/backup` on any claim.
- No token in arguments or logs. The test's cosign key password is passed through the environment, never as a flag
  value.
- `scripts/ci-check all` passes before each commit. `make dev` runs only from the main checkout.

## Review Focus

- **An acceptance whose expiry has passed must stop silencing its CVE.**
  - Trivy's `.trivyignore` `exp:` handles this; `TestTrivyOperatorRender` asserts every entry has a future `exp:`.
  - The render test also asserts the file is the one the operator mounts (`trivy.ignoreFile`).
- **Two workloads sharing one image must not double-alert or hide each other.**
  - The alerts group by `namespace` and `resource_name` from the report's metric labels.
  - `TestTrivyOperatorRender` asserts the `by` clause.
- **A scan Job that is OOM-killed in the sandbox leaves no report and no alert, silently.**
  - `TestTrivyOperator` asserts a report exists for a known workload.
  - Task 4 measures the scan Jobs' peak memory.
- **The lab policy's subject must pin `refs/heads/master`, not a regex accepting any branch.**
  - A branch's run would otherwise pass.
  - `TestLabImagePolicy` asserts the exact string.
- **A zizmor finding that is ignored without a reason.**
  - The `workflows` check's test asserts every `.github/zizmor.yml` ignore carries a comment.

---

### Task 1: zizmor as the basic check `workflows`

**Files:**
- Modify: `flake.nix`, which adds `unstable.zizmor  # scripts/check-workflows`.
- Create: `scripts/check-workflows`, `.github/zizmor.yml` (only if a finding is ignored), `test/fixtures/workflows/bad/.github/workflows/bad.yaml`.
- Modify: `scripts/ci-check` (`checks` list and its case), `.github/workflows/checks.yaml` (the matrix gains
  `workflows`), `docs/how-to-guides/ci.md`, `.github/workflows/sandbox*.yaml` (the 4 low findings).
- Test: `test/workflows_check_test.go`.

**Interfaces:**
- Produces: `scripts/check-workflows [dir]`. It defaults to the repository root and exits 1 on any finding.

- [ ] **Step 1: Write the failing test.**
  - `TestCheckWorkflows`:
    - `runCheck(t, "check-workflows", "..")` succeeds;
    - `runCheck(t, "check-workflows", "..", "WORKFLOWS_DIR=fixtures/workflows/bad")` fails, and its output contains
      `dangerous-triggers`.
  - The fixture is a `pull_request_target` workflow that checks out `github.event.pull_request.head.sha`.
  - `TestZizmorIgnoresHaveReasons`: every `ignore:` entry in `.github/zizmor.yml`, if the file exists, has a `#`
    comment on its line.
- [ ] **Step 2: Run `go test ./test -run 'CheckWorkflows|ZizmorIgnores'`.**
  - Expected: FAIL, because `check-workflows` is not found.
- [ ] **Step 3: Implement.**
  - `scripts/check-workflows` runs `zizmor --offline --no-progress "${WORKFLOWS_DIR:-.}/.github"`, with `--config` when
    `.github/zizmor.yml` exists.
  - Fix the 4 low findings: use `self-repository` syntax wherever zizmor's auto-fix applies cleanly. Otherwise ignore
    them with a reason.
  - Add `workflows` to `ci-check` and to the matrix.
- [ ] **Step 4: Run it.**
  - Run the same `go test`, then `direnv exec . scripts/ci-check workflows`.
  - Expected: PASS, and `No findings`.
- [ ] **Step 5: Commit** `feat(ci): zizmor checks the workflows (basic check workflows)`.

### Task 2: lab's image signed, with its SBOM, and Kyverno's policy

**Files:**
- Modify: `.github/workflows/lab-image.yaml`.
  - Permissions gain `id-token: write`.
  - After `build-push-action` (with `id: build`):
    - `sigstore/cosign-installer`;
    - `cosign sign --yes ghcr.io/…@${{ steps.build.outputs.digest }}`;
    - `anchore/sbom-action` with `format: spdx-json` and `upload-artifact: false` on the digest;
    - `cosign attest --yes --type spdxjson --predicate sbom.spdx.json` on the digest.
  - The digest reaches `run:` through `env:`, not inline `${{ }}` (zizmor's `template-injection`).
- Create: `system/kyverno/templates/lab-image-policy.yaml`, rendered when `.Values.labImagePolicy.enabled`.
- Modify: `system/kyverno/values.yaml` (`labImagePolicy: {enabled: true, image: ghcr.io/duongle-steven/homelab-lab*}`),
  `system/kyverno/values-dev.yaml` (`enabled: false`, with the reason: the sandbox's lab image is built from the
  checkout, unsigned), `system/egress/values.yaml` (`kyverno: [{port: 443}]  # ghcr.io and Sigstore (TUF, Rekor):
  lab's signature (production)`).
- Test: `test/supply_chain_test.go`.

**Interfaces:**
- Produces: the policy `verify-lab-image`, a cluster-scoped `ImageValidatingPolicy` with
  `validationActions: [Audit]`.

- [ ] **Step 1: Write the failing test.** `TestLabImagePolicy` renders `system/kyverno` (prod) and asserts:
  - exactly one `ImageValidatingPolicy` named `verify-lab-image`;
  - `matchImageReferences` is `ghcr.io/duongle-steven/homelab-lab*`;
  - a keyless attestor with issuer and subject exactly as the Global Constraints say;
  - an attestation for `https://spdx.dev/Document`;
  - Audit.
  - The dev render has no `ImageValidatingPolicy`.
- [ ] **Step 2: Run `go test ./test -run LabImagePolicy`.**
  - Expected: FAIL, because no policy is rendered.
- [ ] **Step 3: Implement the template, the values and the workflow steps above.**
  - Check the field names against Kyverno 1.19's `ImageValidatingPolicy` CRD in `system/kyverno/charts`.
- [ ] **Step 4: Run it.**
  - Run `go test ./test -run LabImagePolicy`, then `scripts/ci-check policy render workflows`.
  - Expected: PASS. zizmor reports no finding for `lab-image.yaml`.
- [ ] **Step 5: Commit** `feat(supply-chain): lab's image signed keyless with its SBOM attested; Kyverno checks it in production`.

### Task 3: `system/trivy-operator`

**Files:**
- Create: `system/trivy-operator/`, with:
  - `Chart.yaml` (the `trivy-operator` dependency from `https://aquasecurity.github.io/helm-charts/`, latest);
  - `values.yaml`, `values-dev.yaml`;
  - `files/trivyignore` (header comment: one CVE per line, `exp:YYYY-MM-DD`, and a `#` reason line above each);
  - `templates/authorization-policy.yaml`, `templates/prometheus-rules.yaml`.
  - `values.yaml` sets the scanners per the Global Constraints, `ignoreUnfixed: false`, `severity: HIGH,CRITICAL`,
    `operator.metricsVulnIdEnabled: true`, `exposedSecretsMetrics`, the ServiceMonitor, and
    `trivy.ignoreFile` from `files/trivyignore` (wrapper template or `--set-file`, as the chart allows).
- Modify:
  - `system/argocd/values.yaml` and `values-dev.yaml`: `system/trivy-operator` in the `root` generators;
  - `system/egress/values.yaml`: `trivy-system: [{port: 443}]  # the vulnerability DB and registries`;
  - `test/scopes.yaml`: `system/trivy-operator/** → TrivyOperator`;
  - `system/monitoring-system/files/security-dashboard.json`: a row "Images" with findings by severity, fixed or not;
  - `scripts/check-backup-groups`, if the chart renders a claim.
- Test: `test/trivy_operator_test.go`.

**Interfaces:**
- Produces:
  - namespace `trivy-system`;
  - alerts named as the Global Constraints say, labelled `source=trivy`;
  - `renderTrivyOperator(t, env, expr string) string` for Task 4.

- [ ] **Step 1: Write the failing test.** `TestTrivyOperatorRender` for both envs asserts:
  - only `vulnerability` and `exposedSecret` are enabled;
  - `ignoreUnfixed` is false;
  - every non-comment line of the rendered ignore file has `exp:` after today's date and a `#` reason line above it;
  - three `PrometheusRule` alerts with those names, all `for: 1h`, `severity: warning`, aggregating `by` `namespace`
    and `resource_name`;
  - the fixed and unfixed alerts differ only in `fixed_version!=""` versus `fixed_version=""`;
  - the dev render has `scanJobsConcurrentLimit: 1` and memory limits on scan Jobs and the server.
- [ ] **Step 2: Run `go test ./test -run TrivyOperatorRender`.**
  - Expected: FAIL, because there is no chart.
- [ ] **Step 3: Implement the chart.**
  - Confirm the metric names from the chart's documented metrics: `trivy_vulnerability_id` with
    `vuln_severity` and `fixed_version`, and the exposed-secret metric.
  - Write the alerts on those metrics.
  - Run `scripts/ci-check policy mesh env-config render` and fix what it reports (securityContext or a
    `PolicyException`).
- [ ] **Step 4: Run it.**
  - Run `go test ./test -run TrivyOperatorRender`, then `scripts/ci-check all`.
  - Expected: PASS.
- [ ] **Step 5: Commit** `feat(supply-chain): the Trivy Operator watches running images: CVEs (fixed or not) and secrets, three alerts`.

### Task 4: sandbox proof, baseline and docs

**Files:**
- Modify: `test/trivy_operator_test.go` (`TestTrivyOperator`), `test/supply_chain_test.go` (`TestSupplyChain`),
  `system/trivy-operator/files/trivyignore` (the accepted baseline).
- Create: `docs/how-to-guides/supply-chain.md`.
- Modify: `mkdocs.yml` nav, and `CLAUDE.md`:
  - roadmap 15e done;
  - the blog's signature waits for its pipeline, with a key in OpenBao;
  - "Before the first production deploy": lab's signature is verified only after the first signed release, and the
    operator's scans on three nodes are unmeasured.

- [ ] **Step 1: Write the sandbox tests.**
  - `TestTrivyOperator`:
    - the operator Deployment is available;
    - a `VulnerabilityReport` for `excalidraw`'s workload appears within 30 minutes;
    - a pod of an old image with a known fixable CRITICAL CVE, run in namespace `trivy-test-<unix>`, gets a report.
      Use `docker.io/library/python:3.9.0-slim` or whatever image the cache already has with one; check with
      `trivy image` first.
    - `prometheusQuery` of `ImageCriticalVulnerability`'s expression returns that namespace within 20 minutes;
    - the namespace is deleted at the end;
    - after the scans, the unfixed and fixed CRITICAL expressions, outside the test namespace, return nothing.
  - `TestSupplyChain`:
    - push `busybox:1.37.0` to zot as `ci/supply-chain-test:signed` and `:unsigned`, using the credentials from the
      `ci` account's Secret, on stdin;
    - `cosign generate-key-pair`, with `COSIGN_PASSWORD` set in the environment, in `t.TempDir()`;
    - sign `:signed`;
    - create an Audit `ImageValidatingPolicy` with that public key and zot's pull credentials;
    - run both pods;
    - expect a `PolicyReport` result `pass` for the signed pod and `fail` for the unsigned one;
    - clean up.
- [ ] **Step 2: Commit, then run `make dev`.**
  - Expected: the new tests FAIL only on the baseline: running images with unaccepted CRITICAL findings.
  - Record the scan Jobs' peak memory and whether the suite fits. If not, rule on moving the scans out of the suite,
    and ledger it.
- [ ] **Step 3: Report the baseline in chat.**
  - List the CRITICAL findings, with and without a fix, per image.
  - Bump the images that have a fixed release.
  - Propose a reason and an expiry for each unfixed one.
  - This is the one stop the spec asks for. Wait for the user's answer.
- [ ] **Step 4: Write the acceptances the user agreed to.**
  - Write the docs page: what scans, what alerts, how to accept a finding, how lab's signature is checked.
  - Write the CLAUDE.md lines.
  - Run `scripts/ci-check all`, commit, then `make dev`.
  - Expected: every test PASSES.
- [ ] **Step 5: Commit** `feat(supply-chain): the sandbox's baseline accepted, the guide and the roadmap`.
