// Package backup drives Velero for lab backup (docs/how-to-guides/backup-and-restore.md): it lists the off-site
// copies, switches the backup location between read-only and writable, pushes one app off-site and restores volumes.
// It always names Velero's kinds in full (backups.velero.io): CloudNativePG also serves a kind Backup.
package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Namespace is system/velero's.
const Namespace = "velero"

// Velero's resources.
var (
	Backups          = schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "backups"}
	Restores         = schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "restores"}
	Schedules        = schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "schedules"}
	StorageLocations = schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "backupstoragelocations"}
)

// Client reaches Velero's resources and the rest of the cluster.
type Client struct {
	Dyn  dynamic.Interface
	Kube kubernetes.Interface
	// StatsSummary is a node's kubelet summary (/stats/summary), through the API server's node proxy
	StatsSummary func(ctx context.Context, node string) ([]byte, error)
	Now          func() time.Time
	// Poll is how often a wait looks again, Wait how long one step of a restore may take
	Poll time.Duration
	Wait time.Duration
	// PortForward forwards a local port to a Service (the sandbox's S3 server) and returns its URL
	PortForward func(ctx context.Context, namespace, service string, port int) (url string, stop func(), err error)
	// ListBucket sums the sizes of a bucket's objects
	ListBucket func(ctx context.Context, creds Credentials, bucket string) (int64, error)
}

// New returns a Client on these clients.
func New(dyn dynamic.Interface, kube kubernetes.Interface) *Client {
	return &Client{
		Dyn:  dyn,
		Kube: kube,
		StatsSummary: func(ctx context.Context, node string) ([]byte, error) {
			return kube.CoreV1().RESTClient().Get().AbsPath("/api/v1/nodes", node, "proxy/stats/summary").DoRaw(ctx)
		},
		Now:        time.Now,
		Poll:       5 * time.Second,
		Wait:       30 * time.Minute,
		ListBucket: listBucket,
	}
}

// Run is one Velero backup: a scheduled off-site copy (Schedule) or one app's push (App).
type Run struct {
	Name      string
	Schedule  string
	App       string
	Phase     string
	Started   time.Time
	Completed time.Time
}

// Runs lists the backups, newest first. A backup synced from the bucket keeps the times it was taken.
func (c *Client) Runs(ctx context.Context) ([]Run, error) {
	list, err := c.Dyn.Resource(Backups).Namespace(Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list backups.velero.io: %w", err)
	}
	runs := make([]Run, 0, len(list.Items))
	for _, item := range list.Items {
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		labels := item.GetLabels()
		runs = append(runs, Run{
			Name:      item.GetName(),
			Schedule:  labels["velero.io/schedule-name"],
			App:       labels[AppLabel],
			Phase:     phase,
			Started:   timestamp(item, "startTimestamp"),
			Completed: timestamp(item, "completionTimestamp"),
		})
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].Started.After(runs[j].Started) })
	return runs, nil
}

func timestamp(item unstructured.Unstructured, field string) time.Time {
	s, _, _ := unstructured.NestedString(item.Object, "status", field)
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// ClusterCreated is when this cluster was built: kube-system's creation time.
func (c *Client) ClusterCreated(ctx context.Context) (time.Time, error) {
	ns, err := c.Kube.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return time.Time{}, err
	}
	return ns.CreationTimestamp.Time, nil
}

// PickRun returns the backup named name, which must have completed, or with an empty name the newest completed backup
// taken before created: a fresh cluster's own backups hold its empty volumes, never what a restore wants.
func PickRun(runs []Run, created time.Time, name string) (Run, error) {
	if name != "" {
		for _, r := range runs {
			if r.Name == name {
				if r.Phase != "Completed" {
					return Run{}, fmt.Errorf("backup %s is %s, not Completed", name, r.Phase)
				}
				return r, nil
			}
		}
		return Run{}, fmt.Errorf("no backup %s (lab backup list)", name)
	}
	for _, r := range runs {
		if r.Phase == "Completed" && r.Started.Before(created) {
			return r, nil
		}
	}
	return Run{}, fmt.Errorf("no completed backup was taken before this cluster was created (%s): name one with --backup (lab backup list)", created.Format(time.RFC3339))
}

// Mode reports whether location default is read-only (anything but ReadWrite counts as read-only).
func (c *Client) Mode(ctx context.Context) (readOnly bool, err error) {
	bsl, err := c.Dyn.Resource(StorageLocations).Namespace(Namespace).Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("backupstoragelocations.velero.io default: %w", err)
	}
	mode, _, _ := unstructured.NestedString(bsl.Object, "spec", "accessMode")
	return mode != "ReadWrite", nil
}

// SetMode makes location default read-only and pauses every schedule, or makes it writable and starts them. Argo CD
// ignores both fields (the root ApplicationSet), so the switch survives syncs.
func (c *Client) SetMode(ctx context.Context, readOnly bool) error {
	mode := "ReadWrite"
	if readOnly {
		mode = "ReadOnly"
	}
	if err := c.merge(ctx, StorageLocations, "default", map[string]any{"spec": map[string]any{"accessMode": mode}}); err != nil {
		return err
	}
	schedules, err := c.Dyn.Resource(Schedules).Namespace(Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list schedules.velero.io: %w", err)
	}
	for _, s := range schedules.Items {
		if err := c.merge(ctx, Schedules, s.GetName(), map[string]any{"spec": map[string]any{"paused": readOnly}}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) merge(ctx context.Context, gvr schema.GroupVersionResource, name string, patch map[string]any) error {
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	if _, err := c.Dyn.Resource(gvr).Namespace(Namespace).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch %s.%s %s: %w", gvr.Resource, gvr.Group, name, err)
	}
	return nil
}

// AppLabel marks the backups lab backup push makes, with the app (namespace) they copy.
const AppLabel = "homelab/app"
