package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/argocd"
	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestE2E runs the cluster specs: just test e2e --env <env> chooses them by label (test/scopes.yaml's labels:)
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "e2e")
}

// Ginkgo runs the top-level containers in a random order (its seed is in the report), where go test ran the files in
// order and the first specs' long waits let a fresh sandbox or testbed finish its first sync before the shorter ones. So
// every local run first waits, at most 20 minutes, for Argo CD's Applications to settle; then the specs run as they
// are, each failing on its own (a Degraded app fails its specs, not the suite). Production is never waited on
var _ = BeforeSuite(func(ctx SpecContext) {
	if _, local := map[string]bool{"dev": true, "testbed": true}[suiteEnv()]; !local {
		return
	}
	deadline := time.Now().Add(20 * time.Minute)
	var waiting []string
	for {
		out, err := kube.Kubectl(ctx, "argocd", "get", "applications.argoproj.io", "-o", "json")
		if err == nil {
			waiting, err = argocd.Unsynced([]byte(out))
		}
		if err == nil && len(waiting) == 0 {
			return
		}
		if err != nil {
			waiting = []string{err.Error()}
		}
		if time.Now().After(deadline) {
			GinkgoWriter.Printf("Argo CD has not settled after 20 minutes, running the specs anyway: %s\n", strings.Join(waiting, ", "))
			return
		}
		sleep(ctx, 15*time.Second)
		Expect(ctx.Err()).NotTo(HaveOccurred())
	}
}, NodeTimeout(25*time.Minute))
