# Step 15h: a seccomp profile for Buildah, and `seccomp-default` on

## Why

kube-bench's 4.2.14 asks for the kubelet's `seccomp-default`: a pod that names no seccomp profile then gets `RuntimeDefault` instead of none. Step 15d left it off (`docs/how-to-guides/cluster-scan.md`, deferred minor 15d → 15h). With the flag on, `TestWoodpecker` failed: Woodpecker's step pods name no profile, and `RuntimeDefault` refuses the `unshare` Buildah's chroot isolation needs to build an image.

Every workload of ours already sets `RuntimeDefault` itself (policy, 15b-1). Two kinds of pod still run with no seccomp filter:

- Woodpecker's step pods, which run whatever a pipeline author writes.
- Pods that nothing of ours renders: k3s's own pods in `kube-system`, and Istio's generated Gateways (deferred minor 15b).

The step pods matter most: they run code from people other than the admin.

## Goals

- `seccomp-default` is on in the kubelet of every environment: the nodes (`infra/_modules/metal`) and the sandbox's k3d node.
- Buildah steps keep working. They get a `Localhost` profile (`homelab/buildah.json`), which is `RuntimeDefault` plus only the syscalls Buildah's chroot isolation needs inside a user namespace.
- Every other step pod gets `RuntimeDefault`, as all other pods do.
- A pipeline author does nothing. A pipeline copied from `docs/how-to-guides/woodpecker.md` builds unchanged, with no `backend_options` line.

## Non-goals

- The Security Profiles Operator. It would be one more component running all the time just to place one file on the nodes.
- Profiles for other workloads, or profiles tighter than `RuntimeDefault`.
- Buildah's other isolation modes (`oci`, `rootless`). Only `--isolation chroot` with `vfs` is supported, as today.
- The Gateways' own profile. That stays deferred minor 15b, for 15b-2; `seccomp-default` covers it at runtime anyway.

## Anti-goals

- A failure must never widen access. If the mutation does not happen (Kyverno down: its webhooks are `failurePolicy: Ignore`), the step pod gets `RuntimeDefault` and the build fails. It must never run unconfined.
- A pipeline cannot choose `Unconfined` itself. Pod Security `baseline`, enforced on namespace `woodpecker`, refuses it. Check this; do not assume it.
- A pipeline cannot give the Buildah profile to another image either. Woodpecker lets a step set `backend_options.kubernetes.securityContext.seccompProfile`, `Localhost` included, and no `*_ALLOW_FROM_STEP` setting turns that off. Without a guard, once the file is on every node, any step could ask for it.
- No syscall goes into the profile because it "might" be needed. Each addition is one that Buildah was seen to need.

## Decided

**1. The flag.**
- `--kubelet-arg=seccomp-default=true` goes in `infra/_modules/k3d/k3d.yaml` and in `infra/_modules/metal/flake/profiles/k3s-server.nix`.
- The comments saying "not seccomp-default" are replaced.
- `TestScanHardeningFlags` asserts the flag is present, where it now asserts it is absent.

