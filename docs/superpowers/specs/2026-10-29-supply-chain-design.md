# Step 15e: supply chain

Status: designed in chat 2026-10-29. Branch: `feat/supply-chain` from `dev`. Decisions marked *(user)* were made in
chat. The rest are *(Claude's call)*.

## Goal

What runs in the cluster is known and watched:

- an image with a fixable critical CVE, or a secret baked into an image, raises an alert;
- our own image (lab's) is signed by the workflow that built it, with its SBOM attached, and Kyverno checks that
  signature;
- the GitHub Actions workflows that build and sign it are checked for known weaknesses on every pull request.

## What exists *(measured 2026-10-29)*

- 15a's `images` check scans only the images new in a pull request's renders. Nothing watches the images already
  running when a CVE is published later.
- lab's image is built by `.github/workflows/lab-image.yaml` from `master` and pushed to
  `ghcr.io/duongle-steven/homelab-lab:<sha>`. `platform/secrets-rotation/values.yaml` pins its digest by hand.
  It is neither signed nor shipped with an SBOM.
- The blog's image (`registry.<domain>/blog:latest`) has no pipeline yet.
- Kyverno 1.19 runs in Audit; Enforce is 15b-2.
- zizmor 1.31.1 (offline) finds 14 issues in `.github/`: 10 suppressed, 4 low, none medium or high.

## Scope *(user)*

- **Trivy Operator scans only what nothing else covers.**
  - It scans image vulnerabilities and exposed secrets in images.
  - Config audit is off: Kyverno covers it (15b).
  - RBAC assessment, infra assessment and cluster compliance are off: kube-bench and kubescape cover them (15d).
- **The blog's signature waits for its pipeline** (Woodpecker, org `ci`, a later step). That pipeline signs with a
  key in OpenBao. This step only records it in CLAUDE.md.

## Design *(Claude's calls)*

### 1. Trivy Operator (`system/trivy-operator`)

**Version and setup**
- The `trivy-operator` chart at its latest release, from Aqua's repository.
- One namespace `trivy-system`, inside the mesh. Its scan Jobs are short pods; they get the mesh's defaults.
- `ClientServer` mode with the built-in Trivy server: the vulnerability DB is downloaded once and kept on the server.

**Scanners**
- `vulnerabilityScannerEnabled` and `exposedSecretScannerEnabled` are on.
- `configAuditScannerEnabled`, `rbacAssessmentScannerEnabled`, `infraAssessmentScannerEnabled`,
  `clusterComplianceEnabled` and `sbomGenerationEnabled` are off.
  - Cluster SBOM reports would grow etcd, one CRD object per image. Our own image's SBOM is in step 2.

**Severity** *(user: unfixed findings are reported too, since they cannot be patched and stay exploitable)*
- Every finding is reported, fixed or not (`ignoreUnfixed: false`), at `HIGH,CRITICAL`.
- The "Security" dashboard shows them all.
- An unfixed CVE cannot be fixed by a bump. It is handled by one of these:
  - what already limits exploitation: default-deny egress, mTLS and deny-all, Pod Security, and 15g later;
  - replacing the app or turning off the vulnerable feature;
  - an acceptance with a reason and an expiry.

**Accepted findings**
- Accepted findings go in the operator's ignore policy in `values.yaml`. Each one carries a reason and an expiry,
  like 15a's `.trivyignore.yaml`.
  - An expired acceptance no longer applies, so the alert fires again.
- The baseline is measured in the sandbox and reported in chat before it is accepted:
  - a fixable finding is fixed by bumping the image;
  - an unfixed CRITICAL is accepted with its reason, or its app is replaced.

**Metrics and alerts**
- Prometheus scrapes the operator through a ServiceMonitor.
- Three `PrometheusRule` alerts, all `warning` and `for: 1h`, labelled by namespace, workload and image:
  - `ImageCriticalVulnerability` fires when a running image has a CRITICAL finding with a fix. The fix is a bump.
  - `ImageCriticalVulnerabilityUnfixed` fires when a running image has a CRITICAL finding with no fix and no
    acceptance. The fix is a decision: accept it with a reason and an expiry, or replace the app.
  - `ImageExposedSecret` fires when a secret is found in a running image.
- HIGH findings alert on nothing and show only on the dashboard: base images carry many, and alerting on them would
  bury the alerts that matter.
- Both reach ntfy and Telegram like every alert.

**Sandbox: 12GB, already tight once Kiali and Kyverno joined**
- One scan Job at a time (`scanJobsConcurrentLimit: 1`) with memory limits.
- Images are pulled through the sandbox's pull-through caches (Trivy's registry mirrors), not from the internet again.
- If the suite's memory still does not fit, the sandbox's scans are scheduled outside the suite. That decision is
  made in the plan's sandbox task, ledgered.

**Network**
- Egress 443 for `trivy-system` (the DB and registries) in `system/egress/values.yaml`.
- An `allow` AuthorizationPolicy for its namespace.
- `homelab/backup: none` on any claim.
- Pod Security restricted, or a PolicyException with its reason.
- An entry in `test/scopes.yaml`.

### 2. lab's image: signature and SBOM

**`lab-image.yaml` after the push**
- Generate an SPDX SBOM with Syft, pinned.
- Sign the image by digest with cosign, keyless: GitHub's OIDC token, Sigstore's Fulcio and Rekor.
- Attest the SBOM to the image (`cosign attest --type spdxjson`).
- Permissions gain `id-token: write`. No key is kept, so `cli/internal/rotate/catalog.yaml` gets no new secret.
- Actions are pinned by digest, as today.

**Kyverno policy, in `system/kyverno`, production only**
- An `ImageValidatingPolicy` matching `ghcr.io/duongle-steven/homelab-lab*`.
- It requires:
  - a keyless signature from issuer `https://token.actions.githubusercontent.com`;
  - subject `https://github.com/duongle-steven/homelab/.github/workflows/lab-image.yaml@refs/heads/master`;
  - the SPDX attestation from the same identity.
- In Audit, like every policy until 15b-2.
- Kyverno gets egress 443 to ghcr.io and Sigstore's TUF and Rekor.

**Sandbox**
- The sandbox runs lab's image built from the checkout, which is unsigned, so the production policy is not rendered
  there (`values-dev.yaml`).
- `TestSupplyChain` proves Kyverno's verification path instead:
  - The test signs a small image in zot with a key pair it generates.
  - It creates its own Audit `ImageValidatingPolicy` for that image's repository, with that public key.
  - It runs a pod from the signed image, then one from an unsigned image in the same repository. Kyverno reads zot
    with the test's pull credentials (the policy's `credentials`).
  - It expects a pass report for the first and a fail report for the second.
