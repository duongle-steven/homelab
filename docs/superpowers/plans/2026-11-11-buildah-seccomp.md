# Buildah seccomp profile Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn on the kubelet's `seccomp-default` everywhere. Woodpecker's Buildah steps keep building under a `Localhost` profile (`homelab/buildah.json`) that nothing else may use.

**Architecture:**
- **The profile:** a JSON file, containerd's `RuntimeDefault` plus measured additions. It ships to the nodes the way `audit-policy.yaml` does: NixOS tmpfiles on the nodes, a k3d `--volume` in the sandbox.
- **The mutation:** a native `MutatingAdmissionPolicy` puts the profile on Woodpecker's Buildah step pods.
- **The guard:** a native `ValidatingAdmissionPolicy` refuses that profile, and any other `Localhost` one, in namespace `woodpecker` for pods that are not all Buildah.
- Both policies are in-tree (`admissionregistration.k8s.io/v1`, served by k3s 1.37.1): no webhook, no Kyverno dependency.

**Tech Stack:** k3s 1.37.1 (containerd embedded), NixOS flake `infra/_modules/metal`, OpenTofu module `infra/_modules/k3d`, Helm wrapper chart `platform/woodpecker`, Go tests (`test/offline` with testify, `test/e2e` with Ginkgo).

**Spec:** `docs/superpowers/specs/2026-11-11-buildah-seccomp-design.md`

## Global Constraints

- The flag is exactly `--kubelet-arg=seccomp-default=true`, in `infra/_modules/k3d/k3d.yaml` (`nodeFilters: [server:*]`) and `infra/_modules/metal/flake/profiles/k3s-server.nix`.
- The profile's path is `<kubelet seccomp root>/homelab/buildah.json`, which is `/var/lib/kubelet/seccomp/homelab/buildah.json`. Pods reference `localhostProfile: homelab/buildah.json`.
- The source file is `infra/_modules/metal/flake/seccomp/buildah.json`, one file for the nodes and k3d.
- The profile's base is the `RuntimeDefault` of the containerd embedded in k3s v1.37.1. The file records the k3s and containerd versions it came from.
- No syscall is added unless Buildah was seen to need it. Each addition is listed with what needed it.
- Image match: repository `quay.io/buildah/stable`, any tag or digest.
- A failure never widens access. A missed mutation leaves `RuntimeDefault`, so the build fails.
- Pipelines need no `backend_options` line.
- Commits end with the session's `Co-Authored-By` and `Claude-Session` lines. Never commit to `dev` or `master`.

## Review Focus

1. **The guard must not refuse what the mutation produces.** Woodpecker's real Buildah step pod may carry more than the step's container: an init container, or a clone step's image. Task 4's sandbox spec runs the real pipeline, not a hand-made pod.
2. **A pod that sets its own profile.**
   - A step whose author sets `RuntimeDefault` keeps it: the mutation does not override it (offline fixture in Task 3, sandbox in Task 4).
   - A container-level `Localhost` profile on a non-Buildah pod is refused, not only a pod-level one (Task 3 fixture, Task 4 spec).
3. **Image references with a digest or a registry port**, such as `quay.io/buildah/stable@sha256:…` or `quay.io/buildah/stable:v1.43.5`, match. `quay.io/buildah/stable-evil` and `evil.io/quay.io/buildah/stable` do not. Task 3 covers each in its CEL tests.
4. **Pods outside `woodpecker`, and its server and agent pods, are untouched by both policies** (Task 3 fixtures, Task 5's full suite).
5. **A newer kernel taking other syscall paths** (`clone3`, the new mount API) on NixOS's kernel 7.0+. Task 6 builds on the testbed.

---

### Task 1: The profile, measured

**Files:**
- Create: `infra/_modules/metal/flake/seccomp/buildah.json`
- Create: `infra/_modules/metal/flake/seccomp/README.md`: where the base came from, the additions with what needed each, and how to regenerate it.
- Test: `test/offline/seccomp_test.go`

**Interfaces:**
- Produces: the file path above, and the profile name `homelab/buildah.json`. Tasks 2, 3, 4 and 6 use both.

- [ ] **Step 1: Find the base.**
  - Read containerd's version from k3s v1.37.1's `go.mod`, tag `v1.37.1+k3s1`.
  - Generate the JSON of `contrib/seccomp.DefaultProfile` for a spec with containerd's default capability set (no `CAP_SYS_ADMIN`). Use a throwaway `go run` in the scratchpad against that containerd module version.
  - Check that nixpkgs' `k3s_1_37` (the nodes' flake lock) is the same k3s patch. If not, ledger a ruling naming the version used.

