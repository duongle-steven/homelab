# Step 12a-2: testbed, optional staging, and what a release must prove

Status: designed in chat 2026-11-02, approved by the user. It replaces `docs/superpowers/specs/2026-11-01-staging-platform-design.md`,
whose staging ran out of CPU on the workstation before the core was up (measurements below). Decisions marked *(user)*
were made in chat; *(Claude's call)* marks the builder's own, open to review.

## Intent

"Staging" named one thing that had to be like production and also fit inside WSL. It cannot be both. Three environments,
each with one job *(user)*:

| Environment | Runs on | Proves | Required |
|---|---|---|---|
| `sandbox` | k3d, one node, the workstation | apps and the platform, accepting differences from production; the everyday dev and test environment | yes |
| `testbed` (`staging` in 12a-1) | VMs, several nodes, the workstation | what needs several nodes: nodes, their upgrades, etcd, the VIP, Ceph, and more as far as the machine allows | one of these two *(user)*; its size depends on the machine |
| `staging` | its own hardware, like production | everything, as production runs it | one of these two *(user)*: whoever has the hardware turns it on |

Someone with a staging needs no testbed: staging proves all of it on real machines, and nothing in the staging path depends
on the testbed. A testbed stays useful to them only as a cheap place to break things (reinstalls, power cuts) *(discussed
2026-11-02)*.

The repository should work for someone else by changing configuration *(user)*: more resources run more in the sandbox
and the testbed, and hardware adds a staging. This workstation has no staging, and nothing may assume it does or that a
staging is local.

## Goals

- `testbed` proves the multi-node path, and grows by measurement until the machine says stop.
- Everything the sandbox and testbed share lives in `values-local.yaml`; a staging on hardware never loads it *(user)*.
- Argo CD reading `ops/homelab` on Forgejo, which happens at every release, is proven on the sandbox.
- A release states what proved it: production's configuration names the environment, and `lab infra forgejo verify` checks
  that environment's mark and says plainly how far it goes.
- Adding a staging later needs no change to the gate's code; it does need code to deploy it and write its mark, which its
  guide names *(user)*.

## Non-goals

- Building or testing a staging now: nobody here has the hardware. Its path is documented and marked unproven *(user:
  optional; Claude's call: no test suite for it yet)*.
- The whole switch of a cluster from the seed to Forgejo in any test: it happens once in a cluster's life, and a failure
  leaves the apps running and is fixed by running the bootstrap again. It stays on the before-production list with Forgejo
  on Ceph *(discussed 2026-11-02)*.
- Making the whole repository portable (see Portability).
- The power work (12a-3), which runs on the testbed afterwards.

## Anti-goals

- A testbed that crashes WSL or makes the workstation unusable while it runs.
- A `verify` message that lets someone think the testbed proved what only production-like hardware can.
- Calling anything "like production" that is not.

## The testbed

- `infra/staging` becomes `infra/testbed`, with its targets, scripts, tests and GitHub branch: `make testbed`,
  `testbed-fresh`, `testbed-clean`, `testbed-stop`, `scripts/testbed-*`, `TestTestbed*`, `ENV=testbed`,
  `values-testbed.yaml` *(user: a better name than sandbox 2; the name is Claude's)*.
- Three VMs on the workstation: 3.5 GB each, **2 vCPUs** *(discussed: the i5-12500H has 4 P-cores and 8 E-cores, so 16
  threads overstate it; 12a-1's 2 vCPUs ran the nodes and rook-ceph)*. Memory and vCPUs are configuration; raising vCPUs
  needs a measurement showing CPU is the limit.
- WSL at 13 GB with 4 GB of swap, as the workstation is now *(user)*.
- Keeps what the runs measured it needs: one install at a time, the installer reclaiming cache, raw disks with
  `cache=unsafe`, `slow_control_plane` (etcd heartbeat 500 ms, election 5 s, controller leases 60 s), `scripts/testbed-trust`
  waiting for SSH, `make testbed-clean` not asking the cluster, kernel 7.3 and AES256K keys.
- No image cache *(user: few deploys, and closer to a real deploy)*: nodes pull from the registries. Add one only if a
  measurement shows pulls dominate.
- Argo CD deploys it *(user)*, syncing GitHub branch `testbed`, which `make testbed` force-pushes, as the sandbox does.
  There is no Forgejo and no seed switch, so the testbed's seed machinery goes (`seeded`, `seed_environment`,
  `seed_revision`, `set-seed.sh`); the fix for a cluster without Argo CD's CRDs stays, since production's first install
  needs it. `make testbed` no longer applies rook-ceph by hand.

### What runs, by layer

Each layer must be stable and measured before the next joins *(the spec's rule the last run broke by starting with
almost everything)*. **A layer passes from a fresh install with every listed layer syncing** *(user)*, since that is how a
testbed is really rebuilt: each measurement is one `make testbed-fresh` plus the soak below. Sync ordering is added only if a
measurement needs it:

| Layer | Adds | Proves |
|---|---|---|
| 1 | Argo CD, rook-ceph | Ceph on three nodes through Argo CD; the base measurement, at 2 vCPUs |
| 2 | Istio with what it needs (cert-manager, and the CRDs of Kyverno and the monitoring operator, as little of them as works) | the ambient mesh and the Gateway's VIP on several nodes |
| 3 | CloudNativePG's three instances, failing over | what only production otherwise runs |

- **Stable** means, over 30 minutes after the last Application is healthy: no k3s restart, no etcd leader change, no
  API server or lease error (`leaderelection lost`, `request timed out`, `deadline exceeded`), and WSL with at least 2 GB
  available *(user, 2026-11-03)*. `leader failed to send out heartbeat` warnings are counted and recorded, not failed:
  layer 1 logged two (2.3 s and 35 ms over) with no leader change and no error, and the first followed a sign of the VM
  being stalled from outside. A delay close to the 5 s election timeout stops the run to ask.
- The list stops where the machine stops *(user: fit what fits)*, recorded with its numbers in `docs/installation/testbed.md`.
  Another machine extends the list in `system/argocd/values-testbed.yaml`, up to everything.
- A vCPU ceiling of 4 per VM *(user)*: a layer that fails at that stops the list at the layer before it, which is not a
  failure of 12a-2: the branch ships with the layers that fit.
- **Layer 1 is the floor** *(user)*: a testbed without Ceph through Argo CD has lost its purpose. If it fails the bar at 4 vCPUs,
  the work stops and asks; the bar is not relaxed and an empty branch does not ship.
- "Up to everything" is a direction, not a promise *(user)*: components past these layers need more than the list (OpenBao
  and its units, Forgejo, tests generalised for them). What is cheap and tested stays (the `ENV` helpers); the units and values for
  components the testbed does not deploy go. `docs/installation/testbed.md` says so.
- The testbed runs on a workstation, not in GitHub Actions *(user)*.

### Measured, 2026-11-01 (why it is built this way)

- Three parallel installs ran WSL out of memory; one at a time, WSL stayed above 5 GB.
- At 2 vCPUs with qcow2: etcd fsync averaged 27 ms, up to a second, a node reached a load of 31, and k3s exited.
- Raw and `cache=unsafe` brought fsync to 3–10 ms. The heartbeat and lease settings took k3s from 12 restarts in 35 minutes
  to 2 in 30.
- With most of the platform, at 4 vCPUs and WSL at 13 GB: about 1% CPU idle on every node, repo-server restarting on its
  1 s probe, rook-ceph unrendered after 48 minutes. Memory inside the guests was not the limit by then.
- Top processes: `k3s-server` about 1 GB on each node, the Argo CD controller up to 1 GB.

## Values

| Environment | Loads, in order |
|---|---|
| production, staging | `values.yaml`, then `values-staging.yaml` for a staging where it exists |
| testbed | `values.yaml`, `values-local.yaml`, `values-testbed.yaml` |
| sandbox | `values.yaml`, `values-local.yaml`, `values-dev.yaml` |

`values-local.yaml` means "runs on the workstation": the local CA and its trust, the tests' fixtures, the S3 stand-in, no
Telegram or Cloudflare, and the limits on Kyverno and the Trivy Operator *(user)*. Comments that say "staging and the
sandbox" change accordingly. A file that holds only what one environment needs stays in that environment's file.

## The deploy path, on the sandbox

- A test pushes to the sandbox's Forgejo (`lab infra forgejo sync|push`), creates a temporary Application reading
  `ops/homelab` at Forgejo's in-cluster address with the `argocd` user's read-only token, checks that it syncs, then removes
  it *(discussed 2026-11-02: this happens at every release; the whole switch does not)*.
- The allow from Argo CD's repo server to Forgejo, production-only and only rendered until now, is enabled in the sandbox
  so the test proves it.
- It runs when the deploy path changes and in fresh runs (`test/scopes.yaml`), not at every `make dev`.
- `lab infra forgejo --env` needs no testbed: what 12a-2 added for `--env staging` goes *(user)*, since it assumes the local CA.

## What a release must prove

- Production's `infra/production/cluster/config.json` names the proving environment: `"release_proof": "testbed"` or
  `"staging"` *(Claude's call, approved in outline)*. The key is required: without it `verify` refuses and says to set it, so a
  release never goes unproven by omission *(Claude's call)*. A commit status named after that environment is its mark, written
  when its run passes.
- **`testbed`:** the mark is needed when a release changes `infra/testbed/release-paths.yaml`'s paths or a component the
  testbed runs, derived from `values-testbed.yaml` as already built. `verify` always says: only the testbed proved this, and
  no production-like environment did.
- **`staging`:** the same gate reads `infra/staging/release-paths.yaml` and `values-staging.yaml`, and with an empty
  exclusion list every release needs the mark. Unit-tested, never run.
- A component switched off inside its chart counts as not run: `release-paths.yaml` gains a `not_run` list that `GatedPaths`
  subtracts *(the gap left open in the last run)*.
- `--skip-staging` becomes `--skip-proof=<reason>`, needing a reason and recording it as the release's status.
- What the mark needs: every test of `make testbed`, which are `TestTestbed*` and the existing tests for the layers it runs
  *(replaces "the whole suite with the destructive tests", which assumed a staging running the platform)*.

## Adding a staging

`docs/installation/staging.md` becomes that guide: hardware in `infra/staging/metal/hosts.json`, its own domain and
`config.json`, the same units as production, its own SSH key, `values-staging.yaml` only where it must differ,
`"release_proof": "staging"`. Nothing more is built now *(user)*: the gate reads it and is unit-tested, and the guide names what
that person must still write: a target that deploys staging (`make infra` runs `verify`, `sync` and `push` only for production)
and runs the mark. It says what is unproven, including that the suite's fixtures live in `values-local.yaml`, so
most sandbox tests would not run there and a staging needs its own less intrusive suite when one first exists *(discussed
2026-11-02)*.

## Portability

This step does what the design needs: VM size and each environment's app list in configuration. The rest becomes its own
roadmap step *(discussed 2026-11-02)*: the GitHub repository in `system/argocd/values-dev.yaml`, the domain in `apps/matrix`,
the time zone in `kured` and `renovate`, and the assumptions of WSL (`C:`, `.wslconfig`).

## Constraints

- `CLAUDE.md`'s rules: pinned latest versions, `config.json` for what depends on where it runs, the secrets catalog, backup
  labels, policy exceptions, mesh policies, `test/scopes.yaml`, deferred minors.
- The sandbox's renders stay identical apart from the deploy-path allow.
- Testbed and sandbox never run together. A run stopped for low memory is not restarted without asking: an operating rule
  for whoever runs it, with no watchdog in `make testbed` *(Claude's call)*.
- Live runs only from the main checkout.

## Left to the builder

- How the branch's existing commits are reworked, and whether the branch is renamed; 12a-2's tests for environments
  (`localOnly`, `testDomain`) stay if they still serve someone with a larger testbed.
- How Istio gets the CRDs it needs in layer 2, and whether Argo CD's limits on the testbed stay, from measurements.
- How CloudNativePG is exercised without Forgejo in layer 3.
- The exact `not_run` format and its check that every entry is a real path.
