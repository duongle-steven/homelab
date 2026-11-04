package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	markerNamespace = "kube-system"
	markerName      = "cluster-shutdown"
	// keepLabel marks a CronJob that recovery itself needs: the mesh's watchdog
	keepLabel = "homelab/power-keep"
	// cephConfig is where Rook's operator keeps the cluster's client configuration
	cephConfig = "/var/lib/rook/rook-ceph/rook-ceph.config"
)

// errNoCeph: the cluster has no CephCluster, so there is no noout to set
var errNoCeph = errors.New("no CephCluster")

// marker is what a shutdown changed, for the nodes' cluster-startup to undo
// (infra/_modules/metal/flake/profiles/cluster-startup.sh reads these keys)
type marker struct {
	Replicas int               // Argo CD's application controller before it stopped
	CronJobs []string          // namespace/name of those this shutdown suspended
	Boots    map[string]string // each node's boot ID: startup waits for a majority on a new boot
}

func (m marker) data() map[string]string {
	boots := make([]string, 0, len(m.Boots))
	for node, id := range m.Boots {
		boots = append(boots, node+"="+id)
	}
	sort.Strings(boots)
	return map[string]string{
		"replicas": strconv.Itoa(m.Replicas),
		"cronjobs": strings.Join(m.CronJobs, "\n"),
		"boots":    strings.Join(boots, "\n"),
	}
}

func parseMarker(data map[string]string) (marker, error) {
	replicas, err := strconv.Atoi(data["replicas"])
	if err != nil {
		return marker{}, fmt.Errorf("marker's replicas: %w", err)
	}
	m := marker{Replicas: replicas, CronJobs: strings.Fields(data["cronjobs"])}
	for _, line := range strings.Fields(data["boots"]) {
		node, id, _ := strings.Cut(line, "=")
		if m.Boots == nil {
			m.Boots = map[string]string{}
		}
		m.Boots[node] = id
	}
	return m, nil
}

type cronJob struct {
	name            string // namespace/name
	suspended, kept bool
}

// stopper is what Shutdown does to the cluster, from the workstation like Upgrade
type stopper interface {
	readMarker(context.Context) (marker, bool, error)
	writeMarker(context.Context, marker) error
	controllerReplicas(context.Context) (int, error)
	boots(context.Context) (map[string]string, error)
	stopController(context.Context) error
	cronJobs(context.Context) ([]cronJob, error)
	suspend(ctx context.Context, name string) error
	runningJobs(context.Context) ([]string, error)
	noout(context.Context) error
	powerOff(context.Context) error
}

// Shutdown stops the cluster in order and powers its nodes off, which stay off until someone starts them; their
// cluster-startup then undoes it. It waits at most jobWait for running Jobs. Run again after a failure, it finishes the job.
func Shutdown(ctx context.Context, environment string, jobWait time.Duration, progress io.Writer) error {
	cfg, err := loadConfig(environment)
	if err != nil {
		return err
	}
	kubeconfig, err := filepath.Abs(filepath.Join("infra", environment, "kubeconfig.yaml"))
	if err != nil {
		return err
	}
	return shutdown(ctx, clusterPower{commands{kubeconfig: kubeconfig, progress: progress}, append([]host{cfg.seed}, cfg.joiners...)}, jobWait, progress)
}

func shutdown(ctx context.Context, cluster stopper, jobWait time.Duration, progress io.Writer) error {
	if err := stopWorkloads(ctx, cluster, jobWait, progress); err != nil {
		return fmt.Errorf("%w (the nodes are still up: run lab cluster startup to undo it, or lab cluster shutdown again)", err)
	}
	_, _ = fmt.Fprintln(progress, "powering off every node")
	return cluster.powerOff(ctx)
}

func stopWorkloads(ctx context.Context, cluster stopper, jobWait time.Duration, progress io.Writer) error {
	saved, found, err := cluster.readMarker(ctx)
	if err != nil {
		return err
	}
	// A run after a partial one sees what that one changed: its marker is still the truth
	if !found {
		if saved.Replicas, err = cluster.controllerReplicas(ctx); err != nil {
			return err
		}
		if saved.Boots, err = cluster.boots(ctx); err != nil {
			return err
		}
		if err := cluster.writeMarker(ctx, saved); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintln(progress, "stopping Argo CD's application controller")
	if err := cluster.stopController(ctx); err != nil {
		return err
	}
	crons, err := cluster.cronJobs(ctx)
	if err != nil {
		return err
	}
	for _, cron := range crons {
		if cron.suspended || cron.kept {
			continue
		}
		if !slices.Contains(saved.CronJobs, cron.name) {
			saved.CronJobs = append(saved.CronJobs, cron.name)
			if err := cluster.writeMarker(ctx, saved); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintf(progress, "suspending %s\n", cron.name)
		if err := cluster.suspend(ctx, cron.name); err != nil {
			return err
		}
	}
	var running []string
	waited := poll(ctx, jobWait, func() error {
		var err error
		if running, err = cluster.runningJobs(ctx); err != nil {
			return err
		}
		if len(running) != 0 {
			return fmt.Errorf("Jobs still running")
		}
		return nil
	})
	if waited != nil {
		// A sudden cut is proven survivable, so a Job never holds the shutdown back
		_, _ = fmt.Fprintf(progress, "going on without waiting longer (%v): %s\n", waited, strings.Join(running, ", "))
	}
	if err := cluster.noout(ctx); err != nil && !errors.Is(err, errNoCeph) {
		return err
	}
	return nil
}

// clusterPower does it with kubectl and ssh
type clusterPower struct {
	commands
	nodes []host
}

func (c clusterPower) readMarker(ctx context.Context) (marker, bool, error) {
	out, err := c.kubectl(ctx, "-n", markerNamespace, "get", "configmap", markerName, "-o", "json")
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") {
			return marker{}, false, nil
		}
		return marker{}, false, err
	}
	var configMap struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &configMap); err != nil {
		return marker{}, false, err
	}
	m, err := parseMarker(configMap.Data)
	return m, err == nil, err
}