- [ ] **Step 2: Measure the additions in the sandbox.**
  - Copy the base with `defaultAction: SCMP_ACT_LOG` into the running k3d node, by hand at `/var/lib/kubelet/seccomp/homelab/measure.json`.
  - Run a throwaway pod in namespace `woodpecker`:
    - image `quay.io/buildah/stable:v1.43.5`;
    - `hostUsers: false`, as Woodpecker's step pods have;
    - `localhostProfile: homelab/measure.json`;
    - command: `TestWoodpecker`'s build (`buildah --storage-driver vfs build --isolation chroot` of a two-line Containerfile).
  - Collect the logged syscalls: `dmesg`/kmsg `type=1326` lines with the pod's processes. If WSL's kernel does not log them, fall back to trial: base profile with `SCMP_ACT_ERRNO`, add each syscall the build fails on.
  - The additions are the logged syscalls that the base does not allow unconditionally.
  - Expected: a short list such as `unshare`, `mount`, `umount2`, `pivot_root`, `setns`, `clone` with namespace flags, possibly `clone3`. Delete the throwaway pod and file.

- [ ] **Step 3: Write the failing test `TestBuildahSeccompProfile`** in `test/offline/seccomp_test.go`. It parses the file and asserts:
  - `defaultAction` is `SCMP_ACT_ERRNO`;
  - every addition from the README's list is allowed (`SCMP_ACT_ALLOW`, with no `includes.caps` condition);
  - a fixed sample of the base stays refused: `bpf`, `kexec_load`, `init_module`, `ptrace`, `perf_event_open`, `keyctl`, `add_key`, `open_by_handle_at`;
  - `README.md` names the k3s and containerd versions.

  Run `go test ./test/offline -run TestBuildahSeccompProfile`. Expected: FAIL (file missing).

- [ ] **Step 4:** Write `buildah.json`: the base, with the additions as their own `syscalls` entries (provenance goes in the README: seccomp JSON has no comments).
  - Prove it works. Copy it to the node as `homelab/buildah.json`, run the Step 2 pod with it: the build passes. Run the same pod with `RuntimeDefault`: it fails on `unshare`.

- [ ] **Step 5:** Run the test. Expected: PASS. Commit `feat(seccomp): Buildah's Localhost profile, RuntimeDefault plus what its chroot isolation needs`.

### Task 2: The flag and the file on k3d and the nodes

**Files:**
- Modify: `infra/_modules/k3d/k3d.yaml`: the flag, and the comment that says "not seccomp-default".
- Modify: `infra/_modules/k3d/main.tf`:
  - a variable `seccomp_profiles` (the directory `infra/_modules/metal/flake/seccomp`);
  - `--volume "${var.seccomp_profiles}/buildah.json:/var/lib/kubelet/seccomp/homelab/buildah.json@server:*"`;
  - its `filesha1` in `triggers_replace`.
- Modify: `infra/sandbox/cluster/terragrunt.hcl`: pass it, like `audit_policy`.
- Modify: `infra/_modules/metal/flake/profiles/k3s-server.nix`:
  - the flag, and replace its "not seccomp-default" comment;
  - a tmpfiles rule `L+ /var/lib/kubelet/seccomp/homelab/buildah.json - - - - ${../seccomp/buildah.json}` with `d /var/lib/kubelet/seccomp/homelab 0755 root root -`.
  - The kubelet reads a Localhost profile at each container start, so no restart trigger is needed.
- Test: `test/offline/scan_test.go`

- [ ] **Step 1:** Change `TestScanHardeningFlags`:
  - Move `--kubelet-arg=seccomp-default=true` into the asserted list, and drop the two `NotContains`.
  - Add assertions that `k3s-server.nix` contains `/var/lib/kubelet/seccomp/homelab/buildah.json` and `../seccomp/buildah.json`, and that `main.tf` mounts `:/var/lib/kubelet/seccomp/homelab/buildah.json@server:*`.

  Run it. Expected: FAIL.
