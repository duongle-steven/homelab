# The testbed fits the workstation again

Status: designed in chat 2026-11-25. *(user)* marks what was decided in chat; *(Claude's call)* marks the builder's
own choices, open to review. It unblocks G2's testbed proof (`docs/superpowers/specs/2026-11-24-node-ports-design.md`).

## Intent

Since 2026-11-24 the testbed cannot settle: k3s on every node loses its leader lease and restarts, and each restart's
resync makes the next one likelier. Layers 1 and 2 used to hold on the same three 3.5 GB, 2-vCPU VMs (12a-2:
no k3s exit in 30 minutes, 62–82% idle on each node), so this is a regression, not the workstation's limit. The testbed
must stay usable for good, for what the one-node sandbox cannot prove *(user)*. The workstation cannot grow: 16 GB, with
WSL capped at 13 GB because Windows needs the rest *(user)*.

## What the measurements showed (2026-11-25)

The scenario was the same every time. The VMs started off, were started 90 s apart, and were sampled for 36 minutes:
the host and the VMs every 30 s, and CPU and memory per namespace every 2 minutes. The data is in the session's
`measure/` notes, summarised here:

| Run | k3s restarts in 36 min (three nodes) | CPU pressure in the VMs | WSL swap in / out |
|---|---|---|---|
| 2 vCPUs, 3.5 GB | 9 | 60–95% | ~4 / 4 MB/s |
| 4 vCPUs, 3.5 GB | 8 | 50–77% | ~3 / 3 MB/s (WSL's idle 20–36%) |
| 4 vCPUs, 3.5 GB, guests' `vm.swappiness` 180 | 8 | 45–80% | unchanged |
| 4 vCPUs, 3 GB | 11 | 55–83% | ~2 / 2 MB/s, 3.4 GB available |
| **4 vCPUs, 3 GB, 138 of 205 CRDs removed at minute 12** | **2, none after minute 25** | **35–50%** | ~1 / 1 MB/s |

Other findings from these runs:

- Every k3s exit was `leaderelection lost`, after etcd timeouts.
- etcd's peer round trip between VMs on the same host was mostly over 100 ms, and often over 0.8 s. fsync was mostly
  under 8 ms, so the disk was not the cause.
- KSM merged nothing: 5.5 GB of the pages were volatile.
- The cause is the CRDs. Since 14h (`system/crds`, 2026-11-20), the bootstrap applies every component's CRDs to every
  environment. The testbed runs four components, yet held 205 CRDs.
  - Every k3s server keeps and serves all of them.
  - Every controller and informer lists them again after a restart.
  - With all 205 in place, k3s took 100–175% of a CPU per node, against 60–100% without them.
- Production runs every component, so its list holds every vendored CRD (184 at 2026-11-25): the gain is the
  testbed's (50), and production's list only guards a future environment that runs less.
- Ceph is the largest remaining consumer, at 2–3.5 cores across the three nodes once stable. It does not break the bar.

## Decisions

- **Each environment applies only the CRDs it needs** *(user, 2026-11-25)*. These are the CRDs of the components it
  runs, plus the CRD of any kind its rendered manifests use. The first part is what the testbed showed, and the second
  covers a chart that renders, for example, a `ServiceMonitor` where no Prometheus runs. 14h's guarantee stays:
  - the CRDs are in place before Argo CD's ApplicationSets;
  - they are never pruned;
  - they are applied before Argo CD sees a commit;
  - no Application waits on another's CRD.
- **The testbed's VMs: 3 GB and 4 vCPUs** *(user)*.
  - 3 GB gives WSL back 1.5 GB and halves its swap traffic.
  - 4 vCPUs is the user's call: it uses the workstation's spare threads, which WSL had left at 20–36% idle. The 4-vCPU
    commit moves here from G2's branch.
  - 2.5 GB is not tried: the VMs hold 1.5–2 GB of anonymous memory, and too tight a node fails for reasons production
    never meets *(user)*.
- **No separate measurement afterwards** *(user)*. The last run already combined all three changes. G2's `just testbed`
  run is the confirmation, with the sampler running alongside, against the testbed's bar.

## Goals

1. **An environment's CRDs come from a list made offline.** `scripts/vendor-crds` (or a sibling) writes it, and the
   basic check `crds` keeps it current.
   - The list holds the CRDs of the components the environment's root and apps ApplicationSets deploy, plus the CRDs of
     the kinds found in its renders.
   - It is computed from the same directory lists Argo CD reads: `values.yaml` for production, `values-testbed.yaml` for
     the testbed.
   - The check fails when an environment renders a kind whose CRD it would not apply.
2. **`infra/_modules/argocd/apply-crds.sh` takes the environment** and applies only that environment's list.
   - Its callers pass the environment: the bootstrap, `just dev`, `just testbed`, and `just infra deploy production`.
   - It never deletes a CRD it no longer applies. Removing one stays a manual step, as now.
3. **The sandbox keeps every CRD**: it has no list, and `apply-crds.sh` applies every CRD only for `dev`, refusing any
   other environment without a list, so nothing silently differs. The reason is accumulation,
   not resources.
   - The sandbox lives across many `just dev` runs, and each run turns a different set of deploy groups on.
   - CRDs are never deleted (14h's `Prune=false`), so a per-group list would converge to almost all of them within a
     few runs.
   - `just dev-fresh` runs every group at once.
   - Choosing per group would also make `apply-crds.sh` work out the coming run's groups before the push, for little
     gain.
   - No spec needs a CRD whose kind does not run there. The kinds that core components render for other groups (for
     example `istio-system`'s `PodMonitor` without the `metrics` group) fall under the same rule as everywhere else.
4. **The testbed's VMs default to 3 GB and 4 vCPUs** (`infra/_modules/testbed-vms`), with the reasons beside the
   values. The offline test that pins them follows.
5. **`docs/installation/testbed.md`** records the measurements:
   - the table above;
   - the rule "a CRD the testbed does not run is not applied there";
   - how to measure again (the scenario).
6. **Proven:**
   - offline: the per-environment lists, the check failing on a missing CRD, and the scripts' arguments;
   - on the testbed: G2's `just testbed` run meets the bar (30 minutes with no k3s restart, no etcd leader change, no
     API or lease error) with the sampler's numbers recorded.

## Non-goals

- Ceph's CPU, which is noted for later and does not break the bar.
- Changing what the testbed runs (its layers).
- A Ceph-free testbed profile, which is not needed now that it holds.
- Trimming the sandbox's CRDs, for the reasons in Goal 3.

## Anti-goals

- An environment missing a CRD it renders. The basic check must catch it before a sync fails.
- Pruning: a CRD that leaves the list stays in the cluster, as 14h's `Prune=false` keeps it, until someone removes it on
  purpose.

## Approaches

- **Chosen: a per-environment list generated offline.**
- **Each component installs its own CRDs again** *(set aside)*. That brings back 14h's ordering problems, which
  `SkipDryRunOnMissingResource` once papered over.
- **Apply everything, but tune k3s or etcd to bear it** *(set aside)*. That treats the symptom, and production would
  carry the same unused load.
- **Trim by hand on each cluster** *(set aside)*. It is not reproducible, and the next bootstrap undoes it.

## Left to the builder

- Where the list lives (one file per environment, or computed on the fly in `apply-crds.sh` from the values files), and
  the script that writes it.
- How the renders are scanned for kinds: `lab test charts` already renders every component and app per environment.
- How a component's directory maps to its CRD files, given that `system/crds/files/<component>` is already per
  component.
