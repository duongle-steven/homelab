package e2e

import (
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/duongle-steven/homelab/test/internal/testenv"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// node-exporter reads metric files from its node's /var/lib/node-exporter (G11,
// docs/superpowers/specs/2026-11-27-node-patches-design.md): a file written on the k3d node reaches Prometheus. The
// NixOS nodes' own files are NodePatches'
var _ = Describe("NodeTextfileMetrics", Label("sandbox", "monitoring"), func() {
	It("scrapes a metric file on the node", func(ctx SpecContext) {
		write := "mkdir -p /var/lib/node-exporter && echo 'node_homelab_textfile_probe 1' > /var/lib/node-exporter/probe.prom"
		out, err := exec.CommandContext(ctx, "docker", "exec", testenv.K3dNode, "sh", "-c", write).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		DeferCleanup(func(ctx SpecContext) {
			_ = exec.CommandContext(ctx, "docker", "exec", testenv.K3dNode, "rm", "-f", "/var/lib/node-exporter/probe.prom").Run()
		})
		Eventually(ctx, func() int {
			metrics, err := prometheusQuery(ctx, "node_homelab_textfile_probe")
			if err != nil {
				return -1
			}
			return len(metrics)
		}).WithTimeout(3*time.Minute).WithPolling(15*time.Second).Should(Equal(1), "Prometheus has no series from the node's file")
	}, SpecTimeout(5*time.Minute))
})

// Every node publishes the age of the nixpkgs it was built from, the one this repository locks, and checks its store
// weekly with its reads capped (G11)
var _ = Describe("NodePatches", Label("testbed", "production", "proof"), Ordered, ContinueOnFailure, func() {
	var names []string
	var ips map[string]string
	BeforeAll(func() { names, ips = metalHosts() })

	It("publishes its nixpkgs' age and checks its store weekly", func(ctx SpecContext) {
		out, err := exec.CommandContext(ctx, "jq", "-r", `.nodes[.nodes.root.inputs.nixpkgs].locked.lastModified`,
			filepath.Join(repo.Root(), "infra/_modules/metal/flake/flake.lock")).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		locked := strings.TrimSpace(string(out))
		for _, name := range names {
			prom, err := testbedSSH(ctx, ips[name], "cat /var/lib/node-exporter/nixpkgs.prom")
			Expect(err).NotTo(HaveOccurred(), "%s: %s", name, prom)
			Expect(prom).To(ContainSubstring(`node_nixpkgs_last_modified_seconds{input="nixpkgs"} `+locked+"\n"),
				"%s runs another nixpkgs than this commit locks", name)
			Expect(prom).To(ContainSubstring(`node_nixpkgs_last_modified_seconds{input="nixpkgs-unstable"} `), name)
			enabled, err := testbedSSH(ctx, ips[name], "systemctl is-enabled nix-store-verify.timer")
			Expect(err).NotTo(HaveOccurred(), "%s: %s", name, enabled)
			Expect(enabled).To(Equal("enabled"), name)
			limit, err := testbedSSH(ctx, ips[name], "systemctl show nix-store-verify -p IOReadBandwidthMax")
			Expect(err).NotTo(HaveOccurred(), "%s: %s", name, limit)
			Expect(limit).To(Equal("IOReadBandwidthMax=/nix/store 40000000"),
				"%s: the check reads the whole store on etcd's disk, capped (the idle class does nothing under scheduler none)", name)
			persistent, err := testbedSSH(ctx, ips[name], "systemctl show nix-store-verify.timer -p Persistent")
			Expect(err).NotTo(HaveOccurred(), "%s: %s", name, persistent)
			Expect(persistent).To(Equal("Persistent=no"), "%s: no catch-up, so a power cut does not start every node's check at once", name)
		}
	}, SpecTimeout(5*time.Minute))
})
