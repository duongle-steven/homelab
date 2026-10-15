# Sandbox Branch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `make dev` force-pushes the current commit to GitHub branch `sandbox`, which the sandbox's Argo CD syncs, so feature branches pass the sandbox tests before their merge.

**Architecture:** `system/argocd/values-dev.yaml` points both ApplicationSets at revision `sandbox`. The root `Makefile` gains `sandbox-push` (refuses uncommitted changes, then `git push --force origin HEAD:sandbox`) before the cluster steps and `sandbox-refresh` (asks Argo CD to re-read git) before the tests.

**Tech Stack:** GNU/POSIX make, git, kubectl, Argo CD annotations.

**Spec:** `docs/superpowers/specs/2026-10-15-sandbox-branch-design.md`

## Global Constraints

- Branch name `sandbox` on `origin` (`github.com/duongle-steven/homelab`); overwritten by every `make dev`.
- Production and staging values (`values.yaml`, `values-seed.yaml`) do not change.
- Uncommitted changes to tracked files stop `make dev` before anything is pushed or created; untracked files (`.direnv/`, `site/`) are ignored.

## Review Focus

1. A refresh on a fresh cluster (no Argo CD yet) must not fail `make dev`. Pinned in Task 1 (the refresh ignores errors) and proven by Task 3's fresh build.
2. New apps added on a branch appear only after the ApplicationSet controller re-reads git. Expected: the refresh also annotates the ApplicationSets. Pinned in Task 1.
3. Running `make dev` on `dev` after a merge pushes `dev`'s commit to `sandbox` (no special case). Covered by the workflow docs (Task 2).
4. A push failure (offline, auth) stops before the cluster steps. Pinned by the target order in Task 1.
5. Forgejo's seed copies GitHub's default branch, not `sandbox`; the seed tests only need `master` to exist. Unchanged; noted in Task 2's docs.

---

### Task 1: `sandbox` branch in Argo CD and `make dev`

**Files:**
- Modify: `system/argocd/values-dev.yaml:42` (`revision: &revision dev`)
- Modify: `Makefile` (`dev` target)

- [ ] **Step 1: Write the check (fails before the change)**

Create `.superpowers/sdd/2026-10-15-sandbox-branch/check-task1.sh`:

```sh
#!/bin/sh
# The sandbox's ApplicationSets read branch sandbox; make dev refuses uncommitted changes before pushing
set -e
f=$(mktemp); trap 'rm -f "$f"; git checkout -q -- README.md' EXIT
scripts/render-chart system/argocd dev > "$f" 2>/dev/null
r=$(yq ea '[select(.kind == "ApplicationSet") | (.spec.generators[].git.revision, .spec.template.spec.source.targetRevision, .spec.template.spec.sources[].targetRevision) | select(. != null)] | unique | join(",")' "$f")
echo "sandbox revisions: $r"
test "$r" = "5.3.0,sandbox"
echo " " >> README.md
out=$(make sandbox-push 2>&1 || true)
echo "$out" | grep -q "commit your changes first"
echo "make dev refuses uncommitted changes"
```

Run: `chmod +x .superpowers/sdd/2026-10-15-sandbox-branch/check-task1.sh && .superpowers/sdd/2026-10-15-sandbox-branch/check-task1.sh`
Expected: FAIL with `sandbox revisions: 5.3.0,dev`.

- [ ] **Step 2: `values-dev.yaml`**

Replace `            revision: &revision dev` with:

```yaml
            # make dev force-pushes the current commit here, so a branch is tested before its merge
            revision: &revision sandbox
```

- [ ] **Step 3: `Makefile`**

Replace the `dev:` target with:

