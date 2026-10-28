# Sandbox Optional Apps (Step 14e) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Paperless and OpenCloud deploy in the local sandbox only when their files change or `apps=` asks. The release
sandbox keeps everything, and Prometheus keeps one day there.

**Spec:** `docs/superpowers/specs/2026-10-28-sandbox-optional-apps-design.md`

## Review Focus

1. **`lab secrets rotate --all` in production** must not skip anything: only an entry whose reader namespaces are all
   missing is skipped. Test: `TestRotateAllSkipsMissingNamespaces` keeps an entry whose namespace exists.
2. **A named entry** (`lab secrets rotate paperless.admin`) whose app is not deployed fails clearly; it is not
   skipped. Test: the same test.
3. **The release workflows** pass `apps=all`. Test: actionlint, plus a grep in `TestSandboxWorkflowsAllApps`.

### Task 1: rotation skips what is not deployed (`cli/internal/rotate`)
- [ ] `TestRotateAllSkipsMissingNamespaces` (red), then: `Rotator.Run` with `o.All` checks each entry's reader
  namespaces through an injected `NamespaceExists`; when none exist the result is `skipped: not deployed`. Green;
  commit.

### Task 2: the optional list reaches Argo CD
- [ ] `infra/_modules/argocd/set-sandbox-apps.sh` with an offline test `TestSetSandboxApps` (red first), the module
  variable `optional_apps` (in `triggers_replace`), `infra/sandbox/argocd/terragrunt.hcl`
  (`HOMELAB_SANDBOX_APPS`), and `values-dev.yaml` without paperless and opencloud. Commit.

### Task 3: make dev, workflows, tests, Prometheus, docs
- [ ] Makefile `optional_apps = paperless opencloud`, `apps=`, auto from changed files; the workflows' `apps=all`
  (`TestSandboxWorkflowsAllApps`); `deployed(t, app)` skips in the SSO and smoke tests; Prometheus `retention: 1d` in
  `values-dev.yaml`; docs and CLAUDE.md. `scripts/ci-check all`, then `make dev` (base + paperless, to prove both
  paths), review, PR.
