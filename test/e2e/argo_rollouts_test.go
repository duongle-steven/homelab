package e2e

import (
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The controller downloads the Gateway API plugin at start and exits when it cannot (its egress, its sha256): a
// controller that stays Ready has it. AppDelivery drives a canary through it
var _ = Describe("ArgoRollouts", Label("sandbox", "delivery"), func() {
	It("runs with the Gateway API plugin", func(ctx SpecContext) {
		Eventually(ctx, func() string {
			out, _ := kube.Kubectl(ctx, "argo-rollouts", "get", "deploy", "argo-rollouts", "-o", "jsonpath={.status.availableReplicas}")
			return strings.TrimSpace(out)
		}).WithTimeout(10*time.Minute).WithPolling(10*time.Second).ShouldNot(BeElementOf("", "0"), "the controller is not available")
		out, err := kube.Kubectl(ctx, "argo-rollouts", "get", "configmap", "argo-rollouts-config", "-o", "jsonpath={.data.trafficRouterPlugins}")
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("argoproj-labs/gatewayAPI"))
		logs, err := kube.Kubectl(ctx, "argo-rollouts", "logs", "deploy/argo-rollouts", "--tail=-1")
		Expect(err).NotTo(HaveOccurred(), logs)
		Expect(logs).To(ContainSubstring("gatewayAPI"), "the controller's log never names the plugin")
		Expect(logs).NotTo(ContainSubstring("failed to download"), "the plugin's download failed")
	}, SpecTimeout(15*time.Minute))
})
