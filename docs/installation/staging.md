# Staging (optional)

A staging is an environment on its own hardware, run like production. It proves everything as production runs it, which
the [testbed](testbed.md) cannot, and whoever has one needs no testbed. Nobody has built one yet: **this page is a guide, not a
record**, and nothing in it has run.

## What it takes

1. Hardware in `infra/staging/metal/hosts.json` (name, MAC, address), with its own SSH key
   (`infra/staging/metal/authorized_keys`)
2. Its own domain and addresses in `infra/staging/cluster/config.json`
3. The same units as production under `infra/staging`
4. `values-staging.yaml` next to a component's `values.yaml`, only where it must differ. A staging never loads
   `values-local.yaml`, which means "runs on the workstation"
5. `"release_proof": "staging"` in `infra/production/cluster/config.json`

## What is built

`lab infra forgejo verify` reads the environment `release_proof` names, and the commit status of that name is its mark. For
`staging` it reads `infra/staging/release-paths.yaml` and `values-staging.yaml`; with an empty exclusion list every release
needs the mark. This gate is unit-tested, never run.

## What you still write

- A target that deploys staging: `just infra deploy` runs `verify`, `sync` and `push` only for production
- `lab`'s environments: `env.Load` refuses `--env staging` today
- Its state's backend: `infra/root.hcl` gives only production R2
- A step that writes the mark when its run passes (`scripts/mark-proof staging <commit>`)
- A test suite. The suite's fixtures (Kanidm's `smoke` client, `dev-admin`, the local CA) live in `values-local.yaml`, so
  most sandbox tests would not run there: a staging needs its own, less intrusive suite the first time one exists
