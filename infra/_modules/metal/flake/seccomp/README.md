# Seccomp profiles

The kubelet runs with `seccomp-default` (step 15h, kube-bench 4.2.14): a pod that names no profile gets `RuntimeDefault`.
These files are `Localhost` profiles, placed under the kubelet's seccomp root (`/var/lib/kubelet/seccomp/homelab/`) on
the nodes (`profiles/k3s-server.nix`) and in the sandbox's k3d node (`infra/_modules/k3d/main.tf`).

## `buildah.json`

Woodpecker's Buildah steps (`quay.io/buildah/stable`, `--isolation chroot`, `vfs`) get it from a
`MutatingAdmissionPolicy`, and only they may use it (`platform/woodpecker/templates/seccomp-policy.yaml`).

Its base is `RuntimeDefault` as k3s v1.37.1+k3s1 generates it: containerd v2.3.4-k3s1's `contrib/seccomp.DefaultProfile`
for containerd's default capabilities (no `CAP_SYS_ADMIN`), on amd64. The sandbox (`rancher/k3s:v1.37.1-k3s1`) and the
nodes (nixpkgs' `k3s_1_37`) run that same k3s. The last entry of `syscalls` adds what Buildah was seen to need:

| Syscall | Why |
|---|---|
| `unshare` | Buildah's chroot isolation enters new namespaces (a user and a mount namespace among them) to mount the image and run a `RUN` |
| `mount` | it bind-mounts `/proc`, `/sys`, `/dev` and the root into the namespace |
| `umount2` | it detaches the host's mounts there |
| `pivot_root` | it switches to the image's root |

`RuntimeDefault` allows those only with `CAP_SYS_ADMIN`. The pod already runs in its own user namespace
(`hostUsers: false`), so the kernel still checks every mount against that namespace's capabilities. `setns`, `clone`'s
namespace flags and `clone3` were not needed, and stay refused.

Measured on 2026-11-11 in the sandbox (WSL's kernel 6.18), with Buildah v1.43.5 (`quay.io/buildah/stable`) and `TestWoodpecker`'s `Containerfile`
(`FROM busybox`, a `RUN`): the base with `defaultAction: SCMP_ACT_LOG`, then the syscalls the kernel logged
(`type=1326`). The testbed (`TestbedSeccomp`) builds under it on NixOS's kernel.

When k3s's containerd changes its default profile, regenerate the base, update `buildahSeccompBase` in
`test/offline/seccomp_test.go` and repeat the measurement. When the Buildah steps move to a newer Buildah, repeat the
measurement: a release that takes other syscalls (the new mount API: `open_tree`, `move_mount`, `mount_setattr`) fails
its build with `Operation not permitted` until they are added here.

```go
// go.mod: require github.com/containerd/containerd/v2 <k3s's version>, replaced by k3s-io/containerd/v2 as k3s's go.mod does
caps := []string{"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FSETID", "CAP_FOWNER", "CAP_MKNOD", "CAP_NET_RAW", "CAP_SETGID",
	"CAP_SETUID", "CAP_SETFCAP", "CAP_SETPCAP", "CAP_NET_BIND_SERVICE", "CAP_SYS_CHROOT", "CAP_KILL", "CAP_AUDIT_WRITE"}
spec := &specs.Spec{Process: &specs.Process{Capabilities: &specs.LinuxCapabilities{Bounding: caps, Effective: caps, Permitted: caps}}}
json.NewEncoder(os.Stdout).Encode(seccomp.DefaultProfile(spec))
```