func (c clusterPower) writeMarker(ctx context.Context, m marker) error {
	patch, err := json.Marshal(map[string]any{"data": m.data()})
	if err != nil {
		return err
	}
	_, err = c.kubectl(ctx, "-n", markerNamespace, "patch", "configmap", markerName, "--type", "merge", "-p", string(patch))
	if err == nil || !strings.Contains(err.Error(), "NotFound") {
		return err
	}
	args := []string{"-n", markerNamespace, "create", "configmap", markerName}
	for key, value := range m.data() {
		args = append(args, "--from-literal="+key+"="+value)
	}
	_, err = c.kubectl(ctx, args...)
	return err
}

func (c clusterPower) controllerReplicas(ctx context.Context) (int, error) {
	out, err := c.kubectl(ctx, "-n", "argocd", "get", "statefulset", "argocd-application-controller", "-o", "jsonpath={.spec.replicas}")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

func (c clusterPower) boots(ctx context.Context) (map[string]string, error) {
	out, err := c.kubectl(ctx, "get", "nodes", "-o", `jsonpath={range .items[*]}{.metadata.name}={.status.nodeInfo.bootID}{"\n"}{end}`)
	if err != nil {
		return nil, err
	}
	boots := map[string]string{}
	for _, line := range strings.Fields(out) {
		node, id, _ := strings.Cut(line, "=")
		boots[node] = id
	}
	return boots, nil
}

func (c clusterPower) stopController(ctx context.Context) error {
	_, err := c.kubectl(ctx, "-n", "argocd", "scale", "statefulset", "argocd-application-controller", "--replicas=0")
	return err
}

func (c clusterPower) cronJobs(ctx context.Context) ([]cronJob, error) {
	out, err := c.kubectl(ctx, "get", "cronjobs", "-A", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace string            `json:"namespace"`
				Name      string            `json:"name"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Suspend bool `json:"suspend"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, err
	}
	crons := make([]cronJob, 0, len(list.Items))
	for _, item := range list.Items {
		crons = append(crons, cronJob{
			name:      item.Metadata.Namespace + "/" + item.Metadata.Name,
			suspended: item.Spec.Suspend,
			kept:      item.Metadata.Labels[keepLabel] == "true",
		})
	}
	return crons, nil
}

func (c clusterPower) suspend(ctx context.Context, name string) error {
	namespace, cron, _ := strings.Cut(name, "/")
	_, err := c.kubectl(ctx, "-n", namespace, "patch", "cronjob", cron, "--type", "merge", "-p", `{"spec":{"suspend":true}}`)
	return err
}

func (c clusterPower) runningJobs(ctx context.Context) ([]string, error) {
	out, err := c.kubectl(ctx, "get", "jobs", "-A", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Active int `json:"active"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, err
	}
	var running []string
	for _, item := range list.Items {
		if item.Status.Active > 0 {
			running = append(running, item.Metadata.Namespace+"/"+item.Metadata.Name)
		}
	}
	return running, nil
}

func (c clusterPower) noout(ctx context.Context) error {
	clusters, err := c.kubectl(ctx, "get", "cephclusters.ceph.rook.io", "-A", "-o", "name")
	if err != nil && strings.Contains(err.Error(), "the server doesn't have a resource type") || err == nil && clusters == "" {
		return errNoCeph
	}
	if err != nil {
		return err
	}
	_, err = c.kubectl(ctx, "-n", "rook-ceph", "exec", "deploy/rook-ceph-operator", "--", "ceph", "--conf="+cephConfig, "osd", "set", "noout")
	return err
}

// powerOff asks every node at once: losing them one by one would make etcd and Ceph react to each loss
func (c clusterPower) powerOff(ctx context.Context) error {
	var group sync.WaitGroup
	errs := make([]error, len(c.nodes))
	for i, node := range c.nodes {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := c.ssh(ctx, node, "systemctl poweroff --no-block")
			// The node going away under the connection is what was asked for
			if err != nil && !strings.Contains(err.Error(), "closed by remote host") && !strings.Contains(err.Error(), "reset by peer") {
				errs[i] = fmt.Errorf("%s: %w", node.name, err)
			}
		}()
	}
	group.Wait()
	return errors.Join(errs...)
}
