# 14b-1: `just` replaces make

## Intent

The Makefiles hold commands, not builds: every target is `.PHONY`, nothing uses make's dependency graph, and the cost
is make's syntax: `$$` everywhere, `$(value …)` to keep a `$` in a regex, `override`/`export` lines to pass a reason
with quotes, `$(MAKE) var=…` to call another target, and options as `name=value` strings with no help or checking.
`just` is a command runner: recipes take real arguments (long options and flags since 1.46), run the system shell or a
shebang script with no escaping layer, list themselves (`just --list`), and `just --dry-run` prints without running
(make's `-n` runs `$(MAKE)` lines: it once applied the sandbox mid-test). Upstream moved to `just` on 2026-11-03.
go-task was weighed in chat (2026-11-07) and set aside: its `sources`/`generates` would go unused here, it runs
recipes in its own shell interpreter and writes shell inside YAML strings.

14b is two steps. This one (14b-1) swaps the runner and keeps every behaviour. 14b-2 (later) moves `lab`'s glue
commands into recipes or scripts.

## Goals

- A `justfile` at the root and one each in `infra/`, `cli/` and `test/` (`mod`), replacing the four Makefiles, which
  are deleted (no `make` shim).
- Arguments in `just`'s style: long options and flags instead of `name=value` (decided with the user on 2026-11-08).
- Same behaviour as today for every command: guards, ordering, exit statuses, what runs and what is refused.
- Every living reference moves in the same PR: CI (`.github/actions/sandbox`, the workflows' inputs), `scripts/`,
  messages in Go and scripts, the offline tests that drive make, `test/scopes.yaml`, `docs/` (except
  `docs/superpowers/specs` and `plans`, which are history), `README.md`, `CLAUDE.md`, `docs/superpowers/deferred-minors.md`.
- `just` pinned at its latest stable release, 1.59.0 (nixos-unstable's lock has 1.58.0): a `nix/just.nix` from the
  release binary, like `nix/kubescape.nix`.

## Non-goals

- No change to what `lab` does (14b-2), to the tests' content or to the sandbox, testbed or production.
- No new commands, no renamed concepts beyond what the interface below lists (`--env` replaces make's `env=`).
- Historical specs and plans keep saying `make`.

## Anti-goals

- A recipe that silently does less than its target did (a guard dropped, an exit status swallowed, a refresh skipped).
- Re-creating make's escaping problems in `just`: shell logic of more than a few lines goes in a shebang recipe
  (`#!/usr/bin/env bash` with `set -euo pipefail`) or a script in `scripts/`, never in long `\`-joined lines.

## Interface

| make | just |
|---|---|
| `make dev` | `just dev` |
| `make dev scope=full` | `just dev --full` |
| `make dev tests='<regex>'` | `just dev --tests '<regex>'` |
| `make dev apps=<names>\|all\|none` | `just dev --apps <names>\|all\|none` |
| `make dev revision=<sha>` | `just dev --revision <sha>` |
| `make dev base=<ref>` | `just dev --base <ref>` |
| `make dev-fresh [apps=…] [tests=…]`, `dev-clean`, `dev-scan` | `just dev-fresh [--apps …] [--tests …]`, `dev-clean`, `dev-scan` |
| `make testbed`, `testbed-fresh`, `testbed-clean`, `testbed-stop`, `testbed-scan` | same names |
| `make testbed-power [tests='A B']` | `just testbed-power [--tests 'A B']` |
| `make infra env=<env> [force=1] [break_glass=<r>] [skip_proof=<r>]` | `just infra deploy <env> [--force] [--break-glass <r>] [--skip-proof <r>]` |
| `make -C infra check` | `just infra check` |
| `make -C cli test`, `check` | `just cli test`, `just cli check` |
| `make test`, `make -C test filter=… format=…` | `just test run [--env <env>] [--filter <regex>] [--format <f>]` (or `just test --filter …` if `just` passes a module's default recipe its options; the builder checks) |
| `make docs` | `just docs` |
| `make` (no target: production's `infra` then the smoke tests) | `just` lists the recipes; the old default is dropped |

`infra` becomes a module (`infra/justfile`): a root recipe `infra` would clash with `mod infra`. `HOMELAB_GITHUB_APP_KEY`
keeps its path rules (absolute, `~/`, or relative to where `just` runs; spaces allowed), from the environment only: the
command-line assignment `make infra HOMELAB_GITHUB_APP_KEY=…` allowed goes (its test case too;
`docs/how-to-guides/renovate.md` says `HOMELAB_GITHUB_APP_KEY=… just infra deploy production`, the shell's own form).
(Builder check: my call.)

`just test run` keeps what `make test` did: `--env` defaults to `production` (make's `env ?= production`, so
`docs/installation/post-installation.md`'s check still reads production), and selects the kubeconfig
(`infra/<env>/kubeconfig.yaml`), `ENV` (`dev` for the sandbox, `testbed`, unset for production) and
`INSECURE_SKIP_VERIFY=1` (sandbox and testbed), as the root targets passed them; the filter defaults to `.`. The root
recipes call it with their environment and filter (the old `smoke-test` with `Smoke` is no longer a public command).
`dev-fresh` passes `--apps` and `--tests` on to `dev`, as `$(MAKE)` passed `apps=` and `tests=` (a `--tests` there
overrides `--full`, as before). (Builder check: my calls.)

The internal targets (`sandbox-checkout`, `sandbox-clean`, `sandbox-push`, `sandbox-refresh`, `smoke-test`) become
private recipes or plain dependencies.

## Constraints

- `just`'s recipes run in their justfile's directory; `just` finds the root justfile from any subdirectory.
- The main-checkout guard (never a git worktree) and the clean-tree and `--revision is HEAD` guards run before any
  sandbox or testbed work, as today.
- A reason (`--break-glass`, `--skip-proof`) and a regex (`--tests`) reach `lab` and `go test` unchanged, quotes and
  `$` included: pass them through the environment or `"$@"`, never through re-parsed strings.
- The alone tests keep running one `go test` each (#78), from `lab test scope --alone --each`.
- `testbed` records the commit it started from and refuses to mark another (`proof_commit`), and `infra deploy`
  verifies and pushes the commit it started from (`deploy_commit`).
- `scripts/ci-check` and GitHub Actions call `just` inside the dev shell (`nix develop --command just …`); the job and
  check names do not change (`sandbox` stays the required check).
- The offline tests in `test/ci_scripts_test.go` drive `sandbox-clean` and `infra deploy` with fake `lab` and
  `terragrunt`: the recipes must take those from overridable variables or the environment, and the tests move to
  `just`.

## Proof

- `scripts/ci-check all` (the offline tests drive the new recipes).
- `just dev --full` on the sandbox from the branch (the root justfile is in `test/scopes.yaml`'s run-everything group).
- `just --dry-run` of `testbed-clean`, `testbed-power`, `infra deploy production` and `dev-fresh`, read against the
  old targets; `just infra deploy sandbox` applied for real.
- `just testbed` once on the testbed (sandbox stopped, 30 GB free on `C:`): the testbed recipes are rewritten too.

## Left to the builder

- The exact `just` syntax (`[arg(…, long)]`, `value=` flags, `[private]`, `set` options, exported parameters).
- Whether long shell bodies become shebang recipes or move to `scripts/`.
- How `ci_scripts_test.go` overrides `lab` and `terragrunt`.
- The wording of messages that named make targets.
