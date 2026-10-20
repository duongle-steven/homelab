# Step 10b: The Sandbox on GitHub Actions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** GitHub Actions builds a fresh sandbox and runs the suite on pushes to `dev`, on pull requests labelled
`sandbox`, and on demand, testing a commit Argo CD is told about at bootstrap.

**Architecture:** Argo CD stops managing itself (upstream `nixos`'s model): `infra/_modules/argocd` alone applies
`system/argocd`, rewriting the homelab repository's revisions to a chosen one (`set-revision.sh`); the CRD-gated
templates move to `system/argocd-config`, which Argo CD syncs. `make dev revision=<sha>` tests that commit without
pushing branch `sandbox`; `.github/workflows/sandbox.yaml` calls it.

**Tech Stack:** Argo CD ApplicationSets, Helm, yq, OpenTofu/Terragrunt, k3d, GitHub Actions, Go tests.

**Spec:** `docs/superpowers/specs/2026-10-19-ci-sandbox-design.md`

## Global Constraints

- Branch `feat/ci-sandbox` from `dev`, in the main checkout `/home/nixos/homelab` (never `make dev` from a worktree).
- Actions pinned by SHA as in `checks.yaml` (`actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1  # v7.0.1`,
  `cachix/install-nix-action@448cde739abec1eaf0646249e3d25bb8d9bc7702  # v31.11.2`,
  `nix-community/cache-nix-action@7df957e333c1e5da7721f60227dbba6d06080569  # v7.0.2`); `actions/upload-artifact` at
  its latest release, pinned the same way (look it up: `gh api repos/actions/upload-artifact/releases/latest`).
- `permissions: contents: read`; no `${{ }}` inside `run:`.
- Without a revision, the bootstrap's output is unchanged.
- The sandbox needs no secret; the state passphrase is random per run.

## Review Focus

1. **The migration on an existing cluster:** removing `system/argocd` from `root` makes the ApplicationSet delete
   Application `argocd`, whose `resources-finalizer` would delete Argo CD itself. Pinned in Task 2 (the bootstrap
   drops that finalizer first; `make dev` on the existing sandbox, then Argo CD still runs).
2. **Revisions of other repositories:** app-template's `targetRevision` (a chart version) and the seed placeholder must
   not change. Pinned in Task 1 (`TestArgoCDRevision`).
3. **A fork's head SHA:** reachable only through `refs/pull/<n>/head`. Pinned in Task 5 (dispatch with such a SHA).
4. **Nothing to test on a push:** the job must succeed fast without building the sandbox. Pinned in Task 4 (the
   workflow's scope step) and Task 5 (a docs-only push after merge, or the dispatch with a docs-only range).
5. **Ports on the runner:** the k3d module's localhost proxy refuses busy ports 80/443/22. Pinned in Task 3
   (`HOMELAB_SANDBOX_PROXY=0` skips it; the tests dial the node).

---

### Task 1: `set-revision.sh` and its offline test

**Files:**
- Create: `infra/_modules/argocd/set-revision.sh`, `test/argocd_revision_test.go`
- Modify: `scripts/ci-check` (offline test list gains `TestArgoCDRevision`)

**Interfaces:**
- Produces: `sh infra/_modules/argocd/set-revision.sh <revision>` reads rendered manifests on stdin, writes them on
  stdout; with an empty revision it copies stdin unchanged. The repository is the `root` ApplicationSet's first git
  generator's `repoURL`.

- [ ] **Step 1: Write the failing test** (`test/argocd_revision_test.go`)

```go
package test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// render renders system/argocd for the sandbox the way the bootstrap does, through set-revision.sh
func renderArgoCD(t *testing.T, revision string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/argocd dev | sh infra/_modules/argocd/set-revision.sh "$0"`, revision)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

// The bootstrap points every source and generator of the homelab repository at the chosen commit, and nothing else
func TestArgoCDRevision(t *testing.T) {
	plain := renderArgoCD(t, "")
	require.Contains(t, plain, "revision: sandbox")
	pinned := renderArgoCD(t, "abc123")
	assert.NotContains(t, pinned, "revision: sandbox")
	assert.NotContains(t, pinned, "targetRevision: sandbox")
	assert.Contains(t, pinned, "revision: abc123")
	assert.Contains(t, pinned, "targetRevision: abc123")
	// app-template's chart version is another repository's revision
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "targetRevision:") && !strings.Contains(line, "sandbox") {
			assert.Contains(t, pinned, line, "a revision of another repository changed")
		}
	}
	assert.Equal(t, strings.Count(plain, "sandbox"), strings.Count(pinned, "abc123")+strings.Count(pinned, "sandbox"),
		"every homelab revision was rewritten, and only those")
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `direnv exec . env -C test go test -run TestArgoCDRevision ./... 2>&1 | grep -E -- '--- |^(ok|FAIL)|No such file' | head -3`
Expected: FAIL (`set-revision.sh`: No such file).

