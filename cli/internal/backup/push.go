package backup

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FreeTier is R2's free storage, account-wide (docs/how-to-guides/backup-and-restore.md).
const FreeTier = 10_000_000_000

// Groups backed up off-site: daily by the schedule, manual only by Push.
var offsite = []any{"daily", "manual"}

// VolumeBytes is what the namespace's claims labelled daily or manual use, from the kubelets' summaries.
func (c *Client) VolumeBytes(ctx context.Context, namespace string) (int64, error) {
	claims, err := c.Kube.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{LabelSelector: "homelab/backup in (daily,manual)"})
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	for _, p := range claims.Items {
		want[p.Name] = true
	}
	nodes, err := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, err
	}
	var total int64
	for _, node := range nodes.Items {
		data, err := c.StatsSummary(ctx, node.Name)
		if err != nil {
			return 0, fmt.Errorf("node %s's stats summary: %w", node.Name, err)
		}
		var s struct {
			Pods []struct {
				Volume []struct {
					UsedBytes int64 `json:"usedBytes"`
					PVCRef    *struct {
						Name      string `json:"name"`
						Namespace string `json:"namespace"`
					} `json:"pvcRef"`
				} `json:"volume"`
			} `json:"pods"`
		}
		if err := json.Unmarshal(data, &s); err != nil {
			return 0, fmt.Errorf("node %s's stats summary: %w", node.Name, err)
		}
		// A claim mounted by two pods appears twice: count it once
		seen := map[string]bool{}
		for _, pod := range s.Pods {
			for _, v := range pod.Volume {
				if v.PVCRef != nil && v.PVCRef.Namespace == namespace && want[v.PVCRef.Name] && !seen[v.PVCRef.Name] {
					seen[v.PVCRef.Name] = true
					total += v.UsedBytes
				}
			}
		}
	}
	return total, nil
}

// Push copies one app's labelled volumes off-site now and waits up to wait for Velero to finish. It refuses while the
// location is read-only.
func (c *Client) Push(ctx context.Context, app string, wait time.Duration) (Run, error) {
	readOnly, err := c.Mode(ctx)
	if err != nil {
		return Run{}, err
	}
	if readOnly {
		return Run{}, fmt.Errorf("location default is read-only: lab backup enable first")
	}
	name := fmt.Sprintf("homelab-%s-%s", app, c.Now().UTC().Format("200601021504"))
	b := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"includedNamespaces": []any{app},
			"includedResources":  []any{"persistentvolumeclaims", "persistentvolumes"},
			"labelSelector": map[string]any{"matchExpressions": []any{
				map[string]any{"key": "homelab/backup", "operator": "In", "values": offsite},
			}},
			"snapshotMoveData": true,
			"storageLocation":  "default",
			// Retention is by count (system/velero's prune job), never by Velero's expiry
			"ttl": "87600h0m0s",
		},
	}}
	b.SetAPIVersion("velero.io/v1")
	b.SetKind("Backup")
	b.SetNamespace(Namespace)
	b.SetName(name)
	b.SetLabels(map[string]string{AppLabel: app})
	if _, err := c.Dyn.Resource(Backups).Namespace(Namespace).Create(ctx, b, metav1.CreateOptions{}); err != nil {
		return Run{}, fmt.Errorf("create backups.velero.io %s: %w", name, err)
	}
	return c.waitBackup(ctx, name, wait)
}

func (c *Client) waitBackup(ctx context.Context, name string, wait time.Duration) (Run, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		u, err := c.Dyn.Resource(Backups).Namespace(Namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
			switch phase {
			case "Completed":
				return Run{Name: name, App: u.GetLabels()[AppLabel], Phase: phase, Started: timestamp(*u, "startTimestamp"), Completed: timestamp(*u, "completionTimestamp")}, nil
			case "Failed", "PartiallyFailed", "FailedValidation":
				return Run{}, fmt.Errorf("backup %s %s (kubectl -n %s describe backups.velero.io %s)", name, phase, Namespace, name)
			}
		}
		select {
		case <-ctx.Done():
			return Run{}, fmt.Errorf("backup %s did not finish within %s", name, wait)
		case <-time.After(c.Poll):
		}
	}
}

// Credentials are the parts of Velero's credentials file BucketBytes needs.
type Credentials struct {
	AccessKey string
	SecretKey string
	Endpoint  string
}

// parseCredentials reads the AWS credentials file system/velero mounts. Its errors never quote the file.
func parseCredentials(file string) (Credentials, error) {
	var c Credentials
	scanner := bufio.NewScanner(strings.NewReader(file))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "aws_access_key_id":
			c.AccessKey = strings.TrimSpace(value)
		case "aws_secret_access_key":
			c.SecretKey = strings.TrimSpace(value)
		case "endpoint_url":
			c.Endpoint = strings.TrimSpace(value)
		}
	}
	if c.AccessKey == "" || c.SecretKey == "" || c.Endpoint == "" {
		return Credentials{}, fmt.Errorf("velero-credentials lacks aws_access_key_id, aws_secret_access_key or endpoint_url")
	}
	return c, nil
}