```make
# The sandbox syncs GitHub branch sandbox (system/argocd/values-dev.yaml): test the commit you are on
dev: sandbox-push
	$(MAKE) env=dev ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='Smoke|Identity|SSO|Gateway|Forgejo|Mesh' metal system sandbox-refresh smoke-test

sandbox-push:
	@if [ -n "$$(git status --porcelain --untracked-files=no)" ]; then \
		echo "commit your changes first: make dev tests the commit at HEAD"; \
		git status --short --untracked-files=no; exit 1; \
	fi
	git push --force origin HEAD:sandbox

# An existing sandbox picks up the new commit now instead of at the next poll; on a fresh one there is nothing yet
sandbox-refresh:
	-@kubectl -n argocd annotate applicationsets --all argocd.argoproj.io/application-set-refresh=true --overwrite >/dev/null 2>&1
	-@kubectl -n argocd annotate applications --all argocd.argoproj.io/refresh=normal --overwrite >/dev/null 2>&1
```

- [ ] **Step 4: Run the check**

Run: `.superpowers/sdd/2026-10-15-sandbox-branch/check-task1.sh`
Expected: `sandbox revisions: 5.3.0,sandbox` and `make dev refuses uncommitted changes`.

- [ ] **Step 5: Commit**

```bash
git add system/argocd/values-dev.yaml Makefile
git commit -m "feat: make dev tests the current commit through GitHub branch sandbox"
```

### Task 2: Docs

**Files:**
- Modify: `CLAUDE.md`, `docs/installation/sandbox.md`

- [ ] **Step 1: `CLAUDE.md`**

- Rules: replace "the full sandbox tests (`make dev`) run after the PR is merged into `dev`. If `dev` breaks, fix it with a new PR right away." with "then commit and run `make dev`, which force-pushes the commit to GitHub branch `sandbox` and runs the full sandbox tests on it, before opening the PR; after the merge, run `make dev` on `dev` once more. If `dev` breaks, fix it with a new PR right away."
- Sandbox: replace "ArgoCD in the sandbox syncs from `github.com/duongle-steven/homelab` branch `dev`; push to see changes." with "ArgoCD in the sandbox syncs `github.com/duongle-steven/homelab` branch `sandbox`, which `make dev` force-pushes to the current commit (it refuses uncommitted changes) and then refreshes; nothing merges from `sandbox`."

- [ ] **Step 2: `docs/installation/sandbox.md`**

- In the "Make changes" code block, before `git push -u origin feat/harness-the-sun`, add:

  ```sh
  git commit -am "feat: harness the sun"
  make dev   # pushes this commit to the branch sandbox and runs every sandbox test on it
  ```

- Replace the paragraph "ArgoCD in the sandbox syncs from the `dev` branch on GitHub, so a change reaches the sandbox once its PR is merged. Then run `make dev` to run …" with: "ArgoCD in the sandbox syncs the GitHub branch `sandbox`. `make dev` force-pushes the commit you are on to it (so commit first) and runs the smoke, identity, SSO (including OpenCloud), Gateway, Forgejo and mesh tests against it, so a branch is tested before its pull request. After the merge, run `make dev` on `dev` once more. `dev` is merged into `master` through periodic pull requests."
- In "Differences from production", change "Argo CD still syncs GitHub branch `dev`, not Forgejo" to "Argo CD syncs GitHub branch `sandbox` (Forgejo's seed still copies the default branch), not Forgejo".

- [ ] **Step 3: Check and commit**

Run: `grep -n "branch \`dev\`\|after the PR is merged" CLAUDE.md docs/installation/sandbox.md; mkdocs build --strict 2>&1 | tail -1`
Expected: no grep output; `Documentation built`.

```bash
git add CLAUDE.md docs/installation/sandbox.md
git commit -m "docs: make dev tests the current commit"
```

### Task 3: Prove it on this branch

- [ ] **Step 1: Fresh sandbox from this branch's commit**

Run: `make dev-clean; make dev 2>&1 | tee .superpowers/sdd/2026-10-15-sandbox-branch/make-dev.log | tail -5`
Expected: `git push --force origin HEAD:sandbox` succeeds; every test passes (the same count as the last `dev` run: 93).

- [ ] **Step 2: Argo CD reads `sandbox`**

Run: `kubectl -n argocd get applications -o jsonpath='{range .items[*]}{.metadata.name} {.status.sync.revision}{"\n"}{end}' | head -3; git rev-parse HEAD`
Expected: the applications' revision equals this branch's HEAD commit.
