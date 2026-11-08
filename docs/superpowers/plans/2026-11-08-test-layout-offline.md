# 14f-1: offline tests in their own package — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the 131 offline tests leave package `test` for `test/offline` and `test/internal/*`, so the basic checks
run them by package and `test/` holds only the 83 cluster tests (14f-2 converts those to Ginkgo).

**Architecture:** Go packages under the existing module `github.com/duongle-steven/homelab/test`: `test/offline`
(package `offline`) for render, script and check tests; `test/internal/<pkg>` for helpers both sides use, each with
its own offline tests beside it; the root package `test` keeps the cluster tests and imports `test/internal/*`.
`lab test scope` learns that a changed `test/internal/<pkg>` file selects the cluster tests importing that package.

**Tech Stack:** Go 1.26, testify, `go/parser` (in `cli/internal/scope`).

**Spec:** `docs/superpowers/specs/2026-11-08-test-layout-design.md` (step 1, "14f-1")

## Global Constraints

- No test changes what it asserts; moved tests keep their names (offline names stay `Test…`, so the lists compare).
- `scripts/ci-check go` runs `go vet ./... && go test -count=1 -timeout 20m ./offline/... ./internal/...` from
  `test/`: no list of test names anywhere.
- `test/` (root package) holds only tests that need a cluster; `test/kube-bench-cfg/` stays (the scan tests read it).
- Fixtures an offline test reads move with it (`test/fixtures/` → `test/offline/fixtures/`); repo-relative paths come
  from one helper (`repo.Root(t)`), never a count of `../`.
- Main checkout only for `just dev`; `ci-check` with the sandbox and testbed down.

## Review Focus

- A test that changes side silently (a cluster test moved to `offline/`, or an offline test left in the root and
  dropped from the basic checks): Task 2 compares the before/after name lists exactly.
- A test reading a path relative to its own directory (`../apps`, `fixtures/…`) after moving one level deeper:
  `repo.Root(t)` everywhere, and Task 2's full offline run proves each.
- A helper change no longer selecting the cluster tests that use it (it moved to `test/internal`): Task 1's scope
  tests.
- `KialiRender` in `test/scopes.yaml` matching no cluster test once the render test leaves the root: Task 1 removes
  the term, `just cli check` proves it.
- `go vet ./...` no longer compiling the cluster tests: it stays `./...` (root included).

---

### Task 1: scope knows `test/internal` and `test/offline`

