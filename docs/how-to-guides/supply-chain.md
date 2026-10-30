# Supply chain

What runs in the cluster is watched for known vulnerabilities and baked-in secrets, our own image is signed where it is
built, and the workflows that build it are checked on every pull request (step 15e,
`docs/superpowers/specs/2026-10-29-supply-chain-design.md`).

| What | Where | Checks |
|---|---|---|
| Images already running | the Trivy Operator (`system/trivy-operator`) | CVEs, fixed or not, at HIGH and CRITICAL; secrets inside images |
| Images new in a pull request | the basic check `images` ([CI](ci.md)) | CRITICAL CVEs with a fix |
| lab's image | `.github/workflows/lab-image.yaml`, Kyverno's `verify-lab-image` | a keyless signature and an SPDX SBOM from that workflow on `master` |
| GitHub Actions workflows | the basic check `workflows` (zizmor, offline) | template injection, dangerous triggers, unpinned actions, credentials left in the checkout |

Manifests are Kyverno's ([Policy](policy.md)) and the cluster's configuration is kube-bench's and kubescape's (step
15d): the operator's config audit, RBAC, infra and compliance scanners are off.

## The Trivy Operator

The operator scans each image a workload runs, once, and again when the image changes or its report is a day old. One
Trivy server in namespace `trivy-operator` keeps the vulnerability database (about 80 MB, from
`ghcr.io/aquasecurity/trivy-db`, on an emptyDir: downloaded again when it restarts); the scan Jobs ask it. The scans
pull the images from their registries (egress 443). In the sandbox one Job runs at a time, with memory limits
(`values-dev.yaml`). Measured there: a scan Job peaks under 80Mi (the server does the work), the server at about
100Mi, the operator at about 160Mi.

```sh
kubectl get vulnerabilityreports -A                 # one per container image, with its counts
kubectl get vulnerabilityreports -n <ns> <name> -o yaml
kubectl get exposedsecretreports -A
```

Grafana's dashboard **Security** lists every finding, HIGH included.

### Alerts

They reach ntfy and Telegram like every alert ([Alerts](alerts.md)), after an hour:

| Alert | Fires when | Do |
|---|---|---|
| `ImageCriticalVulnerability` | a running image has a CRITICAL CVE with a fixed release | bump the image |
| `ImageCriticalVulnerabilityUnfixed` | a running image has a CRITICAL CVE with no fix | decide: accept it (below) or replace the app |
| `ImageExposedSecret` | a secret was found inside a running image | `kubectl get exposedsecretreports -n <ns>`; rotate it if it is ours |
| `ImageScanMissing` | a running ReplicaSet, StatefulSet or DaemonSet has had no report for 6 hours: its scans fail (out of memory, timed out, an image it cannot pull), so it would show no finding | read the operator's log for the workload (`kubectl logs -n trivy-operator deploy/trivy-operator`); a log that never names it means the operator dropped it at start (seen in the sandbox): `kubectl annotate -n <ns> <kind> <name> homelab/trivy-rescan=$(date +%s) --overwrite` queues its scan |

HIGH findings alert on nothing: base images carry many, and they would bury these.
A rollout leaves the old ReplicaSet at 0 replicas, with its report, as the Deployment's history: the alerts and the
dashboard leave those out.

### Accepting a finding

When no image fixes a CVE yet (or the fix is not reachable here), accept it in `system/trivy-operator/values.yaml`
under `trivy.ignoreFile.vulnerabilities`, and the same entry in `.trivyignore.yaml` (read by the basic checks):

```yaml
        - id: CVE-2026-12345
          statement: >-
            <package and version> in <image and tag> (the latest): why it cannot be exploited here, or what limits
            it (default-deny egress, mTLS, Pod Security, who can reach the app). Bump when <image> is rebuilt
          expired_at: "2026-12-31T00:00:00Z"
```

- `expired_at` at most 90 days away. In `values.yaml` write it in RFC 3339 and quoted: the chart writes the file with
  `toYaml`, which turns a bare date into a string, and Trivy reads a string only in RFC 3339 (any other form stops
  every scan Job). `.trivyignore.yaml` keeps the bare date.
- Trivy ignores by id: one entry hides that CVE in every image. Name every image it covers in the statement.
- The operator reads Trivy's settings only at start: `TestTrivyOperatorRender` prints the new value of the pod
  annotation `homelab/trivy-config` (in `values.yaml` and `values-dev.yaml`), which restarts it when Argo CD syncs.
- An expired entry no longer applies: the finding is reported and alerts again.
- The operator applies the ignore file only to new scans, and rescans a report once it is a day old
  (`OPERATOR_SCANNER_REPORT_TTL`): to clear an alert now, delete the workload's reports and it is scanned again
  (`kubectl delete vulnerabilityreports -n <ns> -l trivy-operator.resource.name=<workload>`).

`TestTrivyOperator` fails while any running image in the sandbox has a CRITICAL finding that is neither fixed nor
accepted.

## lab's image: signature and SBOM

`.github/workflows/lab-image.yaml` builds lab's image on `master`, generates its SBOM with Syft (SPDX), signs the image
by digest with cosign keyless (GitHub's OIDC token, Sigstore's Fulcio and Rekor) and attests the SBOM. No key is kept.

```sh
cosign verify ghcr.io/duongle-steven/homelab-lab@sha256:<digest> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity https://github.com/duongle-steven/homelab/.github/workflows/lab-image.yaml@refs/heads/master
cosign verify-attestation --type spdxjson <same arguments>
```

Kyverno's `ImageValidatingPolicy` `verify-lab-image` (`system/kyverno/templates/lab-image-policy.yaml`, production
only: the sandbox runs lab's image built from the checkout, unsigned) checks both for every pod running
`ghcr.io/duongle-steven/homelab-lab`. It audits, like every policy until step 15b-2:

```sh
kubectl get policyreports -n secrets-rotation -o yaml | grep -B2 -A6 verify-lab-image
```

In the sandbox, `TestSupplyChain` proves the same path with zot: it signs a test image with a key it generates, runs a
signed and an unsigned pod under an Audit policy with that key, and expects the unsigned pod's `fail` (Kyverno keeps
no `pass` results, `--allowedResults`). Kyverno's admission and reports controllers trust the sandbox CA for that
(`system/kyverno/values-dev.yaml`).

The blog's image will be signed by its own pipeline (Woodpecker, a key in OpenBao) when its repository comes.

## Workflows

`scripts/ci-check workflows` runs `zizmor --offline .github/`; any finding fails it. Fix it, or ignore it in
`.github/zizmor.yml` with the reason ([CI](ci.md)).
