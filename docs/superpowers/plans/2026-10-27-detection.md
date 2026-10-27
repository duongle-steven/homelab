# Detection (Step 15a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Three new basic checks fail a pull request: `leaks` (gitleaks on its commits), `images` (Trivy on its new
images) and `iac` (Trivy on OpenTofu and Dockerfiles).

**Architecture:** Three `scripts/check-*` scripts, called by `scripts/ci-check` and run as matrix jobs in `checks.yaml`.
Offline tests in `test/` run each script against fixtures, with a fake `trivy` where a scan would need the network.

**Tech Stack:** POSIX sh, gitleaks, Trivy, yq, GitHub Actions, Go tests.

**Spec:** `docs/superpowers/specs/2026-10-27-detection-design.md`

## Global Constraints

- gitleaks comes from nixos-26.05 (8.30.1). Trivy comes from `unstable` (0.76.0), with a comment that 0.76.1 is not
  packaged yet.
- Only what the change brings is scanned: `$BASE..HEAD` for `leaks`, and the images new at HEAD for `images`. `iac` scans
  its small set in full.
- `images` fails on CRITICAL with a fix. `iac` fails on HIGH and CRITICAL.
- Every entry in `.trivyignore.yaml` has a reason (`statement`). CVE entries also have an `expired_at` at most 90 days
  away.
- No secret value in any output: gitleaks runs with `--redact`.
- `scripts/lint` passes: shellcheck and actionlint.

## Review Focus

1. **`BASE` is forty zeros** (a new branch's first push) or missing: `leaks` must still scan something sensible (HEAD's
   own commit), not everything and not nothing. Test: `TestCheckLeaks` with `BASE=0000…`.
2. **An image pinned by digest, or with a registry port** (`registry.x:5000/a@sha256:…`) is parsed whole. Test:
   `TestCheckImages` fixture.
3. **A render that fails** in `images` must fail the check, never pass as "no new image". Test: `TestCheckImages` with
   a broken chart.
4. **An expired `.trivyignore.yaml` entry** stops ignoring. That is Trivy's behaviour; `TestCheckIaC` uses a fake that
   checks the flag is passed.

---

### Task 1: `leaks`

**Files:**
- Create: `scripts/check-leaks`, `.gitleaks.toml`
- Modify: `flake.nix` (gitleaks), `scripts/ci-check` (`leaks`), `.github/workflows/checks.yaml` (matrix)
- Test: `test/detection_test.go` (`TestCheckLeaks`)

- [ ] **Step 1: Write the failing test.** `TestCheckLeaks`. In a temp git repository, commit a clean file, then a file
  holding a fake AWS key that gitleaks detects (built at runtime so this repository holds no key-shaped literal).
  - `BASE` = the clean commit: the check fails, and the output holds no key characters (it is redacted).
  - `BASE` = HEAD: it passes.
  - `BASE` = forty zeros: it scans HEAD's commit only.
- [ ] **Step 2: Run it.** It fails: no script.
- [ ] **Step 3: Implement.** `scripts/check-leaks`: `gitleaks git --redact --no-banner --log-opts "$range"`. The range is
  `$BASE..HEAD`, or `HEAD^..HEAD` for zeros or an empty `BASE`. Add gitleaks to the dev shell, `leaks` to `ci-check`'s
  `checks`, and `leaks` to the matrix.
- [ ] **Step 4: Run it.** `go test -run TestCheckLeaks` passes. `scripts/ci-check leaks` passes on this branch.
- [ ] **Step 5: Commit.** `feat(ci): leaks, gitleaks on a pull request's commits`

### Task 2: `iac`

**Files:**
- Create: `scripts/check-iac`, `.trivyignore.yaml`
- Modify: `flake.nix` (unstable trivy), `cli/Dockerfile` (`USER 65532:65532`), `scripts/ci-check`, `checks.yaml`
  (matrix; Trivy's cache step, shared with Task 3)
- Test: `TestCheckIaC`

- [ ] **Step 1: Write the failing test.** `TestCheckIaC` runs `scripts/check-iac` with `TRIVY` pointing at a fake that
  records its arguments and fails when told to.
  - The arguments include `config`, `--severity HIGH,CRITICAL`, `--exit-code 1`, `--ignorefile .trivyignore.yaml` and
    the four targets.
  - A failing fake fails the check.
- [ ] **Step 2: Run it.** It fails.
- [ ] **Step 3: Implement.**
  - The script runs one `trivy config` per target (`infra`, `cli`, `Dockerfile`, `metal/roles/pxe_server/files`) and
    skips `.terragrunt-cache` and `.terraform`.
  - `.trivyignore.yaml`: DS-0002 for the root `Dockerfile` (upstream's docs image, not deployed here) and for `metal`'s
    two images (dnsmasq binds port 67 as root), each with its path and statement.
  - `cli/Dockerfile` gets `USER 65532:65532`.
  - CI caches `~/.cache/trivy` by day.
- [ ] **Step 4: Run it.** The test passes. `scripts/ci-check iac` passes with the real Trivy.
- [ ] **Step 5: Commit.** `feat(ci): iac, Trivy on OpenTofu and the Dockerfiles`

### Task 3: `images`

**Files:**
- Create: `scripts/check-images`
- Modify: `scripts/ci-check`, `checks.yaml`
- Test: `TestCheckImages`

**Interfaces:**
- `scripts/check-images` lists targets with `go -C cli run ./cmd/lab test charts --base "$BASE"`, renders each with
  `scripts/render-chart` at HEAD and at `$BASE` (a `git worktree` of BASE; a target missing there has no old images),
  and scans the new images.
- `IMAGES_ONLY=1` prints the new images without scanning, for the test.

- [ ] **Step 1: Write the failing test.** `TestCheckImages` runs on a temp copy of a small chart. With `IMAGES_ONLY=1`:
  - one image changed between BASE and HEAD, plus one digest-pinned image with a registry port: only the changed and
    the new image are printed;
  - a target whose render fails fails the check.

  With a fake `TRIVY` that reports a finding, the check fails.
- [ ] **Step 2: Run it.** It fails.
- [ ] **Step 3: Implement.** Extract images with `yq '.. | select(has("image")) | .image | select(tag == "!!str")'`
  over the renders, then sort and deduplicate. Scan with `trivy image --severity CRITICAL --ignore-unfixed --exit-code 1
  --ignorefile .trivyignore.yaml`, and `--skip-db-update` when `TRIVY_SKIP_DB_UPDATE=1` (CI's cache hit).
- [ ] **Step 4: Run it.** The test passes. `scripts/ci-check images` on this branch prints that nothing is new.
- [ ] **Step 5: Commit.** `feat(ci): images, Trivy on the images a change brings`

### Task 4: Docs, checks, review, PR

- [ ] **Step 1: Docs.**
  - `docs/how-to-guides/ci.md`: the three checks and what to do about a finding.
  - CLAUDE.md: 15a done; the basic checks list in Rules; the unencrypted state line under "Before the first production
    deploy".
  - `deferred-minors.md`: the root `Dockerfile`'s unpinned images.
- [ ] **Step 2: Run the checks.** `scripts/ci-check all` passes. `make dev` prints that there is nothing to test: CI and
  scripts only, all skipped. If it does not, run what it selects.
- [ ] **Step 3: Review and PR.** A fresh reviewer agent reviews the branch. Fix Critical and Important. Open the PR
  into `dev`; the `checks` workflow runs the three new jobs on the PR.