**Files:**
- Modify: `cli/internal/scope/tests.go`, `cli/internal/scope/scope.go` (`isTestFile`)
- Test: `cli/internal/scope/tests_test.go` (or the file holding `FileTests`' tests)
- Modify: `test/scopes.yaml`

**Interfaces:**
- Produces: `FileTests(root)` selects, for a changed `test/internal/<pkg>/<f>.go` (not `_test.go`), the Test functions
  of every `test/*_test.go` that imports `github.com/duongle-steven/homelab/test/internal/<pkg>`, directly or through
  another `test/internal` package. `isTestFile` matches `test/*_test.go` and `test/internal/**/*.go` but not
  `test/internal/**/*_test.go`.

- [ ] **Step 1: Write the failing tests** in `cli/internal/scope`, on a temp repo:
  - `TestFileTestsInternalPackage`: `test/internal/env/env.go` changed; `test/a_test.go` imports
    `…/test/internal/env` and defines `TestA`; `test/b_test.go` does not import it (`TestB`) → `[TestA]`.
  - `TestFileTestsInternalTransitive`: `test/internal/kube` imports `test/internal/env`; `test/c_test.go` imports only
    `…/internal/kube` (`TestC`); changing `test/internal/env/env.go` → `[TestC]`.
  - `TestSelectOfflineSkipped`: with the repo's `test/scopes.yaml`, a change to `test/offline/x_test.go` or
    `test/internal/env/env_test.go` selects nothing (skip).
- [ ] **Step 2:** `go -C cli test ./internal/scope/` → the new tests FAIL.
- [ ] **Step 3:** implement: parse imports with `go/parser` (`parser.ImportsOnly` is enough for the import walk);
  `test/scopes.yaml` skip: replace the listed offline test files and `test/fixtures/**` with `test/offline/**` and
  `test/internal/**/*_test.go`; drop the term `KialiRender` from the Kiali group (`tests: SSOKiali`).
- [ ] **Step 4:** `go -C cli test ./internal/scope/` PASS; `direnv exec . just cli check` PASS.
- [ ] **Step 5: Commit** `feat: lab test scope follows test/internal packages; offline tests skipped by directory`.

### Task 2: move the offline tests

**Files:**
- Create: `test/offline/*_test.go` (package `offline`), `test/internal/repo/repo.go` (+ `_test.go`),
  `test/internal/env/env.go` (+ `_test.go`), further `test/internal/<pkg>` where a helper is shared (named by what it
  holds: e.g. `identity` for TOTP/PKCE, `gateway` for the node address from `docker inspect`)
- Move: `test/fixtures/` → `test/offline/fixtures/`
- Modify: every `test/*_test.go` that loses or shares a helper; `scripts/ci-check`

**Interfaces:**
- Consumes: Task 1 (a helper moved to `test/internal` still selects its cluster tests).
- Produces: `repo.Root(t testing.TB) string` (the repository root, from `git rev-parse --show-toplevel`);
  `env.Name() string` (the suite's `ENV`), `env.Dir(t testing.TB) string` (`infra/<env>`, refusing production as
  `envDirs` does), `env.Domain(t testing.TB) string` (today's `envDomain`/`testDomain`); 14f-2 builds on these.

- [ ] **Step 1: Record the lists before anything moves** (from `test/`):
  `go test -list . . | grep ^Test | grep -E "$offline_tests|TestRookCephAES256K" | sort > /tmp/…/offline-before`
  (131 lines) and the rest `> cluster-before` (83 lines), with `$offline_tests` taken from today's `ci-check`.
- [ ] **Step 2: Move.** Rules: a test in `offline-before` moves to `test/offline/` unless it tests a shared helper,
  in which case it moves beside that helper in `test/internal/<pkg>/`; a helper used only by offline tests moves to
  `test/offline/`; one used by both moves to `test/internal/<pkg>` (exported); one used only by cluster tests stays.
  Mixed files (`identity_test.go`, `gateway_test.go`, `scan_test.go`, `public_scan_test.go`, `security_logs_test.go`,
  `node_restart_test.go`, `monitoring_test.go`, `kiali_test.go`, `policy_test.go`, `smoke_test.go`,
  `supply_chain_test.go`, `trivy_operator_test.go`, `ntfy_test.go`) split by test. Paths through `repo.Root(t)`.
- [ ] **Step 3: `scripts/ci-check`:** delete `offline_tests`; the `go` step runs
  `go vet ./... && go test -count=1 -timeout 20m ./offline/... ./internal/...` (keep the comment on `-count=1` and
  the timeout).
- [ ] **Step 4: Compare the lists** (from `test/`):
  `go test -list . ./offline/... ./internal/... | grep ^Test | sort` equals `offline-before`, and
  `go test -list . . | grep ^Test | sort` equals `cluster-before`; `diff` prints nothing for both.
- [ ] **Step 5:** `direnv exec . scripts/ci-check go` PASS, and `go -C cli run ./cmd/lab test scope --full` still
  prints the same main regex as on `dev`.
- [ ] **Step 6: Commit** `refactor: offline tests in test/offline and test/internal, run by package`.

### Task 3: references, proof, PR

**Files:** `CLAUDE.md` (the basic checks' description: "the offline tests in `test/`"), `docs/how-to-guides/ci.md`,
`docs/how-to-guides/sandbox-tests.md`, any doc or comment naming `ci-check`'s regex or `test/fixtures`.

- [ ] **Step 1:** `grep -rn 'offline_tests\|test/fixtures\|offline tests in .test/' --exclude-dir=superpowers` →
  only updated prose; CLAUDE.md says the offline tests are `test/offline` and `test/internal`, the cluster tests
  `test/`.
- [ ] **Step 2:** `direnv exec . scripts/ci-check all` PASS (sandbox and testbed down). Commit
  `docs: the offline tests' packages`.
- [ ] **Step 3:** `just dev --full` on the sandbox (the cluster tests' helpers moved): every test passes.
- [ ] **Step 4:** Final review (opus), fixes, PR into `dev` with the review rulings and the proof (both list diffs
  empty, the full run).
