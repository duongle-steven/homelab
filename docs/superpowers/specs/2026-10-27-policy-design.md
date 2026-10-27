# Step 15b: policy (Kyverno and Pod Security Admission)

Status: designed in chat 2026-10-27, after a spike. Branch: `feat/policy` from `dev`. Decisions marked *(user)* were
agreed in chat. The rest are *(Claude's call)*.

## Goal

Workloads that break the Pod Security Standards, run unpinned images, or pull from an unknown registry are refused.
The refusal happens in the pull request (CI) and again at admission (the cluster). The few that must break a rule do
so through a written exception with its reason.

## The spike *(2026-10-27)*

The official policies (`kyverno-policies` 3.9.2, Pod Security restricted, Audit) were applied with the `kyverno` CLI
1.19.2 to the workload renders of every chart and app file (36 files).

- **Baseline: 14 failures, all inherent.**
  - istio-cni and ztunnel: capabilities, hostPath, AppArmor (the mesh);
  - tailscale and wireguard: `NET_ADMIN` (VPN routers);
  - node-exporter, kured and Alloy: host namespaces, hostPath, privileged (they read or reboot the node);
  - csi-hostpath (sandbox only): privileged, hostPath.
- **Restricted: 41 workloads**, mostly missing declarations: `allowPrivilegeEscalation: false` (40),
  `seccompProfile: RuntimeDefault` (39), `runAsNonRoot` (33). Some images can only run as root.
- **Errors:** `woodpecker-agent`'s check failed with "no such overload" until user namespaces are allowed
  (`podSecurityUserNamespaces`; its step pods run with `hostUsers: false`).

## Decisions

1. *(user)* **Kyverno 1.19.2** (chart 3.9.2) in `system/kyverno`, in the sandbox too, with three policy sets:
   - the Pod Security Standards at **restricted** (the `kyverno-policies` chart 3.9.2, `ValidatingPolicy`);
   - **pinned images**: a tag that is not `latest`, or a digest;
   - **allowed registries**: the list of registries the renders use today, plus zot (`registry.<domain>`).
2. *(user)* **Two phases in step 15b.**
   - **15b-1 (this branch):**
     - fix every violation that values can fix, by setting each workload's securityContext;
     - write a `PolicyException` for each inherent one, with its reason: the baseline list above, plus images that
       must run as root;
     - the policies run in **Audit** in the cluster, and **block in CI**: a new basic check `policy` runs
       `kyverno apply` with the policies and exceptions on the renders.
   - **15b-2 (later):** once the sandbox's PolicyReports show no failure, switch to **Enforce**.
3. *(user)* **Pod Security Admission** as a second layer, in case Kyverno is down: every managed namespace gets
   `pod-security.kubernetes.io/enforce: baseline`. A namespace with an inherent baseline exception gets `privileged`
   instead, through the same mechanism that labels namespaces for the mesh. `kube-system` is not managed.
4. *(Claude's call)* **Kyverno in the mesh.** The API server calls its admission controller, so its pods carry
   `istio.io/dataplane-mode: none`, and its chart's NetworkPolicies, if any, let 15008 in. The webhooks are
   `failurePolicy: Ignore` while in Audit; Enforce in 15b-2 reconsiders that. Its CRDs follow the same SkipDryRun
   rules as the other operators, on a fresh cluster.
5. *(Claude's call)* **Exceptions live with the component they excuse:** a `templates/policy-exception.yaml` in that
   chart, or a raw resource in its app file. Each names the policies and the workload, and carries a
   `policies.kyverno.io/description`-style reason. The `policy` check loads them with the policies.
6. *(Claude's call)* **The `policy` check** (`scripts/check-policy`):
   - render the policies (`system/kyverno`), the exceptions and the workloads of every chart and app file, both
     environments;
   - keep workload kinds only, since the CLI's fake cluster refuses duplicate names of other kinds;
   - fail on any `fail` or `error` result.

   Policies change rarely and a full run takes about a minute, so everything is checked, not only what the change
   touches. The `kyverno` CLI comes from `unstable` (1.19.2); nixos-26.05 ships 1.18.2.
7. *(Claude's call)* **Tests.**
   - Offline `TestCheckPolicy`: a fixture workload without securityContext fails the check, and one with it passes.
   - A sandbox test `TestPolicy`: Kyverno is up, a PolicyReport for a workload exists, and a pod that breaks
     restricted is reported (Audit) but still admitted. PSA's `baseline` refuses a privileged pod in a namespace
     labelled baseline.

## Not in this step

- Enforce mode (15b-2), image signatures (15e), and runtime detection (15g).
- Kyverno's mutation or generation policies.