- The production policy is checked offline: it renders, with the identity fields above (`TestLabImagePolicy`).
  `kyverno apply` cannot check it offline, since verification fetches the image.
- Its real verification against ghcr is listed under "Before the first production deploy". It needs the first
  signed release from `master`.

### 3. zizmor in the basic checks

- zizmor comes from the dev shell: `unstable.zizmor` 1.31.1, the latest.
- A new check `workflows` in `scripts/ci-check`, and a job in `checks.yaml`'s matrix that feeds the required `checks`.
  - It runs `zizmor --offline .github/`.
  - Any finding fails it. The 4 low findings today are fixed, or ignored in `.github/zizmor.yml` with a reason.
- Online audits (they call GitHub's API) are not run: a token in the basic checks is not worth what they find here.

## Tests

- **`TestTrivyOperatorRender`** (offline) checks:
  - only the two scanners are on;
  - `ignoreUnfixed` is false;
  - every ignore entry has a reason and an unexpired expiry;
  - the alerts' expressions use metrics the operator exports.
- **`TestTrivyOperator`** (sandbox):
  - the operator is running;
  - a `VulnerabilityReport` appears for a known workload;
  - an image with a known fixable CRITICAL CVE, run in a test namespace, raises `ImageCriticalVulnerability`'s
    expression in Prometheus;
  - after the suite, no other workload has an unaccepted CRITICAL finding, fixed or not.
- **`TestSupplyChain`** (sandbox): the signed and unsigned pods, as in section 2.
- **`TestLabImagePolicy`** (offline): the production policy with the identity above; the dev render has no policy.
- **The `workflows` check:** a fixture workflow with a known finding (`pull_request_target` checking out the head)
  fails, and today's `.github/` passes.

## Out of scope

- The blog's pipeline and signature (later, with its repository).
- Signing or verifying third-party images.
- Enforce (15b-2).
- Trivy's config, RBAC and compliance scans (15b and 15d).
