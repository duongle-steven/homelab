# Step 15a: detection in the basic checks

Status: designed in chat 2026-10-27. Branch: `feat/detection` from `dev`. Decisions marked *(user)* were made in chat.
The rest are *(Claude's call)*.

## Goal

A pull request that adds a secret, an image with a fixable critical CVE, or a risky OpenTofu or Dockerfile setting
fails its basic checks, before anything reaches `dev`.

## Scope against step 15b *(user)*

Kubernetes manifests (privileged, root, securityContext, RBAC) are 15b's: Kyverno's policies, also run in CI with the
`kyverno` CLI on the renders. One rule set and one place for exceptions (`PolicyException`). So 15a does not run Trivy's
Kubernetes checks. A spike counted 224 HIGH/CRITICAL findings on today's renders; that baseline is 15b's work.

15a covers what Kyverno cannot see: secrets in the diff, image CVEs, and OpenTofu and Dockerfile config.

## What exists *(measured 2026-10-27)*

- GitHub secret scanning and push protection are already on: the repository is public. Nothing to change in GitHub's
  settings.
- gitleaks finds nothing in today's tree. Its 10 findings in the history are upstream's (2021–2022 values of khuedoan's
  homelab, variable references, test fixtures). Only the pull request's commits are scanned, so they do not matter.
- Trivy finds nothing HIGH/CRITICAL in `infra/`. Four Dockerfiles run as root:
  - `cli/Dockerfile`: a false positive. distroless `nonroot` is already non-root, but has no `USER` line;
  - the root `Dockerfile` (upstream's docs image);
  - `metal/`'s dnsmasq and http images for PXE, where dnsmasq needs root.

## Design *(Claude's calls)*

1. **Tools.** Both come from the dev shell:
   - gitleaks 8.30.1 from nixos-26.05, the latest;
   - Trivy from `unstable`: 0.76.0. 0.76.1 is two days old and not packaged yet; note it next to the package.
2. **Three new checks in `scripts/ci-check`**, each its own job in `.github/workflows/checks.yaml`'s matrix. They feed
   the required `checks`.
   - **`leaks`**: `gitleaks git` over the commits `$BASE..HEAD`, with the default rules. Any finding fails.
     `.gitleaks.toml` extends the defaults; an allowlist entry needs a reason, and none is expected now. Output is
     redacted.
   - **`images`**:
     - Render each chart and app file the change touches, at the base and at HEAD (`lab test charts` lists them).
     - Collect the images (every `image:` in the renders) and scan only those new at HEAD with `trivy image
       --severity CRITICAL --ignore-unfixed`. Any finding fails.
     - Images pinned by digest are scanned by digest.
     - `.trivyignore.yaml` lists accepted CVEs: each with the image, the reason and an expiry date at most 90 days
       away, so it is looked at again.
   - **`iac`**: `trivy config --severity HIGH,CRITICAL` over `infra/` (OpenTofu), `cli/`, the root `Dockerfile` and
     `metal/`. These are small, so all of them are scanned every time, not only what changed.
     - `cli/Dockerfile` gains `USER 65532:65532` (distroless' nonroot), which clears its finding.
     - The other three go in `.trivyignore.yaml` with their reasons: dnsmasq binds port 67; upstream's docs image is
       not deployed by this repository. The root `Dockerfile`'s unpinned images go in deferred minors.
3. **Trivy's database in CI.** Cached with `actions/cache`, keyed by day. Trivy reads it offline after the first
   download (`--skip-db-update` when the cache hit), so 11 parallel jobs do not each pull it, and GHCR's rate limit is
   not hit. Locally, Trivy's cache is in `~/.cache/trivy`.
4. **Local runs.** `scripts/ci-check all` runs the three like the rest. `images` pulls only the new images' layers,
   usually none or a few.
5. **Tests.** Offline tests in `test/` (`TestCheckLeaks`, `TestCheckImages`, `TestCheckIaC`) run the checks against
   fixture repositories or files. Each one shows:
   - a planted fake secret fails `leaks`, and a clean diff passes;
   - an image list diff picks only the new images, and a fake trivy that reports a finding fails `images`;
   - a Dockerfile without `USER` fails `iac`, and the ignore file is honoured.

   They are added to `ci-check`'s `offline_tests`. The `checks` workflow proves the jobs on the pull request itself.
6. **Docs.** `docs/how-to-guides/ci.md` gains what each check does, how to handle a finding (rotate a leaked secret:
   `lab secrets rotate`; bump an image; add a reasoned, expiring ignore), and the ruleset note that `checks` covers the
   new jobs.

## Also recorded *(user)*

CLAUDE.md, "Before the first production deploy": a scan found unencrypted production state under
`.state/production/cloudflare/` (`terraform.tfstate` and a `.pre-r2` copy, git-ignored, holding Cloudflare tokens).
Check that the state on R2 is complete, delete the local copies, and rotate the Cloudflare tokens they hold before the
first deploy.

## Not in this step

- Kubernetes manifest policy (15b).
- The Trivy Operator, SBOMs and signatures (15e).
- Rewriting history (the upstream findings are not this repository's secrets).
