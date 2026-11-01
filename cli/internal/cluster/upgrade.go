package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// upgrader is what Upgrade does to one node; the commands behind it (nix, nixos-rebuild, ssh, kubectl) run from the
// workstation like nixie and enroll do
type upgrader interface {
	// isCurrent reports whether the node runs the system its flake builds now
	isCurrent(context.Context, host) (bool, error)
	drain(context.Context, host) error
	// rebuild switches the node to its flake's system, rebooting it when the kernel or initrd changed
	rebuild(context.Context, host) error
	ready(context.Context, host) error
	// healthy: every node Ready and, once a CephCluster exists, every mon and OSD running and Ceph HEALTH_OK
	healthy(context.Context) error
	uncordon(context.Context, host) error
}

// Upgrade rebuilds the installed nodes in place from the node flake at flakeDir, one at a time (the initializer
// first): a node already on its system is skipped; the others are drained, rebuilt, waited for and uncordoned, and
// the cluster must be healthy again before the next (a node's Ceph mon and OSD run only on it, so they come back
// only once it is uncordoned). It stops at the first failure; a node that fails before its uncordon stays cordoned.
func Upgrade(ctx context.Context, environment, flakeDir string, progress io.Writer) error {
	cfg, err := loadConfig(environment)
	if err != nil {
		return err
	}
	flake, err := filepath.Abs(flakeDir)
	if err != nil {
		return err
	}
	kubeconfig, err := filepath.Abs(filepath.Join("infra", environment, "kubeconfig.yaml"))
	if err != nil {
		return err
	}
	return upgrade(ctx, cfg, commands{flake: flake, kubeconfig: kubeconfig, progress: progress}, progress)
}

func upgrade(ctx context.Context, cfg config, nodes upgrader, progress io.Writer) error {
	for _, node := range append([]host{cfg.seed}, cfg.joiners...) {
		current, err := nodes.isCurrent(ctx, node)
		if err != nil {
			return fmt.Errorf("%s: %w", node.name, err)
		}
		if current {
			_, _ = fmt.Fprintf(progress, "%s: up to date\n", node.name)
			continue
		}
		_, _ = fmt.Fprintf(progress, "%s: upgrading\n", node.name)
		for _, step := range []struct {
			name string
			run  func() error
		}{
			{"drain", func() error { return nodes.drain(ctx, node) }},
			{"rebuild", func() error { return nodes.rebuild(ctx, node) }},
			{"wait for Ready", func() error { return nodes.ready(ctx, node) }},
			{"uncordon", func() error { return nodes.uncordon(ctx, node) }},
			{"wait for a healthy cluster", func() error { return nodes.healthy(ctx) }},
		} {
			if err := step.run(); err != nil {
				return fmt.Errorf("%s: %s: %w (fix it, then run the upgrade again)", node.name, step.name, err)
			}
		}
		_, _ = fmt.Fprintf(progress, "%s: upgraded\n", node.name)
	}
	return nil
}

type commands struct {
	flake, kubeconfig string
	progress          io.Writer
}

func (c commands) output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// run shows the command's output: a rebuild takes minutes
func (c commands) run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = c.progress, c.progress
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func (c commands) ssh(ctx context.Context, node host, command string) (string, error) {
	return c.output(ctx, "ssh", "-o", "BatchMode=yes", "root@"+node.address.Addr().String(), command)
}

func (c commands) kubectl(ctx context.Context, args ...string) (string, error) {
	return c.output(ctx, "kubectl", append([]string{"--kubeconfig", c.kubeconfig}, args...)...)
}

func (c commands) target(node host) string {
	return "path:" + c.flake + "#nixosConfigurations." + node.name + ".config.system.build.toplevel"
}

func (c commands) isCurrent(ctx context.Context, node host) (bool, error) {
	want, err := c.output(ctx, "nix", "build", "--no-link", "--print-out-paths", "--no-write-lock-file", c.target(node))
	if err != nil {
		return false, err
	}
	running, err := c.ssh(ctx, node, "readlink -f /run/current-system")
	if err != nil {
		return false, err
	}
	return running == want, nil
}

