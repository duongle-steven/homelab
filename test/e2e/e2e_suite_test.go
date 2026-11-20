package e2e

import (
	"net/http"
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
// A spec whose labels need a sandbox group the run did not deploy is skipped, or fails when the run asked for it
// (test/scopes.yaml deployGroups; just dev deploys what its selection needs, so this only meets a hand-run filter)
var _ = BeforeEach(func(ctx SpecContext) {
	skipUnlessGroups(ctx, scopeMap().GroupsOfLabels(CurrentSpecReport().Labels()))
})

var _ = BeforeSuite(func(ctx SpecContext) {
	if _, local := map[string]bool{"dev": true, "testbed": true}[suiteEnv()]; !local {
		return
	}
	syncForgejo(ctx)
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
}, NodeTimeout(45*time.Minute))

// syncForgejo runs lab infra forgejo sync once Forgejo answers, at most 15 minutes, as production's deploy does
// before Argo CD reads Forgejo: our apps' versions come from ops/releases (docs/how-to-guides/app-delivery.md), which
// Applications read from their first sync. A Forgejo that never answers leaves its specs to fail on their own
func syncForgejo(ctx SpecContext) {
	// Forgejo is group git's: a run without it has nothing to sync (waiting cost every such run 15 minutes)
	if suiteEnv() != "dev" || !appDeployed(ctx, "forgejo") {
		return
	}
	deadline := time.Now().Add(15 * time.Minute)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+forgejoHost+"/api/healthz", nil)
		Expect(err).NotTo(HaveOccurred())
		if resp, err := insecureClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				lab(ctx, "infra", "forgejo", "sync", "--env", labEnv)
				return
			}
		}
		if time.Now().After(deadline) {
			GinkgoWriter.Printf("Forgejo has not answered after 15 minutes: no lab infra forgejo sync before the specs\n")
			return
		}
		sleep(ctx, 15*time.Second)
		Expect(ctx.Err()).NotTo(HaveOccurred())
	}
}
