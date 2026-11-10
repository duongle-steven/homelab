package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// The rule Falco's stable ruleset raises for a binary that was not in the image (Critical), and the vendored miner rule
// (system/falco/files/miners.yaml, Critical)
const (
	falcoDropRule  = "Drop and execute new binary in container"
	falcoMinerRule = "Detect crypto miners using the Stratum protocol"
)

// falcoProbe runs busybox in a namespace of its own and triggers two events in it: a read of /etc/shadow (a sensitive
// file, Warning), a copied binary executed (Critical; busybox picks its applet by name, so the copy is named true) and a
// command line naming a Stratum pool (Critical; nothing is contacted). It returns the namespace
func falcoProbe(ctx context.Context) string {
	GinkgoHelper()
	namespace := fmt.Sprint("falco-test-", time.Now().Unix())
	Expect(kube.CreateNamespace(ctx, namespace)).To(Succeed())
	DeferCleanup(func(ctx SpecContext) { Expect(kube.DeleteNamespace(ctx, namespace)).To(Succeed()) }, NodeTimeout(5*time.Minute))
	out, err := kube.Kubectl(ctx, namespace, "run", "probe", "--image=docker.io/library/busybox:1.37.0", "--restart=Never", "--", "sleep", "3600")
	Expect(err).NotTo(HaveOccurred(), out)
	out, err = kube.Kubectl(ctx, namespace, "wait", "--for=condition=Ready", "pod/probe", "--timeout=5m")
	Expect(err).NotTo(HaveOccurred(), out)
	out, err = kube.Kubectl(ctx, namespace, "exec", "probe", "--", "cat", "/etc/shadow")
	Expect(err).NotTo(HaveOccurred(), out)
	out, err = kube.Kubectl(ctx, namespace, "exec", "probe", "--", "sh", "-c", "cp /bin/busybox /tmp/true && /tmp/true")
	Expect(err).NotTo(HaveOccurred(), out)
	out, err = kube.Kubectl(ctx, namespace, "exec", "probe", "--", "sh", "-c", "true stratum+tcp://pool.invalid:3333")
	Expect(err).NotTo(HaveOccurred(), out)
	return namespace
}

// falcoEvent is the part of Falco's JSON output the specs read
type falcoEvent struct {
	Rule         string         `json:"rule"`
	Priority     string         `json:"priority"`
	OutputFields map[string]any `json:"output_fields"`
}

// lokiInstant runs a LogQL metric query now and returns each series' labels
func lokiInstant(ctx context.Context, query string) ([]map[string]string, error) {
	params := url.Values{}
	params.Set("query", query)
	out, err := kube.Kubectl(ctx, "loki", "get", "--raw",
		"/api/v1/namespaces/loki/services/loki:3100/proxy/loki/api/v1/query?"+params.Encode())
	if err != nil {
		return nil, err
	}
	var res struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, err
	}
	var series []map[string]string
	for _, r := range res.Data.Result {
		series = append(series, r.Metric)
	}
	return series, nil
}

