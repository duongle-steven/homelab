package e2e

import (
	"fmt"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// In the sandbox: the operator reports running images, an image with a known fixable CRITICAL CVE raises
// ImageCriticalVulnerability's expression, and no other workload has an unaccepted CRITICAL finding
var _ = Describe("TrivyOperator", Label("sandbox", "testbed", "trivy-operator"), func() {
	It("reports images and raises the CRITICAL alerts", func(ctx SpecContext) {
		waitForDeployment(ctx, "trivy-operator", "trivy-operator")

		reported := func(namespace string) bool {
			out, err := kube.Kubectl(ctx, namespace, "get", "vulnerabilityreports.aquasecurity.github.io",
				"-o", "jsonpath={.items[*].report.summary.criticalCount}")
			return err == nil && strings.TrimSpace(out) != ""
		}
		Eventually(ctx, func() bool { return reported("excalidraw") }).WithTimeout(45*time.Minute).WithPolling(30*time.Second).
			Should(BeTrue(), "no vulnerability report for excalidraw")

		// A run cut by a timeout skips its cleanup: its old alpine would fail this spec and Policy
		leftovers, err := kube.Kubectl(ctx, "trivy-operator", "get", "namespaces", "-o", "name")
		Expect(err).NotTo(HaveOccurred())
		for _, ns := range strings.Fields(leftovers) {
			if strings.HasPrefix(ns, "namespace/trivy-test-") {
				_, _ = kube.Kubectl(ctx, "trivy-operator", "delete", ns, "--wait=false")
			}
		}

		// alpine 3.14.0: four CRITICAL CVEs with a fix (CVE-2021-36159, CVE-2021-3711)
		namespace := fmt.Sprint("trivy-test-", time.Now().Unix())
		Expect(kube.CreateNamespace(ctx, namespace)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { Expect(kube.DeleteNamespace(ctx, namespace)).To(Succeed()) }, NodeTimeout(5*time.Minute))
		out, err := kube.Kubectl(ctx, namespace, "run", "old-alpine", "--image=docker.io/library/alpine:3.14.0", "--restart=Never", "--", "sleep", "3600")
		Expect(err).NotTo(HaveOccurred(), out)
		// The alerts' own expressions, as deployed
		get := func(args ...string) string {
			out, err := kube.Kubectl(ctx, "trivy-operator", args...)
			Expect(err).NotTo(HaveOccurred(), out)
			return out
		}
		alert := func(name string) string {
			return get("get", "prometheusrules.monitoring.coreos.com", "trivy-operator", "-o", `jsonpath={.spec.groups[0].rules[?(@.alert=="`+name+`")].expr}`)
		}
		fixed, unfixed := alert("ImageCriticalVulnerability"), alert("ImageCriticalVulnerabilityUnfixed")
		Eventually(ctx, func() bool {
			found, err := prometheusQuery(ctx, fixed)
			if err != nil {
				return false
			}
			for _, m := range found {
				if m["namespace"] == namespace {
					return true
				}
			}
			return false
		}).WithTimeout(30*time.Minute).WithPolling(30*time.Second).Should(BeTrue(), "ImageCriticalVulnerability's expression does not see alpine 3.14.0 in %s", namespace)

		// Every running workload scanned first: a clean baseline over half the workloads proves nothing.
		// The operator sometimes drops a workload it lists at start, silently (ImageScanMissing fires for it in 6 hours):
		// one still unscanned after 10 minutes is touched once, and its update event queues its scan. So an operator that
		// scans a workload only after an update passes here; production's alert still catches it
		missing := alert("ImageScanMissing")
		var unscanned []string
		since := map[string]time.Time{}
		touched := map[string]bool{}
		Eventually(ctx, func() bool {
			found, err := prometheusQuery(ctx, missing)
			if err != nil {
				return false
			}
			unscanned = nil
			for _, m := range found {
				workload := m["namespace"] + "/" + m["resource_name"]
				unscanned = append(unscanned, workload)
				if since[workload].IsZero() {
					since[workload] = time.Now()
				}
				if touched[workload] || time.Since(since[workload]) < 10*time.Minute {
					continue
				}
				touched[workload] = true
				for _, kind := range []string{"replicaset", "statefulset", "daemonset"} {
					if m[kind] != "" {
						GinkgoWriter.Printf("%s has had no report for 10 minutes: touching its %s\n", workload, kind)
						if out, err := kube.Kubectl(ctx, m["namespace"], "annotate", kind, m[kind],
							fmt.Sprint("homelab/trivy-rescan=", time.Now().Unix()), "--overwrite"); err != nil {
							GinkgoWriter.Printf("touching %s failed: %v %s\n", workload, err, out)
						}
					}
				}
			}
			return len(unscanned) == 0
		}).WithTimeout(20*time.Minute).WithPolling(30*time.Second).Should(BeTrue(), func() string {
			return fmt.Sprintf("running workloads with no vulnerability report (ImageScanMissing's expression): %v", unscanned)
		})

		// The baseline: every other CRITICAL finding is fixed by a bump or accepted (system/trivy-operator/values.yaml).
		// The operator applies its ignore file only to new scans, and rescans a report only when it is a day old
		// (OPERATOR_SCANNER_REPORT_TTL): a report scanned before the operator's start is deleted, so it is scanned again
		// under the current settings
		started := get("get", "pods", "-l", "app.kubernetes.io/name=trivy-operator", "-o", "jsonpath={.items[0].status.startTime}")
		operatorStart, err := time.Parse(time.RFC3339, started)
		Expect(err).NotTo(HaveOccurred())
		rescanned := map[string]bool{}
		deleted := map[string][2]string{} // workload: its namespace and report selector, until scanned again
		var settled time.Time             // a new report reaches Prometheus at the next scrape
		var open []string
		Eventually(ctx, func() bool {
			open = nil
			for _, expr := range []string{fixed, unfixed} {
				found, err := prometheusQuery(ctx, expr)
				if err != nil {
					return false
				}
				for _, m := range found {
					if m["namespace"] == namespace {
						continue
					}
					workload := m["namespace"] + "/" + m["resource_name"]
					open = append(open, fmt.Sprintf("%s %s (%s)", workload, m["image_repository"], map[bool]string{true: "fix", false: "no fix"}[expr == fixed]))
					if rescanned[workload] {
						continue
					}
					rescanned[workload] = true
					selector := "trivy-operator.resource.name=" + m["resource_name"]
					created, err := kube.Kubectl(ctx, m["namespace"], "get", "vulnerabilityreports.aquasecurity.github.io", "-l", selector,
						"-o", "jsonpath={.items[*].metadata.creationTimestamp}")
					if err != nil {
						continue
					}
					for _, c := range strings.Fields(created) {
						if at, err := time.Parse(time.RFC3339, c); err == nil && at.Before(operatorStart) {
							GinkgoWriter.Printf("%s was scanned before the operator's start: scanning it again\n", workload)
							_, _ = kube.Kubectl(ctx, m["namespace"], "delete", "vulnerabilityreports.aquasecurity.github.io", "-l", selector)
							deleted[workload] = [2]string{m["namespace"], selector}
							break
						}
					}
				}
			}
			// A deleted report hides its findings until the new scan: wait for it
			for workload, d := range deleted {
				out, err := kube.Kubectl(ctx, d[0], "get", "vulnerabilityreports.aquasecurity.github.io",
					"-l", d[1], "-o", "jsonpath={.items[*].metadata.name}")
				if err != nil || strings.TrimSpace(out) == "" {
					open = append(open, workload+" (being scanned again)")
					continue
				}
				delete(deleted, workload)
				settled = time.Now().Add(2 * time.Minute)
			}
			return len(open) == 0 && time.Now().After(settled)
		}).WithTimeout(30*time.Minute).WithPolling(30*time.Second).Should(BeTrue(), func() string {
			return fmt.Sprintf("CRITICAL findings neither fixed nor accepted: %v", open)
		})
	}, SpecTimeout(60*time.Minute))
})
