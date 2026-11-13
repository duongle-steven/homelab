package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const buildahGuard = "only the golden path's Buildah step may use a Localhost seccomp profile (homelab/buildah.json)"

// argoexec is Argo's own container in every Workflow pod (platform/argo-workflows' controller)
const argoexec = "quay.io/argoproj/argoexec:v4.1.6"

// stepPod is a pod shaped like the golden path's build (platform/ci: its label, a user namespace, Argo's wait
// container beside main), with main's image and the seccomp profiles given (nil: none) on the pod and on main
func stepPod(name, image string, podProfile, containerProfile map[string]string, command string) string {
	container := map[string]any{"name": "main", "image": image, "command": []string{"sh", "-c", command}}
	if containerProfile != nil {
		container["securityContext"] = map[string]any{"seccompProfile": containerProfile}
	}
	wait := map[string]any{"name": "wait", "image": argoexec, "command": []string{"argoexec", "version"}}
	spec := map[string]any{"restartPolicy": "Never", "hostUsers": false, "containers": []any{wait, container}}
	if podProfile != nil {
		spec["securityContext"] = map[string]any{"seccompProfile": podProfile}
	}
	out, _ := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "labels": map[string]string{"homelab/ci-step": "build"}},
		"spec":     spec,
	})
	return string(out)
}

// Step 15h, moved with step 10f: the kubelet's seccomp-default gives every pod naming no profile RuntimeDefault, and
// only the golden path's Buildah step gets the Localhost profile that lets it unshare (CI checks that it builds)
var _ = Describe("CISeccomp", Label("sandbox", "ci", "seccomp"), func() {
	localhost := map[string]string{"type": "Localhost", "localhostProfile": "homelab/buildah.json"}

	It("gives a build's other images RuntimeDefault, which refuses unshare", func(ctx SpecContext) {
		name := fmt.Sprintf("seccomp-probe-%d", time.Now().UnixNano())
		Expect(kube.Apply(ctx, "ci", stepPod(name, "docker.io/library/busybox:1.37.0", nil, nil,
			"grep Seccomp: /proc/self/status; unshare -U true && echo UNSHARED"))).To(Succeed())
		DeferCleanup(func(ctx context.Context) { kube.Kubectl(ctx, "ci", "delete", "pod", name, "--wait=false") }, NodeTimeout(time.Minute))
		Eventually(ctx, func() string {
			out, _ := kube.Kubectl(ctx, "ci", "get", "pod", name, "-o", "jsonpath={.status.phase}")
			return out
		}).WithTimeout(3 * time.Minute).WithPolling(2 * time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))
		profile, err := kube.Kubectl(ctx, "ci", "get", "pod", name, "-o", "jsonpath={.spec.securityContext.seccompProfile}")
		Expect(err).NotTo(HaveOccurred())
		Expect(profile).To(BeEmpty(), "the mutation gave busybox a profile")
		logs, err := kube.Kubectl(ctx, "ci", "logs", name, "-c", "main")
		Expect(err).NotTo(HaveOccurred())
		// Mode 2: a filter, which the pod did not ask for: the kubelet's seccomp-default
		Expect(logs).To(MatchRegexp(`Seccomp:\s+2`), "the node runs pods naming no profile unconfined")
		Expect(logs).NotTo(ContainSubstring("UNSHARED"))
		Expect(logs).To(ContainSubstring("Operation not permitted"))
	}, SpecTimeout(5*time.Minute))

	It("refuses Buildah's profile to other images", func(ctx SpecContext) {
		for i, pod := range []string{
			stepPod("seccomp-pod-level", "docker.io/library/busybox:1.37.0", localhost, nil, "true"),
			stepPod("seccomp-container-level", "docker.io/library/busybox:1.37.0", nil, localhost, "true"),
			stepPod("seccomp-other-profile", "quay.io/buildah/stable:v1.43.5", map[string]string{"type": "Localhost", "localhostProfile": "homelab/other.json"}, nil, "true"),
		} {
			err := kube.Apply(ctx, "ci", pod)
			if err == nil {
				kube.Kubectl(ctx, "ci", "delete", "pod", "seccomp-pod-level", "seccomp-container-level", "seccomp-other-profile", "--ignore-not-found", "--wait=false")
			}
			Expect(err).To(HaveOccurred(), "pod %d was admitted", i)
			Expect(err.Error()).To(ContainSubstring(buildahGuard), "pod %d", i)
		}
	}, SpecTimeout(2*time.Minute))

	It("refuses Unconfined", func(ctx SpecContext) {
		err := kube.Apply(ctx, "ci", stepPod("seccomp-unconfined", "docker.io/library/busybox:1.37.0",
			map[string]string{"type": "Unconfined"}, nil, "true"))
		if err == nil {
			kube.Kubectl(ctx, "ci", "delete", "pod", "seccomp-unconfined", "--wait=false")
		}
		Expect(err).To(HaveOccurred(), "an Unconfined pod was admitted in ci")
		Expect(strings.Contains(err.Error(), `violates PodSecurity "baseline`)).To(BeTrue(), err.Error())
	}, SpecTimeout(2*time.Minute))
})