**2. The profile.** A JSON file in the repository: `infra/_modules/metal/flake/seccomp/buildah.json`.
- It is shipped the way `audit-policy.yaml` is: one file for the nodes and k3d.
- On the nodes, NixOS places it under the kubelet's seccomp root as `homelab/buildah.json`, with tmpfiles or similar, so pods reference `localhostProfile: homelab/buildah.json`.
- On k3d, `infra/_modules/k3d/main.tf` mounts it at the same path in the server node, as it mounts the audit policy. Changing the mount rebuilds the sandbox's cluster: one `just dev-fresh`.
- Its base is the `RuntimeDefault` profile of the containerd embedded in k3s v1.37.1. The sandbox (`rancher/k3s:v1.37.1-k3s1`) and the nodes (nixpkgs' `k3s_1_37`) run the same k3s minor, and so the same containerd; check the patch versions match. The file records the k3s and containerd versions it came from.
- The additions are found by running `buildah build --isolation chroot` (the `TestWoodpecker` pipeline) under the base profile and adding each refused syscall, or argument condition, until the build passes. The spec and the profile's commit list each addition with the step that needed it.
- Expected additions are the namespace and mount calls that `RuntimeDefault` allows only with `CAP_SYS_ADMIN`, such as `unshare`, `mount`, `umount2`, `pivot_root` and `setns`. The measurement decides.

**3. The mutation.** A Kyverno mutating policy, in `system/kyverno` beside the existing policies or in `platform/woodpecker`. The builder picks; prefer Kyverno 1.19's CEL `MutatingPolicy` if it mutates on admission without the background controller (still off).
- It matches pods in namespace `woodpecker` that Woodpecker's agent creates for steps (by the labels the Kubernetes backend sets; read them off a live step pod), with a container whose image repository is `quay.io/buildah/stable`.
- It sets the pod-level `seccompProfile` to `{type: Localhost, localhostProfile: homelab/buildah.json}`.
- It does not override a profile the pod or container already sets: a step whose author sets `RuntimeDefault` keeps it.
- The image match is accepted as is. Anyone who picks the Buildah image gets the profile, whatever they run with it. The profile only adds namespace syscalls, in a pod already in its own user namespace (`WOODPECKER_BACKEND_K8S_USER_NAMESPACES`), unprivileged. Today the same pod runs with no filter at all.
- `scripts/ci-check policy` still passes. If it runs the Kyverno CLI on renders, the mutation gets a CLI test there too.

**4. The guard (my call, from the builder check).** A native `ValidatingAdmissionPolicy` with its binding, enforced now (`validationActions: [Deny]`), not a Kyverno rule:
- It applies to pods in namespace `woodpecker`.
- It refuses a pod or container that names `localhostProfile: homelab/buildah.json` unless every container and init container in the pod uses an image from repository `quay.io/buildah/stable`.
- It refuses any other `Localhost` profile there too: only this one exists.
- It is in-tree, so the API server evaluates it itself (`failurePolicy: Fail`, no webhook). It holds while Kyverno is down and does not wait for 15b-2's Enforce. Kyverno stays in Audit for everything else.
- If the mutation and the guard disagree about what a Buildah step pod is, the step fails. Test the agent's real step pod, including the clone step and any init containers the backend adds.
- Its home is beside the mutation.

**5. Falco (15g).** Buildah's `unshare` under the new profile must not add Critical or Error events to the sandbox's clean baseline. If it does, accept it in `accepted`, with the reason, narrowed like the memfd entry (namespace, image, process).

## Proofs

- **Offline:**
  - `TestScanHardeningFlags`: the flag is on in both files.
  - The profile is valid JSON, its default action is an error action, and it lists the base's syscalls plus the documented additions.
  - The policy matches a Buildah step pod and leaves a non-Buildah step pod, and a pod that sets its own profile, unchanged (a Kyverno CLI test or a rendered fixture).
  - The guard's CEL is checked against the same fixtures, if a tool can evaluate it offline. Otherwise the sandbox spec proves it.
- **Sandbox (Ginkgo, `test/e2e`):**
  - `Woodpecker` builds and pushes as today, with `seccomp-default` on.
  - A new spec shows the other side:
    - a non-Buildah step, or a pod shaped like one in namespace `woodpecker`, cannot `unshare` under `RuntimeDefault`;
    - the Buildah step pod's spec carries the `Localhost` profile;
    - a pod asking for `Unconfined` in `woodpecker` is refused by Pod Security;
    - a non-Buildah pod asking for `homelab/buildah.json` in `woodpecker` is refused by the guard.
  - Labels and `test/scopes.yaml`: a change to the profile, the k3d mount, or the policy runs the Woodpecker specs.
- **Full suite** (`just dev-fresh`): every pod without its own profile now has `RuntimeDefault`. That includes k3s's `kube-system` pods, the Gateways and every Woodpecker step. Nothing else may break.
- **Testbed, once by hand:**
  - The profile file is at its path on every node.
  - The kubelet runs with the flag.
  - A pod with the `Localhost` profile can `unshare -U` while a `RuntimeDefault` one cannot.
  - A plain Buildah pod under the profile builds a small image with `--isolation chroot` and `vfs` (my call, from the builder check). Production's newer kernel (7.0 or later) may take other syscall paths than WSL's 6.18, such as `clone3` or the new mount API. Any syscall it needs there is added, and recorded as "needed on NixOS's kernel".
  - Woodpecker itself does not run on the testbed.

## Docs

- `docs/how-to-guides/cluster-scan.md`: 4.2.14 is fixed.
- `docs/how-to-guides/woodpecker.md`: Buildah steps get the profile by themselves, and other images cannot `unshare`.
- CLAUDE.md: roadmap 15h done, and the 15d text that says "not `seccomp-default`".
- `deferred-minors.md`: remove the 15d row, and add this PR's own rows.
- The production list in CLAUDE.md: a containerd bump on the nodes can change `RuntimeDefault`. The profile was derived from one version, so review it when k3s's containerd changes its default profile.

## Left to the builder

- Where the policy lives and its exact kind.
- The tmpfiles mechanism on NixOS.
- The precise label selector for step pods.
- How the syscall additions are measured: audit-mode profile (`SCMP_ACT_LOG`), strace, or trial and error.
