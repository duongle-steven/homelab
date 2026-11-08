package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/repo"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The weekly scan runs in the sandbox against the public apps it serves, through the LAN Gateway: it completes, loads
// its templates, finds nothing unaccepted at medium or above, and Loki's ruler holds PublicScanFinding. system/public-scan
// runs in the sandbox only
var _ = Describe("PublicScan", Label("sandbox", "public"), func() {
	It("scans the public apps", func(ctx SpecContext) {
		job := fmt.Sprintf("public-scan-test-%d", time.Now().Unix())
		_, err := kube.Kubectl(ctx, "public-scan", "create", "job", job, "--from=cronjob/public-scan")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) {
			_, _ = kube.Kubectl(ctx, "public-scan", "delete", "job", job, "--ignore-not-found")
		}, NodeTimeout(time.Minute))

		var state string
		Eventually(ctx, func() bool {
			state, _ = kube.Kubectl(ctx, "public-scan", "get", "job", job, "--output",
				`jsonpath={.status.conditions[?(@.status=="True")].type}`)
			return strings.Contains(state, "Complete") || strings.Contains(state, "Failed")
		}).WithTimeout(40*time.Minute).WithPolling(20*time.Second).Should(BeTrue(), "the scan did not finish")
		logs, err := kube.Kubectl(ctx, "public-scan", "logs", "job/"+job, "--tail=-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(ContainSubstring("Complete"), "the scan failed: %s", scanTail([]byte(logs)))

		lines := strings.Split(strings.TrimSpace(logs), "\n")
		var done struct {
			PublicScan string `json:"public_scan"`
			Templates  int    `json:"templates"`
		}
		Expect(json.Unmarshal([]byte(lines[len(lines)-1]), &done)).To(Succeed(), lines[len(lines)-1])
		Expect(done.PublicScan).To(Equal("done"))
		out, err := exec.CommandContext(ctx, "yq", ".minTemplates", filepath.Join(repo.Root(), "system/public-scan/values.yaml")).Output()
		Expect(err).NotTo(HaveOccurred())
		floor, err := strconv.Atoi(strings.TrimSpace(string(out)))
		Expect(err).NotTo(HaveOccurred())
		Expect(done.Templates).To(BeNumerically(">=", floor))
		GinkgoWriter.Printf("templates loaded: %d\n", done.Templates)
		for _, line := range lines {
			var f struct{ PublicScan, Template, Host, Severity string }
			if json.Unmarshal([]byte(line), &f) != nil || f.PublicScan != "finding" {
				continue
			}
			GinkgoWriter.Printf("finding: %s on %s (%s)\n", f.Template, f.Host, f.Severity)
			Expect([]string{"medium", "high", "critical"}).NotTo(ContainElement(f.Severity), "an unaccepted finding: %s on %s", f.Template, f.Host)
		}

		Eventually(ctx, func() bool {
			out, err := kube.Kubectl(ctx, "loki", "get", "--raw",
				"/api/v1/namespaces/loki/services/loki:3100/proxy/prometheus/api/v1/rules")
			return err == nil && strings.Contains(out, `"name":"PublicScanFinding"`)
		}).WithTimeout(5*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "Loki's ruler has no PublicScanFinding")
	}, SpecTimeout(50*time.Minute))
})