- [ ] **Step 3: Write `infra/_modules/argocd/set-revision.sh`**

```sh
#!/bin/sh
# Point every source and generator of the homelab repository in rendered Argo CD manifests (stdin) at one revision
# (stdout): the sandbox on GitHub Actions tests a commit without pushing branch sandbox. The repository is the root
# ApplicationSet's first git generator's; other repositories (app-template's chart) keep theirs. No revision: unchanged.
set -eu
revision=${1:-}
if [ -z "$revision" ]; then
	exec cat
fi
manifests=$(cat)
repo=$(printf '%s\n' "$manifests" | yq 'select(.kind == "ApplicationSet" and .metadata.name == "root") | .spec.generators[0].matrix.generators[0].git.repoURL')
[ -n "$repo" ] && [ "$repo" != null ] || { echo "set-revision: no root ApplicationSet git generator" >&2; exit 1; }
printf '%s\n' "$manifests" | REPO="$repo" REVISION="$revision" yq '
  (.. | select(tag == "!!map" and .repoURL == strenv(REPO) and has("revision")) | .revision) = strenv(REVISION) |
  (.. | select(tag == "!!map" and .repoURL == strenv(REPO) and has("targetRevision")) | .targetRevision) = strenv(REVISION)'
```

In `scripts/ci-check`, append `|TestArgoCDRevision` inside `offline_tests` (after `TestState`).

- [ ] **Step 4: Run it**

Run: `direnv exec . env -C test go test -run TestArgoCDRevision -v ./... 2>&1 | grep -E -- '--- |^(ok|FAIL)'; direnv exec . scripts/lint; echo lint=$?`
Expected: PASS; `lint=0`.

- [ ] **Step 5: Commit**

```bash
git add infra/_modules/argocd/set-revision.sh test/argocd_revision_test.go scripts/ci-check
git commit -m "feat(infra): set-revision.sh points the homelab repository's Argo CD sources at one revision"
```

---

### Task 2: Argo CD stops managing itself

**Files:**
- Create: `system/argocd-config/Chart.yaml`, `system/argocd-config/values.yaml`; move
  `system/argocd/templates/kanidm-client.yaml` and `forgejo-repository.yaml` there (`git mv`)
- Modify: `system/argocd/values.yaml`, `values-dev.yaml`, `values-seed.yaml` (root generator: `system/argocd` →
  `system/argocd-config`), `infra/_modules/argocd/main.tf` (variable `revision`; one resource; triggers),
  `infra/_modules/argocd/bootstrap.sh` (finalizer migration; `set-revision.sh`), `infra/sandbox/argocd/terragrunt.hcl`
  (`revision`), `infra/production/argocd/terragrunt.hcl` (drop `protect`), `test/scopes.yaml`
  (`system/argocd-config/**` in `full`), `scripts/render-chart` comment if it names the moved templates

**Interfaces:**
- Consumes: `set-revision.sh <revision>` (Task 1).
- Produces: module variable `revision` (string, default `""`); env `HOMELAB_SANDBOX_REVISION` read by the sandbox unit.

- [ ] **Step 1: Read what the moved templates need**