- [ ] **Step 2:** Make the changes above. Run `go test ./test/offline -run TestScanHardeningFlags`, `just infra check` and `nix flake check` (or the eval the flake's checks run). Expected: PASS.
- [ ] **Step 3:** Commit `feat(seccomp): seccomp-default on k3d and the nodes, Buildah's profile beside the kubelet`.

### Task 3: The mutation and the guard

**Files:**
- Create: `platform/woodpecker/templates/seccomp-policy.yaml`. It holds:
  - `MutatingAdmissionPolicy` `woodpecker-buildah-seccomp` with its binding;
  - `ValidatingAdmissionPolicy` `woodpecker-localhost-seccomp` with its binding (`validationActions: [Deny]`);
  - `failurePolicy: Fail` on both;
  - binding `matchResources.namespaceSelector` on `kubernetes.io/metadata.name: {{ .Release.Namespace }}`;
  - policy `matchConstraints` on pods `CREATE`.
- Test: `test/offline/woodpecker_seccomp_test.go`

**Interfaces:**
- Produces: policy names `woodpecker-buildah-seccomp` and `woodpecker-localhost-seccomp`. Task 4 reads them in failure messages.

**The rules, which the CEL must implement:**
- *Buildah pod:*
  - every container and init container image, after stripping a `@digest` and then a `:tag` after the last `/`, equals `quay.io/buildah/stable`;
  - the pod has label `woodpecker-ci.org/step` or whatever the step-pod label the Kubernetes backend sets. Read it off a live step pod (`TestWoodpecker`'s pipeline) and ledger it.
- *Mutation:* on a Buildah pod with no pod-level `seccompProfile` and no container-level one, set `spec.securityContext.seccompProfile` to `{type: Localhost, localhostProfile: homelab/buildah.json}` (`patchType: ApplyConfiguration`, `reinvocationPolicy: Never`).
- *Guard:* refuse a pod where the pod or any container (init included) has `seccompProfile.type == 'Localhost'`, unless it is a Buildah pod and every such profile is `homelab/buildah.json`.
  - Message: `"only Woodpecker's Buildah steps may use a Localhost seccomp profile (homelab/buildah.json)"`.
- Annotate both policies with why they are native, not Kyverno: in-tree, they hold while Kyverno is down. They need no `SkipDryRunOnMissingResource`, since the kinds are built in.

- [ ] **Step 1: Write the failing test `TestWoodpeckerSeccompPolicies`.**
  - It renders `platform/woodpecker` for both environments (`render.Yq`) and asserts both policies, both bindings, `failurePolicy: Fail`, `[Deny]` and the namespace selector.
  - It evaluates the two CEL rules over fixtures with `k8s.io/apiserver`'s CEL libraries or `github.com/google/cel-go` with the Kubernetes CEL environment. If neither is practical in `test/go.mod`, assert the expressions' presence here and leave behavior to Task 4. Ledger the ruling.
  - Fixtures:
    - (a) Buildah step pod, no profile → mutated, admitted;
    - (b) `alpine` step pod → unchanged, admitted;
    - (c) Buildah pod with `RuntimeDefault` → unchanged;
    - (d) `alpine` with pod-level `Localhost homelab/buildah.json` → denied;
    - (e) `alpine` with a container-level one → denied;
    - (f) Buildah with `Localhost other.json` → denied;
    - (g) a Buildah container plus an `alpine` init container → not mutated, and denied if it asks;
    - (h) image refs `quay.io/buildah/stable@sha256:…`, `quay.io/buildah/stable:v1.43.5` match; `quay.io/buildah/stable-evil`, `evil.io/quay.io/buildah/stable` do not.

  Run it. Expected: FAIL.
- [ ] **Step 2:** Write the template. Run the test, `scripts/ci-check render`, `scripts/ci-check policy` and `scripts/ci-check mesh`. Expected: PASS.
- [ ] **Step 3:** Commit `feat(woodpecker): native admission policies give Buildah steps their seccomp profile, and only them`.

### Task 4: The sandbox spec

**Files:**
- Create: `test/e2e/seccomp_test.go`
- Modify: `test/scopes.yaml`:
  - label `seccomp` in `labels:`;
  - `full` gains `|| seccomp`;
  - a group with paths `infra/_modules/metal/flake/seccomp/**`, `platform/woodpecker/templates/seccomp-policy.yaml` and `infra/_modules/k3d/**`, with tests `seccomp || woodpecker`. Merge into an existing group if the k3d paths already have one.
  - `TestE2EKeepsSelection` must still pass.

- [ ] **Step 1: Write `Describe("WoodpeckerSeccomp", Label("sandbox", "woodpecker", "seccomp"), …)`** with these Its:
  - **"puts Buildah's profile on its step pods":** run `TestWoodpecker`'s pipeline helper, or watch its step pods while `Woodpecker` runs if reusing it is simpler. The Buildah step pod's `spec.securityContext.seccompProfile` is `Localhost homelab/buildah.json`, and the pipeline succeeds.
  - **"refuses unshare to other images":** a pod in `woodpecker` with the step-pod label, image `quay.io/buildah/stable` replaced by the busybox the suite already uses, `hostUsers: false`, running `unshare -U true`. It terminates non-zero with `Operation not permitted`. Use the label, so it is shaped like a step.
  - **"refuses the profile to other images":** creating that pod with pod-level, then container-level, `Localhost homelab/buildah.json` fails with the guard's message.
  - **"refuses Unconfined":** the same pod with `Unconfined` fails with Pod Security's `baseline` message.
  - **"the node runs with seccomp-default":** a pod in `woodpecker` with no profile, as the API server stores it, still has none (so the mutation did not add one), and `grep Seccomp /proc/self/status` inside it reads `Seccomp: 2`.
- [ ] **Step 2: RED.** Commit the test and the scopes. Run `just dev --tests 'seccomp'` from `/home/nixos/homelab` (the sandbox is running). The sandbox's k3d still lacks the flag and the profile, since the cluster is rebuilt only by `just dev-fresh`, but it now has the policies. Expected: FAIL on the profile or the node's `Seccomp` line. Record which.
- [ ] **Step 3: GREEN.** `just dev-fresh` (Task 5) rebuilds k3d with the mount and the flag. The spec passes there.

### Task 5: The whole sandbox

- [ ] **Step 1:** Check C: has 30 GB free, and that the testbed is stopped. Run `just dev-fresh` from `/home/nixos/homelab` in the background, its output to a file.
  - Expected: every spec passes, `WoodpeckerSeccomp` and `Woodpecker` included.
  - A failure elsewhere is checked for a `RuntimeDefault` cause first: a pod in `kube-system` or a Gateway now under a filter. Fix the cause or ledger a ruling.
- [ ] **Step 2: Falco baseline.** In Loki, list Falco events of priority `warning` and up since the run started, by rule and namespace (the query `docs/how-to-guides/runtime-detection.md` gives).
  - Expected: only the spec's own events, plus what 15g's baseline already had.
  - A new Critical or Error from Buildah's `unshare` gets an entry in `system/falco/values.yaml`'s `accepted`, narrowed like `buildah-chroot-reexec` (namespace, image, process), with its reason. `TestFalcoAccepted` covers its shape.
- [ ] **Step 3:** Commit any fix, and rerun the specs it touches.

### Task 6: The testbed, by hand once

**Files:**
- Modify: `test/e2e/testbed_test.go`: `Describe("TestbedSeccomp", Label("testbed", "proof"), …)`.

**The spec:**
- On every node, over SSH as the other testbed specs do, `/var/lib/kubelet/seccomp/homelab/buildah.json` equals the repository's file.
- The k3s unit's command line has `seccomp-default=true`.
- A `hostUsers: false` busybox pod in a scratch namespace with no profile fails `unshare -U true`.
- A Buildah pod with the `Localhost` profile builds a two-line Containerfile with `--isolation chroot` and `vfs`.
  - The guard is only in `woodpecker`, which the testbed does not run, so the scratch namespace has no policy.
  - Label `proof`: it checks node settings, as `TestbedSecretsEncrypted` does, and costs about a minute.

- [ ] **Step 1:** Write the spec and commit it.
- [ ] **Step 2: RED.** Stop the sandbox (`k3d cluster stop homelab-dev`), then start the testbed with the nodes still on `dev`'s flake (`just testbed`). Run `just test e2e --env testbed --focus TestbedSeccomp`. Expected: FAIL (no file).
- [ ] **Step 3: GREEN.**
  - Pre-build the three nodes' closures (`nix build path:infra/_modules/metal/flake#nixosConfigurations.homelab-N.config.system.build.toplevel`) before `lab cluster upgrade`, so its hook does not build under memory pressure.
  - Upgrade, then rerun the spec. Expected: PASS.
  - A syscall needed only on NixOS's kernel goes into `buildah.json` and its README as "needed on NixOS's kernel", then back through Task 1's test.
- [ ] **Step 4:** Stop the testbed. Commit any profile change.

### Task 7: Docs

**Files:**
- Modify: `docs/how-to-guides/cluster-scan.md`: 4.2.14 fixed (row and text).
- Modify: `docs/how-to-guides/woodpecker.md`: Buildah steps get `homelab/buildah.json` by themselves. Other images run under `RuntimeDefault` and cannot `unshare`. No step may name a `Localhost` profile, nor `Unconfined`.
- Modify: `CLAUDE.md`:
  - roadmap 15h done, with its key facts;
  - 15d's "not `seccomp-default`" text;
  - production list: the profile was derived from k3s 1.37.1's containerd and measured on the testbed's kernel, so review it at a k3s minor bump. Woodpecker's Buildah steps on the hardware are its first real run.
- Modify: `docs/superpowers/deferred-minors.md`: remove the 15d row, add this branch's.

- [ ] **Step 1:** Write them. Run `mkdocs build --strict` and `scripts/ci-check all`. Expected: PASS.
- [ ] **Step 2:** Commit `docs(15h): seccomp-default on, Buildah's profile`.