// Falco (system/falco) sees a sensitive read, a dropped binary and a miner's command line in a pod, Loki keeps them with
// their rules, the deployed alert expression selects the critical ones in the pod's namespace, and Prometheus has the
// metrics FalcoStalled and FalcoEventsDropped read
var _ = Describe("Falco", Label("sandbox", "falco"), func() {
	It("reports a read of /etc/shadow, a dropped binary and a miner", func(ctx SpecContext) {
		// A renamed metric would leave FalcoStalled's counter arm and FalcoEventsDropped silent
		for _, query := range []string{
			`up{namespace="falco", job="falco-metrics"} == 1`,
			`falcosecurity_scap_n_evts_total{namespace="falco", job="falco-metrics"} > 0`,
			`falcosecurity_scap_n_drops_total{namespace="falco", job="falco-metrics"}`,
		} {
			Eventually(ctx, func() int {
				series, _ := prometheusQuery(ctx, query)
				return len(series)
			}).WithTimeout(5*time.Minute).WithPolling(15*time.Second).Should(BeNumerically(">", 0), "Prometheus has no %s", query)
		}

		namespace := falcoProbe(ctx)
		query := fmt.Sprintf(`{job="pods", namespace="falco"} |= %q`, namespace)
		var rules map[string]string
		Eventually(ctx, func() bool {
			entries, err := lokiQuery(ctx, query, 15*time.Minute)
			if err != nil {
				return false
			}
			rules = map[string]string{}
			for _, e := range entries {
				var event falcoEvent
				if json.Unmarshal([]byte(e.Line), &event) == nil && event.OutputFields["k8s.ns.name"] == namespace {
					rules[event.Rule] = event.Priority
				}
			}
			return rules[falcoDropRule] == "Critical" && rules[falcoMinerRule] == "Critical" && strings.Contains(fmt.Sprint(rules), "sensitive file")
		}).WithTimeout(10*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "Falco's events for %s: %v", namespace, rules)

		// The deployed expression, as Loki's ruler evaluates it
		raw, err := kube.Kubectl(ctx, "loki", "get", "configmap", "loki-audit-rules", "-o", `jsonpath={.data.falco-rules\.yaml}`)
		Expect(err).NotTo(HaveOccurred(), raw)
		var file struct {
			Groups []struct {
				Rules []struct {
					Alert string `yaml:"alert"`
					Expr  string `yaml:"expr"`
				} `yaml:"rules"`
			} `yaml:"groups"`
		}
		Expect(yaml.Unmarshal([]byte(raw), &file)).To(Succeed())
		var expr string
		for _, g := range file.Groups {
			for _, r := range g.Rules {
				if r.Alert == "FalcoCriticalEvent" {
					expr = r.Expr
				}
			}
		}
		Expect(expr).NotTo(BeEmpty(), "no FalcoCriticalEvent in loki-audit-rules")
		// Its window is 5 minutes: the event may be older by now, so widen it to the spec's
		expr = strings.Replace(expr, "[5m]", "[30m]", 1)
		Eventually(ctx, func() bool {
			series, err := lokiInstant(ctx, expr)
			if err != nil {
				return false
			}
			found := map[string]bool{}
			for _, s := range series {
				if s["namespace"] == namespace {
					found[s["rule"]] = true
				}
			}
			return found[falcoDropRule] && found[falcoMinerRule]
		}).WithTimeout(5*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "FalcoCriticalEvent's expression does not select %s's drop and miner events", namespace)
	}, SpecTimeout(15*time.Minute))
})

// On the testbed's three NixOS nodes: Falco runs on each with the modern eBPF probe, and a dropped binary is reported
// (the testbed has no Loki: the pods' logs are read directly). Run by hand with system/falco added to the testbed's list:
// the layer breaks this workstation's bar (docs/installation/testbed.md)
var _ = Describe("TestbedFalco", Label("testbed", "falco"), func() {
	It("watches every node", func(ctx SpecContext) {
		var ds struct {
			Status struct {
				Desired int `json:"desiredNumberScheduled"`
				Ready   int `json:"numberReady"`
			} `json:"status"`
		}
		Eventually(ctx, func() bool {
			return kube.GetJSON(ctx, "falco", &ds, "daemonset", "falco") == nil && ds.Status.Desired == 3 && ds.Status.Ready == 3
		}).WithTimeout(5*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), "falco ready on %d of %d nodes", ds.Status.Ready, ds.Status.Desired)

		pods, err := kube.Kubectl(ctx, "falco", "get", "pods", "-l", "app.kubernetes.io/name=falco", "-o", "name")
		Expect(err).NotTo(HaveOccurred(), pods)
		Expect(strings.Fields(pods)).To(HaveLen(3))
		for _, pod := range strings.Fields(pods) {
			log, err := kube.Kubectl(ctx, "falco", "logs", pod, "-c", "falco")
			Expect(err).NotTo(HaveOccurred(), log)
			Expect(log).To(ContainSubstring("modern BPF probe"), "%s did not load the modern eBPF probe", pod)
		}

		namespace := falcoProbe(ctx)
		Eventually(ctx, func() bool {
			for _, pod := range strings.Fields(pods) {
				log, err := kube.Kubectl(ctx, "falco", "logs", pod, "-c", "falco", "--since=15m")
				if err != nil {
					continue
				}
				for _, line := range strings.Split(log, "\n") {
					var event falcoEvent
					if json.Unmarshal([]byte(line), &event) == nil && event.Rule == falcoDropRule && event.OutputFields["k8s.ns.name"] == namespace {
						return true
					}
				}
			}
			return false
		}).WithTimeout(5*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "no %q for %s in Falco's logs", falcoDropRule, namespace)
	}, SpecTimeout(15*time.Minute))
})