Run: `head -20 system/argocd/templates/kanidm-client.yaml system/argocd/templates/forgejo-repository.yaml; grep -n 'Values' system/argocd/templates/kanidm-client.yaml system/argocd/templates/forgejo-repository.yaml`
Expected: the values they read (e.g. `global.baseDomain`, Argo CD's host). Copy exactly those keys into
`system/argocd-config/values.yaml` (with the same defaults as `system/argocd/values.yaml`) so the templates render
unchanged; record each in the ledger.

- [ ] **Step 2: Create the chart and move the templates**

```yaml
# system/argocd-config/Chart.yaml
apiVersion: v2
name: argocd-config
description: Argo CD's resources that need other components' CRDs (its Kanidm client, the Forgejo repository credential); Argo CD syncs them, infra/_modules/argocd bootstraps Argo CD itself
version: 0.0.0
```

```bash
git mv system/argocd/templates/kanidm-client.yaml system/argocd/templates/forgejo-repository.yaml system/argocd-config/templates/
```

In the three values files, replace `- path: system/argocd` in the `root` generator's directories with
`- path: system/argocd-config`. Its Application deploys into namespace `argocd` (the root template's destination is the
directory's base name: check `values.yaml`'s template; if it is `{{ .path.basename }}`, add an `argocd-config` →
`argocd` rule the same way other components set their namespace, or set the destination in a `templatePatch`; ledger
the choice).

- [ ] **Step 3: Render and check**

Run: `direnv exec . sh -c 'for e in "" dev; do scripts/render-chart system/argocd-config $e | grep -c "^kind:"; scripts/render-chart system/argocd $e | grep -cE "KanidmOAuth2Client|name: argocd-repo-forgejo"; done; scripts/check-env-config >/dev/null && echo env=0; scripts/check-mesh-policies >/dev/null && echo mesh=0; scripts/check-secret-stores >/dev/null && echo stores=0'`
Expected: `2` and `0` for both environments; `env=0`, `mesh=0`, `stores=0`.

- [ ] **Step 4: The module and bootstrap**

`infra/_modules/argocd/main.tf`: add

```hcl
variable "revision" {
  type        = string
  default     = ""
  description = "Commit or branch every homelab source and generator syncs (set-revision.sh); empty keeps the values file's"
}
```

pass `REVISION = var.revision` in `local.environment`, add `var.revision` to `triggers_replace`, and replace the two
resources by one (Argo CD no longer manages itself, so every environment re-applies on change):

```hcl
# Re-applied whenever the chart's files, the values, the revision or the cluster change: Argo CD does not manage
# itself (upstream nixos's model). It has no destroy-time action: destroying it leaves Argo CD running.
resource "terraform_data" "bootstrap" {
  triggers_replace = [local.hash, var.values, var.seed, var.cluster_id, var.revision]

  provisioner "local-exec" {
    command     = local.command
    environment = local.environment
  }
}
```

Remove `variable "protect"` and the header's "every later change reaches the cluster through Argo CD's own sync";
rewrite the header: "Apply Argo CD from the repository's system/argocd chart, again whenever its files, the values, the
revision or the cluster change; Argo CD syncs everything else (system/argocd-config among it), not itself." Production
state holds `terraform_data.protected[0]`: add `moved { from = terraform_data.protected[0] to = terraform_data.bootstrap }`
and `moved { from = terraform_data.bootstrap[0] to = terraform_data.bootstrap }` so neither environment re-runs only
because of the rename.

(Ruling recorded here: `prevent_destroy` cannot coexist with re-applying, which replaces the resource; the resource
has no destroy-time action, so the guard protected nothing in the cluster. It goes.)

`bootstrap.sh`: after the `helm template … > "$manifests"` line, add

```sh
# The sandbox on GitHub Actions syncs one commit (REVISION) instead of branch sandbox
sh "$(dirname "$0")/set-revision.sh" "${REVISION:-}" < "$manifests" > "$manifests.rev" && mv "$manifests.rev" "$manifests"
```

and before the first `apply` call:

```sh
# Argo CD no longer manages itself: the root ApplicationSet drops Application argocd, whose resources finalizer
# would delete Argo CD with it. Release it first (a no-op on a new cluster).
if kubectl -n argocd get application argocd >/dev/null 2>&1; then
	kubectl -n argocd patch application argocd --type merge -p '{"metadata":{"finalizers":null}}' >/dev/null
fi
```

Update the script's header comment ("apply it once; Argo CD manages itself from then on" → "apply it; Argo CD does
not manage itself"). `bootstrap.sh` is called as `sh <path>`: `$0` is that path. `trap` also removes
`"$manifests.rev"`.

`infra/sandbox/argocd/terragrunt.hcl` inputs: `revision = get_env("HOMELAB_SANDBOX_REVISION", "")`.
`infra/production/argocd/terragrunt.hcl`: remove `protect = true`; replace its comment with "Re-applied when
system/argocd changes; the first run after Forgejo runs switches from the seed to ops/homelab".

`test/scopes.yaml`: add `- system/argocd-config/**` next to `- system/argocd/**` in `full`.

- [ ] **Step 5: Basic checks, then the migration on the existing sandbox**

Run: `direnv exec . scripts/ci-check all > <scratchpad>/ci-10b.log 2>&1; echo all=$?` then commit (Step 6), then
`direnv exec . make dev > <scratchpad>/dev-10b.log 2>&1; echo dev=$?; grep -aE '^make dev:|^DONE|PASS TestNodeRestart |--- FAIL' <scratchpad>/dev-10b.log | tail -5; KUBECONFIG=infra/sandbox/kubeconfig.yaml direnv exec . kubectl -n argocd get applications argocd argocd-config --no-headers 2>&1 | cut -c1-80; KUBECONFIG=infra/sandbox/kubeconfig.yaml direnv exec . kubectl -n argocd get deploy argocd-server -o name`
Expected: `all=0`; `dev=0`, 123 tests and `TestNodeRestart` pass; Application `argocd` NotFound, `argocd-config`
Synced Healthy; `deployment.apps/argocd-server` exists (the finalizer migration kept Argo CD).

- [ ] **Step 6: Commit** (before Step 5's `make dev`)

```bash
git add system/argocd system/argocd-config infra test/scopes.yaml scripts
git commit -m "feat: Argo CD no longer manages itself; its CRD-gated resources move to system/argocd-config; the bootstrap takes a revision"
```

---

### Task 3: `make dev revision=` and the proxy switch

**Files:**
- Modify: `Makefile` (`revision ?=`; `dev` skips `sandbox-push` and exports `HOMELAB_SANDBOX_REVISION` when set),
  `infra/_modules/k3d/main.tf` (variable `proxy`, default `true`), `infra/sandbox/cluster/terragrunt.hcl`
  (`proxy = get_env("HOMELAB_SANDBOX_PROXY", "1") != "0"`), `docs/how-to-guides/sandbox-tests.md`

- [ ] **Step 1: See the current behaviour (the failing state)**

Run: `make -n dev revision=abc123 tests=Smoke 2>/dev/null | grep -c 'git push --force origin HEAD:sandbox\|sandbox-push'`
Expected: `1` or more (it would push).

- [ ] **Step 2: Implement**

In the `Makefile`, add `revision ?=` under `tests ?=`, and in `dev`'s recipe replace `$(MAKE) sandbox-push && \` with
`if [ -z "$(revision)" ]; then $(MAKE) sandbox-push; fi && \` and `(cd infra/sandbox && $(terragrunt_apply))` with
`(cd infra/sandbox && HOMELAB_SANDBOX_REVISION="$(revision)" $(terragrunt_apply))`. Extend the comment above `dev`:
"revision=<sha> syncs that commit instead (GitHub Actions: nothing is pushed)".

In `infra/_modules/k3d/main.tf` add `variable "proxy" { type = bool, default = true, description = "Forward
127.0.0.1:80/443/22 to the node (the browser under WSL); GitHub Actions turns it off" }` and `count = var.proxy ? 1 : 0`
on `terraform_data.proxy` (and on anything that references it; add `moved { from = terraform_data.proxy to =
terraform_data.proxy[0] }`).

In `docs/how-to-guides/sandbox-tests.md`'s table add the row "`make dev revision=<sha>` | The same selection against
that commit, without pushing branch `sandbox` (what GitHub Actions runs, `docs/how-to-guides/ci.md`)".

- [ ] **Step 3: Check**

Run: `make -n dev revision=abc123 tests=Smoke 2>/dev/null | grep -c 'HEAD:sandbox'; make -n dev revision=abc123 tests=Smoke 2>/dev/null | grep -c 'HOMELAB_SANDBOX_REVISION="abc123"'; direnv exec . make -C infra check >/dev/null 2>&1; echo infra=$?; direnv exec . sh -c 'cd infra/sandbox/cluster && terragrunt plan --non-interactive --log-level error -detailed-exitcode >/dev/null 2>&1; echo plan=$?'`
Expected: `0`, `1`, `infra=0`, `plan=0` (the `moved` block: no change on the existing sandbox).

- [ ] **Step 4: Commit**

```bash
git add Makefile infra/_modules/k3d/main.tf infra/sandbox/cluster/terragrunt.hcl docs/how-to-guides/sandbox-tests.md
git commit -m "feat: make dev revision=<sha> tests a commit without pushing branch sandbox; the localhost proxy can be off"
```

---

### Task 4: The workflow and the docs

**Files:**
- Create: `.github/workflows/sandbox.yaml`
- Modify: `docs/how-to-guides/ci.md`, the installation docs that describe Argo CD's bootstrap
  (`grep -rln 'manages itself\|bootstrap' docs/installation`), `CLAUDE.md`

- [ ] **Step 1: Write `.github/workflows/sandbox.yaml`** (pin `actions/upload-artifact` to its latest release's SHA)

```yaml
# A fresh sandbox and the suite (make dev revision=<sha>): after every push to dev, on pull requests labelled sandbox,
# and on demand. Not a required check: it runs after the merge, or when a maintainer asks for it.
name: sandbox

on:
  push:
    branches: [dev]
  pull_request:
    types: [labeled]
  workflow_dispatch:
    inputs:
      scope:
        description: auto (the change since dev) or full
        default: full
        type: choice
        options: [full, auto]
      tests:
        description: exactly these tests (a go test -run regex); empty for the scope
        default: ""
      revision:
        description: commit to test (default the branch's head)
        default: ""

permissions:
  contents: read

concurrency:
  group: ${{ github.workflow }}-${{ github.event_name == 'pull_request' && github.ref || github.run_id }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}

jobs:
  sandbox:
    if: github.event_name != 'pull_request' || github.event.label.name == 'sandbox'
    runs-on: ubuntu-24.04
    timeout-minutes: 90
    env:
      HOMELAB_SANDBOX_PROXY: "0"
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1  # v7.0.1
        with:
          fetch-depth: 0  # the scope diffs against the base
          persist-credentials: false
          ref: ${{ github.event.pull_request.head.sha || inputs.revision || github.sha }}
      - name: free disk space and raise inotify limits
        run: |
          sudo rm -rf /usr/share/dotnet /usr/local/lib/android /opt/ghc /opt/hostedtoolcache/CodeQL
          sudo sysctl -w fs.inotify.max_user_instances=8192 fs.inotify.max_user_watches=1048576
          df -h /
      - uses: cachix/install-nix-action@448cde739abec1eaf0646249e3d25bb8d9bc7702  # v31.11.2
      - uses: nix-community/cache-nix-action@7df957e333c1e5da7721f60227dbba6d06080569  # v7.0.2
        with:
          primary-key: nix-${{ runner.os }}-${{ hashFiles('flake.nix', 'flake.lock') }}
          restore-prefixes-first-match: nix-${{ runner.os }}-
          gc-max-store-size-linux: 8G
          save: ${{ github.event_name == 'push' }}
      - name: make dev
        env:
          EVENT: ${{ github.event_name }}
          BEFORE: ${{ github.event.before }}
          PR_BASE: ${{ github.event.pull_request.base.sha }}
          SCOPE: ${{ inputs.scope }}
          TESTS: ${{ inputs.tests }}
        run: |
          revision=$(git rev-parse HEAD)
          HOMELAB_STATE_PASSPHRASE=$(head -c 32 /dev/urandom | base64)
          export HOMELAB_STATE_PASSPHRASE
          case "$EVENT" in
            push)
              # Full, unless every change since the previous push is skipped (a new branch: full)
              if [ "$BEFORE" != 0000000000000000000000000000000000000000 ] && \
                [ -z "$(nix develop --command sh -c "cd cli && go run ./cmd/lab test scope --base $BEFORE")$(nix develop --command sh -c "cd cli && go run ./cmd/lab test scope --base $BEFORE --alone")" ]; then
                echo "nothing to test in the sandbox"; exit 0
              fi
              set -- scope=full ;;
            pull_request) set -- "base=$PR_BASE" ;;
            workflow_dispatch) if [ -n "$TESTS" ]; then set -- "tests=$TESTS"; else set -- "scope=$SCOPE" base=origin/dev; fi ;;
          esac
          nix develop --command make dev "revision=$revision" "$@" 2>&1 | tee make-dev.log
      - name: collect the cluster's state
        if: failure()
        run: |
          mkdir -p sandbox-state
          cp make-dev.log sandbox-state/ 2>/dev/null || true
          export KUBECONFIG=infra/sandbox/kubeconfig.yaml
          nix develop --command sh -c '
            kubectl get pods -A -o wide > sandbox-state/pods.txt 2>&1
            kubectl -n argocd get applications > sandbox-state/applications.txt 2>&1
            kubectl get pods -A --no-headers | awk "\$3 !~ /^([0-9]+)\/\1$/ && \$4 != \"Completed\" {print \$1, \$2}" |
              while read -r ns pod; do kubectl -n "$ns" logs "$pod" --all-containers --tail=200 > "sandbox-state/$ns.$pod.log" 2>&1; done'
      - uses: actions/upload-artifact@<sha>  # <latest version>
        if: failure()
        with:
          name: sandbox-state
          path: sandbox-state/
```

Note on `tee`: the step's shell is `bash -e {0}` without `pipefail`, so add `set -o pipefail` as the `run` block's
first line. The `push` scope check uses `$BEFORE` inside a `sh -c` string: it is a SHA from GitHub, but pass it as
`"$1"` instead (`sh -c 'cd cli && go run ./cmd/lab test scope --base "$1"' sh "$BEFORE"`) so no event value is
interpolated into a script.

- [ ] **Step 2: Lint**

Run: `direnv exec . sh -c 'actionlint && scripts/lint && echo ok'; awk '/^ *run:/ && /\$\{\{/' .github/workflows/sandbox.yaml | wc -l`
Expected: `ok`, `0`.

- [ ] **Step 3: Docs and CLAUDE.md**

- `docs/how-to-guides/ci.md`: a section "The sandbox" (the three triggers, the label and its rerun by re-labelling, the
  artifact `sandbox-state`, forks, `workflow_dispatch` inputs, the time it takes).
- Installation docs: Argo CD's own changes reach production with `make infra env=production`; the first run after
  Forgejo runs switches from the seed to `ops/homelab`.
- CLAUDE.md: Sandbox bullets (Argo CD no longer manages itself; `system/argocd-config`; `make dev revision=`;
  `HOMELAB_SANDBOX_PROXY`); the Rules bullet ("After the merge, run `make dev scope=full` on `dev`" → "After the merge,
  the workflow `sandbox` runs the whole suite on `dev` (check it)"; "Before a `dev` → `master` PR or a release, run
  `make dev-fresh`" → "… the latest `sandbox` run on `dev` must be green (a fresh sandbox; rerun it with
  `workflow_dispatch`)"); the production checklist's sentence on Argo CD's switch to Forgejo (the first
  `make infra env=production` after Forgejo runs) and on `infra/production/argocd` ("bootstraps once" → "re-applied
  when `system/argocd` changes"); roadmap 10b done.

- [ ] **Step 4: Build and commit**

Run: `direnv exec . mkdocs build --strict 2>&1 | grep -aE 'WARNING|built in' | tail -1`
Expected: built, no warning.

```bash
git add .github/workflows/sandbox.yaml docs CLAUDE.md
git commit -m "feat(ci): the sandbox on GitHub Actions: pushes to dev, pull requests labelled sandbox, dispatch"
```

---

### Task 5: Runs on GitHub

- [ ] **Step 1: Push, open the pull request, label it**

```bash
git push -u origin feat/ci-sandbox
gh label create sandbox --description "Run the sandbox suite on GitHub Actions" --color 1d76db 2>/dev/null || true
gh pr create --base dev --head feat/ci-sandbox --title "Step 10b: the sandbox on GitHub Actions" --body-file <scratchpad>/pr-10b.md
gh pr edit <number> --add-label sandbox
```

(`gh label create` changes repository settings: ask the user first.)

- [ ] **Step 2: Watch it**

Run: `gh run watch <run-id> --exit-status --interval 60 > /dev/null 2>&1; echo watch=$?; gh run view <run-id> --json jobs --jq '.jobs[] | "\(.name) \(.conclusion) \(.startedAt) \(.completedAt)"'`
Expected: success, about 45 minutes. A failure: download `sandbox-state` (`gh run download <run-id>`), find the cause
(systematic debugging), fix with a failing offline test first where the cause is code, ledger it, push, re-label.
Docker Hub `toomanyrequests` in pod events: add the CI-only mirror (k3d `registries` config pointing `docker.io` at
`mirror.gcr.io`, enabled by an env var the workflow sets), ledger it.

- [ ] **Step 3: A fork's head SHA**

Create a throwaway branch with an empty commit, open a draft pull request from it, close it and delete the branch, so
its head is reachable only through `refs/pull/<n>/head`; then
`gh workflow run sandbox.yaml --ref feat/ci-sandbox -f tests=Smoke -f revision=<that sha>` and watch it.
Expected: success (Argo CD fetched the SHA). If it fails on fetching the revision, the docs say the label covers
same-repository pull requests only; ledger it either way.

- [ ] **Step 4: The PR body** with timings, the runs, the fork result, the rulings; the push run on `dev` is checked
  after the merge.