func (c commands) drain(ctx context.Context, node host) error {
	_, err := c.kubectl(ctx, "drain", node.name, "--ignore-daemonsets", "--delete-emptydir-data", "--timeout=10m")
	return err
}

func (c commands) rebuild(ctx context.Context, node host) error {
	if err := c.run(ctx, "nixos-rebuild", "switch", "--flake", "path:"+c.flake+"#"+node.name,
		"--target-host", "root@"+node.address.Addr().String()); err != nil {
		return err
	}
	// A new kernel, initrd or kernel modules only take effect after a reboot
	changed, err := c.ssh(ctx, node, `for f in initrd kernel kernel-modules; do `+
		`[ "$(readlink -f /run/booted-system/$f)" = "$(readlink -f /run/current-system/$f)" ] || echo $f; done`)
	if err != nil {
		return err
	}
	if changed == "" {
		return nil
	}
	_, _ = fmt.Fprintf(c.progress, "%s: rebooting (%s changed)\n", node.name, strings.ReplaceAll(changed, "\n", ", "))
	booted, _ := c.ssh(ctx, node, "cat /proc/sys/kernel/random/boot_id")
	_, _ = c.ssh(ctx, node, "systemctl reboot")
	return poll(ctx, 10*time.Minute, func() error {
		now, err := c.ssh(ctx, node, "cat /proc/sys/kernel/random/boot_id")
		if err != nil || now == booted {
			return fmt.Errorf("%s has not rebooted yet", node.name)
		}
		return nil
	})
}

func (c commands) ready(ctx context.Context, node host) error {
	return poll(ctx, 10*time.Minute, func() error {
		status, err := c.kubectl(ctx, "get", "node", node.name, "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
		if err != nil || status != "True" {
			return fmt.Errorf("%s is not Ready", node.name)
		}
		return nil
	})
}

func (c commands) healthy(ctx context.Context) error {
	return poll(ctx, 15*time.Minute, func() error {
		statuses, err := c.kubectl(ctx, "get", "nodes", "-o", `jsonpath={.items[*].status.conditions[?(@.type=="Ready")].status}`)
		if err != nil {
			return err
		}
		for _, status := range strings.Fields(statuses) {
			if status != "True" {
				return fmt.Errorf("a node is not Ready")
			}
		}
		// The node's mon and OSD back first: the CephCluster's health may not have noticed them gone yet
		pods, err := c.kubectl(ctx, "get", "pods", "-A", "-l", "app in (rook-ceph-mon,rook-ceph-osd)",
			"-o", `jsonpath={range .items[*]}{.metadata.name}={.status.conditions[?(@.type=="Ready")].status}{" "}{end}`)
		if err != nil {
			return err
		}
		for _, pod := range strings.Fields(pods) {
			if !strings.HasSuffix(pod, "=True") {
				return fmt.Errorf("Ceph pod %s is not ready", strings.Split(pod, "=")[0])
			}
		}
		// No CephCluster yet (or no rook-ceph at all): nothing more to wait for
		health, err := c.kubectl(ctx, "get", "cephclusters.ceph.rook.io", "-A", "-o", "jsonpath={.items[*].status.ceph.health}")
		if err != nil && !strings.Contains(err.Error(), "the server doesn't have a resource type") {
			return err
		}
		for _, h := range strings.Fields(health) {
			if h != "HEALTH_OK" {
				return fmt.Errorf("Ceph is %s", h)
			}
		}
		return nil
	})
}

func (c commands) uncordon(ctx context.Context, node host) error {
	_, err := c.kubectl(ctx, "uncordon", node.name)
	return err
}

// poll retries check every 10 seconds until it succeeds or wait has passed, returning its last error
func poll(ctx context.Context, wait time.Duration, check func() error) error {
	deadline := time.Now().Add(wait)
	for {
		err := check()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(10 * time.Second):
		}
	}
}
