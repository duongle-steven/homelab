package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/duongle-steven/homelab/cli/internal/workload"
)

// Source is where a restore takes a volume from.
type Source string

const (
	// Offsite is a Velero backup (R2, or the sandbox's S3 server).
	Offsite Source = "offsite"
	// Local is the newest hourly VolumeSnapshot at or before a time, on the nodes' disks: no internet needed.
	Local Source = "local"
)

// The other operators' resources a restore drives.
var (
	Applications    = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	CNPGClusters    = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
	Kanidms         = schema.GroupVersionResource{Group: "kaniop.rs", Version: "v1beta1", Resource: "kanidms"}
	VolumeSnapshots = schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshots"}
	ExternalSecrets = workload.ExternalSecrets
)

// Plan is a whole-cluster restore: the scheduled run for every app with a daily volume (OpenBao first), and each
// app's own newest push from before this cluster for its manual volumes.
type Plan struct {
	Run    Run
	Apps   []string
	Pushes map[string]Run
}

// PlanCluster lists what RestoreCluster restores from run. Argo CD has already created every app's (empty) claims
// from git, so their labels say which apps hold daily and manual volumes.
func (c *Client) PlanCluster(ctx context.Context, run Run) (Plan, error) {
	if run.App != "" {
		return Plan{}, fmt.Errorf("backup %s holds only %s's volumes: restore it with lab backup restore %s --from offsite --backup %s, or the whole cluster from a scheduled run", run.Name, run.App, run.App, run.Name)
	}
	claims, err := c.Kube.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{LabelSelector: "homelab/backup in (daily,manual)"})
	if err != nil {
		return Plan{}, err
	}
	daily, manual := map[string]bool{}, map[string]bool{}
	for _, p := range claims.Items {
		if p.Labels["homelab/backup"] == "daily" {
			daily[p.Namespace] = true
		} else {
			manual[p.Namespace] = true
		}
	}
	plan := Plan{Run: run, Pushes: map[string]Run{}}
	for ns := range daily {
		plan.Apps = append(plan.Apps, ns)
	}
	// OpenBao first: every other app reads its secrets
	sort.Slice(plan.Apps, func(i, j int) bool {
		if (plan.Apps[i] == "openbao") != (plan.Apps[j] == "openbao") {
			return plan.Apps[i] == "openbao"
		}
		return plan.Apps[i] < plan.Apps[j]
	})
	runs, err := c.Runs(ctx)
	if err != nil {
		return Plan{}, err
	}
	created, err := c.ClusterCreated(ctx)
	if err != nil {
		return Plan{}, err
	}
	// Runs is newest first: each manual app's newest push from before this cluster
	for ns := range manual {
		for _, r := range runs {
			if r.App == ns && r.Phase == "Completed" && r.Started.Before(created) {
				plan.Pushes[ns] = r
				break
			}
		}
	}
	return plan, nil
}

// AppRun is the off-site run to restore one app from: name, or the newest completed one holding it: its own push, or
// a scheduled run when it has a daily volume (scheduled runs hold only those)
func (c *Client) AppRun(ctx context.Context, runs []Run, app, name string) (Run, error) {
	if name != "" {
		return PickRun(runs, c.Now().Add(time.Hour), name)
	}
	daily, err := c.Kube.CoreV1().PersistentVolumeClaims(app).List(ctx, metav1.ListOptions{LabelSelector: "homelab/backup=daily"})
	if err != nil {
		return Run{}, err
	}
	for _, r := range runs {
		if r.Phase == "Completed" && (r.App == app || len(daily.Items) > 0 && r.App == "" && r.Schedule != "") {
			return r, nil
		}
	}
	return Run{}, fmt.Errorf("no completed off-site backup holds %s (lab backup list)", app)
}

// RestoreCluster restores a fresh cluster from plan, then makes every reader take the restored secrets. It refuses a
// writable location: until the restored data is checked, nothing may write or prune backups.
func (c *Client) RestoreCluster(ctx context.Context, plan Plan) error {
	readOnly, err := c.Mode(ctx)
	if err != nil {
		return err
	}
	if !readOnly {
		return fmt.Errorf("location default is writable: lab backup disable first (a restore needs it read-only)")
	}
	// Each app waits only for its own workloads: in production Argo CD reads Forgejo with a token the restore rolls
	// back, and reads again only once the readers refresh below
	for _, app := range plan.Apps {
		if err := c.restoreApp(ctx, app, Offsite, time.Time{}, plan.Run, c.waitWorkloads); err != nil {
			return err
		}
	}
	apps := make([]string, 0, len(plan.Pushes))
	for app := range plan.Pushes {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	for _, app := range apps {
		if err := c.restoreApp(ctx, app, Offsite, time.Time{}, plan.Pushes[app], c.waitWorkloads); err != nil {
			return err
		}
	}
	if err := c.refreshReaders(ctx); err != nil {
		return err
	}
	return c.waitApplications(ctx)
}

// RestoreApp replaces one app's volumes: from run's off-site copy, or from the newest local snapshot at or before
// at. Argo CD's automated sync stays paused until the app runs on the restored volumes; on an error it stays paused,
// with the app stopped, and the error says so.
func (c *Client) RestoreApp(ctx context.Context, app string, src Source, at time.Time, run Run) error {
	return c.restoreApp(ctx, app, src, at, run, c.waitHealthy)
}

func (c *Client) restoreApp(ctx context.Context, app string, src Source, at time.Time, run Run, wait func(context.Context, string) error) error {
	groups := []string{"daily", "manual"}
	if src == Offsite && run.App == "" {
		// A scheduled run holds only daily volumes
		groups = []string{"daily"}
	}
	list, err := c.Kube.CoreV1().PersistentVolumeClaims(app).List(ctx, metav1.ListOptions{LabelSelector: "homelab/backup in (" + strings.Join(groups, ",") + ")"})
	if err != nil {
		return err
	}
	saved, err := c.loadSaved(ctx, app)
	if err != nil {
		return err
	}
	// A failed run may have deleted claims already: they are in the saved state
	claims := list.Items
	for _, p := range saved.Claims {
		if !slices.ContainsFunc(claims, func(q corev1.PersistentVolumeClaim) bool { return q.Name == p.Name }) {
			claims = append(claims, p)
		}
	}
	if len(claims) == 0 {
		return fmt.Errorf("%s has no claim labelled homelab/backup %s", app, strings.Join(groups, " or "))
	}
	saved.Claims = nil
	for _, p := range claims {
		saved.Claims = append(saved.Claims, savedClaim(p))
	}
	sources := map[string]string{}
	if src == Local {
		// Every snapshot is chosen before anything is touched
		for _, p := range claims {
			name, err := c.snapshotAt(ctx, app, p.Name, at)
			if err != nil {
				return err
			}
			sources[p.Name] = name
		}
	}
	if err := c.pause(ctx, app, saved); err != nil {
		return err
	}
	stopped := paused(app)
	if err := c.stop(ctx, app, claims, saved); err != nil {
		return stopped(err)
	}
	if err := c.replace(ctx, app, src, run, claims, sources); err != nil {
		return stopped(err)
	}
	if err := c.start(ctx, app, saved); err != nil {
		return stopped(err)
	}
	if err := c.resetAgentIDs(ctx, app); err != nil {
		return stopped(err)
	}
	if saved.openbao {
		if err := c.restoreOpenBao(ctx); err != nil {
			return stopped(err)
		}
	}
	if saved.kanidm {
		if err := c.refreshKaniop(ctx, app); err != nil {
			return stopped(err)
		}
	}
	// Only now: OpenBao's snapshot CronJob, run before the raft restore, would replace the restored raft.snap with a
	// snapshot of the new, empty OpenBao
	if err := c.startCronJobs(ctx, app, saved); err != nil {
		return stopped(err)
	}
	if err := c.resume(ctx, app, saved); err != nil {
		return stopped(err)
	}
	return wait(ctx, app)
}

func paused(app string) func(error) error {
	return func(err error) error {
		return fmt.Errorf("%w\nApplication %s stays paused and its workloads may be stopped: fix the cause, then run the restore again (or resume its sync in Argo CD)", err, app)
	}
}

func (c *Client) patch(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, patch any) error {
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	if _, err := c.Dyn.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch %s.%s %s/%s: %w", gvr.Resource, gvr.Group, namespace, name, err)
	}
	return nil
}

// restoreState is the Application annotation holding what a restore changed (its automated sync, the replicas it
// stopped), written before anything changes: a restore run again after a failure finds the app already stopped and
// puts back these, not what it finds
const restoreState = "homelab/restore-state"

// savedApp is what pause and stop changed, for start and resume to put back
type savedApp struct {
	Automated    map[string]any   `json:"automated"`
	Deployments  map[string]int32 `json:"deployments,omitempty"`
	StatefulSets map[string]int32 `json:"statefulSets,omitempty"`
	Kanidms      map[string][]any `json:"kanidms,omitempty"`
	CronJobs     []string         `json:"cronJobs,omitempty"`
	// Claims are the claims being replaced, as they were before
	Claims   []corev1.PersistentVolumeClaim `json:"claims,omitempty"`
	clusters []string
	openbao  bool
	kanidm   bool
}

func (c *Client) save(ctx context.Context, app string, saved *savedApp) error {
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	return c.patch(ctx, Applications, "argocd", app, map[string]any{"metadata": map[string]any{"annotations": map[string]any{restoreState: string(data)}}})
}

// loadSaved is the state a failed restore of app left in restoreState, or the Application's automated sync as it is
func (c *Client) loadSaved(ctx context.Context, app string) (*savedApp, error) {
	a, err := c.Dyn.Resource(Applications).Namespace("argocd").Get(ctx, app, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("Application %s: %w", app, err)
	}
	saved := &savedApp{}
	if state, ok := a.GetAnnotations()[restoreState]; ok {
		if err := json.Unmarshal([]byte(state), saved); err != nil {
			return nil, fmt.Errorf("Application %s: annotation %s: %w", app, restoreState, err)
		}
		return saved, nil
	}
	saved.Automated, _, _ = unstructured.NestedMap(a.Object, "spec", "syncPolicy", "automated")
	return saved, nil
}

// savedClaim is the claim without what the API server and the binder set
func savedClaim(p corev1.PersistentVolumeClaim) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: p.Name, Labels: p.Labels, Annotations: p.Annotations, OwnerReferences: p.OwnerReferences},
		Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: p.Spec.AccessModes, Resources: p.Spec.Resources, StorageClassName: p.Spec.StorageClassName, VolumeMode: p.Spec.VolumeMode},
	}
}

// pause removes the Application's automated sync (the ApplicationSets ignore that field) after saving it in
// restoreState, then waits for a sync already running to end: it would recreate the claims being replaced
func (c *Client) pause(ctx context.Context, app string, saved *savedApp) error {
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if err := c.patch(ctx, Applications, "argocd", app, map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{restoreState: string(data)}},
		"spec":     map[string]any{"syncPolicy": map[string]any{"automated": nil}},
	}); err != nil {
		return err
	}
	return c.until(ctx, "Argo CD's running sync of "+app+" to end (argocd app terminate-op "+app+")", func() (bool, error) {
		a, err := c.Dyn.Resource(Applications).Namespace("argocd").Get(ctx, app, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(a.Object, "status", "operationState", "phase")
		return phase != "Running" && phase != "Terminating", nil
	})
}

// resume puts back the automated sync and drops restoreState
func (c *Client) resume(ctx context.Context, app string, saved *savedApp) error {
	spec := map[string]any{}
	if saved.Automated != nil {
		spec["syncPolicy"] = map[string]any{"automated": saved.Automated}
	}
	return c.patch(ctx, Applications, "argocd", app, map[string]any{"metadata": map[string]any{"annotations": map[string]any{restoreState: nil}}, "spec": spec})
}

func scale(replicas int32) []byte {
	return []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))
}

// stop takes the app off its volumes: Deployments and StatefulSets to 0, kaniop's Kanidm through its replicas (and
// its StatefulSets: kaniop stops with the Deployments), CloudNativePG clusters hibernated, CronJobs suspended; then it
// waits for no pod to mount the claims, deleting the finished ones (a finished pod still holds its claim). What it
// changes is saved first (a workload a failed run already stopped keeps the replicas saved then).
func (c *Client) stop(ctx context.Context, app string, claims []corev1.PersistentVolumeClaim, s *savedApp) error {
	if s.Deployments == nil {
		s.Deployments = map[string]int32{}
	}
	if s.StatefulSets == nil {
		s.StatefulSets = map[string]int32{}
	}
	if s.Kanidms == nil {
		s.Kanidms = map[string][]any{}
	}
	apps := c.Kube.AppsV1()
	deployments, err := apps.Deployments(app).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, d := range deployments.Items {
		if r := ptr.Deref(d.Spec.Replicas, 1); r > 0 {
			s.Deployments[d.Name] = r
		}
	}
	kanidms, err := c.Dyn.Resource(Kanidms).Namespace(app).List(ctx, metav1.ListOptions{})
	if err != nil {
		kanidms = &unstructured.UnstructuredList{}
	}
	for _, k := range kanidms.Items {
		groups, _, _ := unstructured.NestedSlice(k.Object, "spec", "replicaGroups")
		if _, ok := s.Kanidms[k.GetName()]; !ok {
			s.Kanidms[k.GetName()] = groups
		}
		s.kanidm = true
	}
	statefulSets, err := apps.StatefulSets(app).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	var kaniop []string
	for _, st := range statefulSets.Items {
		if workload.OwnedBy(st.OwnerReferences, "Kanidm") {
			kaniop = append(kaniop, st.Name)
			continue
		}
		if st.Name == "openbao" {
			s.openbao = true
		}
		if r := ptr.Deref(st.Spec.Replicas, 1); r > 0 {
			s.StatefulSets[st.Name] = r
		}
	}
	if app == "openbao" {
		s.openbao = true
	}
	clusters, err := c.Dyn.Resource(CNPGClusters).Namespace(app).List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, cl := range clusters.Items {
			s.clusters = append(s.clusters, cl.GetName())
		}
	}
	cronJobs, err := c.Kube.BatchV1().CronJobs(app).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, cj := range cronJobs.Items {
		if !ptr.Deref(cj.Spec.Suspend, false) && !slices.Contains(s.CronJobs, cj.Name) {
			s.CronJobs = append(s.CronJobs, cj.Name)
		}
	}
	if err := c.save(ctx, app, s); err != nil {
		return err
	}

	for name := range s.Deployments {
		if _, err := apps.Deployments(app).Patch(ctx, name, types.MergePatchType, scale(0), metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	for name, groups := range s.Kanidms {
		zero := make([]any, 0, len(groups))
		for _, g := range groups {
			m := map[string]any{}
			for key, v := range g.(map[string]any) {
				m[key] = v
			}
			m["replicas"] = int64(0)
			zero = append(zero, m)
		}
		if err := c.patch(ctx, Kanidms, app, name, map[string]any{"spec": map[string]any{"replicaGroups": zero}}); err != nil {
			return err
		}
	}
	// kaniop stops with its namespace's Deployments, so its StatefulSets go to 0 here too; it starts them again from
	// the Kanidm once it runs
	for _, name := range kaniop {
		if _, err := apps.StatefulSets(app).Patch(ctx, name, types.MergePatchType, scale(0), metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	for name := range s.StatefulSets {
		if _, err := apps.StatefulSets(app).Patch(ctx, name, types.MergePatchType, scale(0), metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	for _, name := range s.clusters {
		if err := c.patch(ctx, CNPGClusters, app, name, map[string]any{"metadata": map[string]any{"annotations": map[string]any{"cnpg.io/hibernation": "on"}}}); err != nil {
			return err
		}
	}
	for _, name := range s.CronJobs {
		if _, err := c.Kube.BatchV1().CronJobs(app).Patch(ctx, name, types.MergePatchType, []byte(`{"spec":{"suspend":true}}`), metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	names := map[string]bool{}
	for _, p := range claims {
		names[p.Name] = true
	}
	return c.until(ctx, "no pod of "+app+" mounting its claims", func() (bool, error) {
		pods, err := c.Kube.CoreV1().Pods(app).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, p := range pods.Items {
			mounts := false
			for _, v := range p.Spec.Volumes {
				if v.PersistentVolumeClaim != nil && names[v.PersistentVolumeClaim.ClaimName] {
					mounts = true
				}
			}
			if !mounts {
				continue
			}
			// A finished Job's pod still holds the claim (pvc-protection), and its Job keeps the record
			if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
				if err := c.Kube.CoreV1().Pods(app).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
					return false, err
				}
				continue
			}
			return false, nil
		}
		return true, nil
	})
}

// start puts back what stop changed but the CronJobs (startCronJobs), the clusters' reconciliation first
func (c *Client) start(ctx context.Context, app string, s *savedApp) error {
	for _, name := range s.clusters {
		if err := c.patch(ctx, CNPGClusters, app, name, map[string]any{"metadata": map[string]any{"annotations": map[string]any{"cnpg.io/hibernation": nil}}}); err != nil {
			return err
		}
	}
	for name, groups := range s.Kanidms {
		if err := c.patch(ctx, Kanidms, app, name, map[string]any{"spec": map[string]any{"replicaGroups": groups}}); err != nil {
			return err
		}
	}
	apps := c.Kube.AppsV1()
	for name, r := range s.StatefulSets {
		if _, err := apps.StatefulSets(app).Patch(ctx, name, types.MergePatchType, scale(r), metav1.PatchOptions{}); err != nil {
			return err
		}
	}
	for name, r := range s.Deployments {
		if _, err := apps.Deployments(app).Patch(ctx, name, types.MergePatchType, scale(r), metav1.PatchOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// startCronJobs undoes stop's suspension, once the app's own restore steps are done
func (c *Client) startCronJobs(ctx context.Context, app string, s *savedApp) error {
	for _, name := range s.CronJobs {
		if _, err := c.Kube.BatchV1().CronJobs(app).Patch(ctx, name, types.MergePatchType, []byte(`{"spec":{"suspend":false}}`), metav1.PatchOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// replace deletes the claims and restores them, keeping CloudNativePG from declaring its cluster lost meanwhile and
// giving restored claims back the owner references Velero drops
func (c *Client) replace(ctx context.Context, app string, src Source, run Run, claims []corev1.PersistentVolumeClaim, sources map[string]string) error {
	clusters, _ := c.Dyn.Resource(CNPGClusters).Namespace(app).List(ctx, metav1.ListOptions{})
	var cnpg []string
	if clusters != nil {
		for _, cl := range clusters.Items {
			cnpg = append(cnpg, cl.GetName())
			if err := c.patch(ctx, CNPGClusters, app, cl.GetName(), map[string]any{"metadata": map[string]any{"annotations": map[string]any{"cnpg.io/reconciliationLoop": "disabled"}}}); err != nil {
				return err
			}
		}
	}
	pvcs := c.Kube.CoreV1().PersistentVolumeClaims(app)
	for _, p := range claims {
		if err := pvcs.Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	for _, p := range claims {
		name := p.Name
		if err := c.until(ctx, "claim "+app+"/"+name+" deleted", func() (bool, error) {
			_, err := pvcs.Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}); err != nil {
			return err
		}
	}
	switch src {
	case Offsite:
		restore, err := c.veleroRestore(ctx, app, run)
		if err != nil {
			return err
		}
		// Velero completes without a claim the run does not hold, or one that came back meanwhile
		for _, p := range claims {
			restored, err := pvcs.Get(ctx, p.Name, metav1.GetOptions{})
			if err != nil || restored.Labels["velero.io/restore-name"] != restore {
				return fmt.Errorf("claim %s/%s was not restored by %s (kubectl -n %s describe restores.velero.io %s): does backup %s hold it?", app, p.Name, restore, Namespace, restore, run.Name)
			}
		}
	case Local:
		for _, p := range claims {
			if _, err := pvcs.Create(ctx, fromSnapshot(p, sources[p.Name]), metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("claim %s/%s from snapshot %s: %w", app, p.Name, sources[p.Name], err)
			}
		}
	default:
		return fmt.Errorf("unknown source %q (offsite or local)", src)
	}
	for _, p := range claims {
		if len(p.OwnerReferences) == 0 {
			continue
		}
		restored, err := pvcs.Get(ctx, p.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("restored claim %s/%s: %w", app, p.Name, err)
		}
		if len(restored.OwnerReferences) == 0 {
			data, err := json.Marshal(map[string]any{"metadata": map[string]any{"ownerReferences": p.OwnerReferences}})
			if err != nil {
				return err
			}
			if _, err := pvcs.Patch(ctx, p.Name, types.MergePatchType, data, metav1.PatchOptions{}); err != nil {
				return err
			}
		}
	}
	for _, name := range cnpg {
		if err := c.patch(ctx, CNPGClusters, app, name, map[string]any{"metadata": map[string]any{"annotations": map[string]any{"cnpg.io/reconciliationLoop": nil}}}); err != nil {
			return err
		}
	}
	return nil
}

// fromSnapshot is the claim again, with its metadata and spec, its data from snapshot
func fromSnapshot(p corev1.PersistentVolumeClaim, snapshot string) *corev1.PersistentVolumeClaim {
	annotations := map[string]string{}
	for k, v := range p.Annotations {
		// The provisioner's and binder's own, set again on the new claim
		if strings.HasPrefix(k, "pv.kubernetes.io/") || strings.HasPrefix(k, "volume.kubernetes.io/") || strings.HasPrefix(k, "volume.beta.kubernetes.io/") {
			continue
		}
		annotations[k] = v
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       p.Namespace,
			Name:            p.Name,
			Labels:          p.Labels,
			Annotations:     annotations,
			OwnerReferences: p.OwnerReferences,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      p.Spec.AccessModes,
			Resources:        p.Spec.Resources,
			StorageClassName: p.Spec.StorageClassName,
			VolumeMode:       p.Spec.VolumeMode,
			DataSource:       &corev1.TypedLocalObjectReference{APIGroup: ptr.To("snapshot.storage.k8s.io"), Kind: "VolumeSnapshot", Name: snapshot},
		},
	}
}

// snapshotAt is the newest ready hourly snapshot of the claim taken at or before at (zero: now)
func (c *Client) snapshotAt(ctx context.Context, app, claim string, at time.Time) (string, error) {
	if at.IsZero() {
		at = c.Now()
	}
	list, err := c.Dyn.Resource(VolumeSnapshots).Namespace(app).List(ctx, metav1.ListOptions{LabelSelector: "homelab/snapshot=hourly,homelab/pvc=" + claim})
	if err != nil {
		return "", fmt.Errorf("volumesnapshots of %s/%s: %w", app, claim, err)
	}
	best, bestTime := "", time.Time{}
	for _, s := range list.Items {
		ready, _, _ := unstructured.NestedBool(s.Object, "status", "readyToUse")
		created := s.GetCreationTimestamp().Time
		if ready && !created.After(at) && created.After(bestTime) {
			best, bestTime = s.GetName(), created
		}
	}
	if best == "" {
		return "", fmt.Errorf("no ready snapshot of %s/%s at or before %s (lab backup list; or --from offsite)", app, claim, at.Format(time.RFC3339))
	}
	return best, nil
}

func (c *Client) veleroRestore(ctx context.Context, app string, run Run) (string, error) {
	if run.Name == "" {
		return "", fmt.Errorf("no backup to restore %s from", app)
	}
	name := fmt.Sprintf("%s-%s-%s", app, run.Name, c.Now().UTC().Format("20060102150405"))
	if len(name) > 63 {
		name = name[len(name)-63:]
		name = strings.TrimLeft(name, "-.")
	}
	r := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"backupName":         run.Name,
		"includedNamespaces": []any{app},
		"includedResources":  []any{"persistentvolumeclaims", "persistentvolumes"},
	}}}
	r.SetAPIVersion("velero.io/v1")
	r.SetKind("Restore")
	r.SetNamespace(Namespace)
	r.SetName(name)
	if _, err := c.Dyn.Resource(Restores).Namespace(Namespace).Create(ctx, r, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("create restores.velero.io %s: %w", name, err)
	}
	return name, c.until(ctx, "restore "+name, func() (bool, error) {
		u, err := c.Dyn.Resource(Restores).Namespace(Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
		switch phase {
		case "Completed":
			return true, nil
		case "Failed", "PartiallyFailed", "FailedValidation":
			return false, fmt.Errorf("restore %s %s (kubectl -n %s describe restores.velero.io %s)", name, phase, Namespace, name)
		}
		return false, nil
	})
}

// restoreOpenBao replaces OpenBao's data with its restored raft snapshot, through a Job as the admin role: the
// snapshot's volume is not OpenBao's own
func (c *Client) restoreOpenBao(ctx context.Context) error {
	const ns = "openbao"
	cron, err := c.Kube.BatchV1().CronJobs(ns).Get(ctx, "openbao-snapshot", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("CronJob openbao/openbao-snapshot (its image): %w", err)
	}
	if err := c.until(ctx, "OpenBao ready", func() (bool, error) {
		st, err := c.Kube.AppsV1().StatefulSets(ns).Get(ctx, "openbao", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return err == nil && st.Status.ReadyReplicas >= ptr.Deref(st.Spec.Replicas, 1), err
	}); err != nil {
		return err
	}
	image := cron.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "openbao-restore-" + c.Now().UTC().Format("20060102150405")},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To[int32](0),
			TTLSecondsAfterFinished: ptr.To[int32](3600),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				ServiceAccountName:           "openbao-admin",
				AutomountServiceAccountToken: ptr.To(false),
				RestartPolicy:                corev1.RestartPolicyNever,
				SecurityContext:              cron.Spec.JobTemplate.Spec.Template.Spec.SecurityContext,
				Containers: []corev1.Container{{
					Name:    "restore",
					Image:   image,
					Command: []string{"sh", "-ec"},
					Args: []string{`export BAO_TOKEN="$(bao write -field=token auth/kubernetes/login role=admin jwt=@/var/run/secrets/openbao/token)"; ` +
						`bao operator raft snapshot restore -force /snapshots/raft.snap`},
					Env:          []corev1.EnvVar{{Name: "BAO_ADDR", Value: "http://openbao.openbao:8200"}},
					VolumeMounts: []corev1.VolumeMount{{Name: "snapshots", MountPath: "/snapshots", ReadOnly: true}, {Name: "token", MountPath: "/var/run/secrets/openbao", ReadOnly: true}},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: ptr.To(false),
						ReadOnlyRootFilesystem:   ptr.To(true),
						Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					},
				}},
				Volumes: []corev1.Volume{
					{Name: "snapshots", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "openbao-snapshots"}}},
					{Name: "token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{
						ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: "openbao", ExpirationSeconds: ptr.To[int64](600), Path: "token"},
					}}}}},
				},
			}},
		},
	}
	jobs := c.Kube.BatchV1().Jobs(ns)
	if _, err := jobs.Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("Job %s: %w", job.Name, err)
	}
	return c.until(ctx, "Job openbao/"+job.Name, func() (bool, error) {
		j, err := jobs.Get(ctx, job.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if j.Status.Failed > 0 {
			return false, fmt.Errorf("OpenBao's snapshot restore failed (kubectl -n openbao logs job/%s)", job.Name)
		}
		return j.Status.Succeeded > 0, nil
	})
}

// refreshKaniop deletes the Secrets kaniop writes only when they are missing (Kanidm's admin passwords, every OAuth2
// client's), so it writes them again from the restored data, then restarts their readers
func (c *Client) refreshKaniop(ctx context.Context, app string) error {
	secrets, err := c.Kube.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	var gone []corev1.Secret
	for _, s := range secrets.Items {
		if (s.Namespace == app && s.Name == "kanidm-admin-passwords") || strings.HasSuffix(s.Name, "-kanidm-oauth2-credentials") {
			if err := c.Kube.CoreV1().Secrets(s.Namespace).Delete(ctx, s.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			gone = append(gone, s)
		}
	}
	namespaces := map[string]bool{}
	for _, s := range gone {
		ns, name := s.Namespace, s.Name
		if err := c.until(ctx, "kaniop's Secret "+ns+"/"+name, func() (bool, error) {
			_, err := c.Kube.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return err == nil, err
		}); err != nil {
			return err
		}
		if ns != app {
			namespaces[ns] = true
		}
	}
	return c.restartAndWait(ctx, slices.Sorted(maps.Keys(namespaces)))
}

// refreshReaders makes every ExternalSecret read OpenBao again and restarts the workloads of their namespaces (OpenBao
// and Argo CD aside), so every reader runs on the restored secrets; nothing else restarts (Ceph's mons and OSDs at once
// would stall every volume)
func (c *Client) refreshReaders(ctx context.Context) error {
	list, err := c.Dyn.Resource(ExternalSecrets).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list externalsecrets: %w", err)
	}
	names := map[string][]string{}
	for _, es := range list.Items {
		names[es.GetNamespace()] = append(names[es.GetNamespace()], es.GetName())
	}
	// restore waits for Argo CD at its end, not for each refresh: an ExternalSecret that cannot refresh must not stop it
	for ns, n := range names {
		if err := c.workload().ForceSync(ctx, ns, n); err != nil {
			return err
		}
	}
	readers := map[string]bool{}
	for ns := range names {
		readers[ns] = true
	}
	delete(readers, "openbao")
	delete(readers, "argocd")
	namespaces := make([]string, 0, len(readers))
	for ns := range readers {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	return c.restartAndWait(ctx, namespaces)
}

// waitApplications waits for every Application to be Synced and Healthy, naming the ones that are not
func (c *Client) waitApplications(ctx context.Context) error {
	var bad []string
	err := c.until(ctx, "every Application Synced and Healthy", func() (bool, error) {
		list, err := c.Dyn.Resource(Applications).Namespace("argocd").List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, nil
		}
		bad = bad[:0]
		for _, a := range list.Items {
			sync, _, _ := unstructured.NestedString(a.Object, "status", "sync", "status")
			health, _, _ := unstructured.NestedString(a.Object, "status", "health", "status")
			if sync != "Synced" || health != "Healthy" {
				bad = append(bad, fmt.Sprintf("%s %s/%s", a.GetName(), sync, health))
			}
		}
		return len(bad) == 0, nil
	})
	if err != nil && len(bad) > 0 {
		return fmt.Errorf("%w: %s", err, strings.Join(bad, ", "))
	}
	return err
}

// waitHealthy waits for the Application to be Synced and Healthy, restarting the app's pods that crash-looped while
// their database was away (Forgejo's init container), once each, then for the app's pods themselves: Argo CD's
// health lags the scale-up
func (c *Client) waitHealthy(ctx context.Context, app string) error {
	restarted := map[string]bool{}
	err := c.until(ctx, "Application "+app+" Synced and Healthy", func() (bool, error) {
		c.workload().RestartCrashLooping(ctx, app, restarted)
		a, err := c.Dyn.Resource(Applications).Namespace("argocd").Get(ctx, app, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		sync, _, _ := unstructured.NestedString(a.Object, "status", "sync", "status")
		health, _, _ := unstructured.NestedString(a.Object, "status", "health", "status")
		return sync == "Synced" && health == "Healthy", nil
	})
	if err != nil {
		return err
	}
	return c.waitWorkloads(ctx, app)
}

// until polls done every c.Poll for up to c.Wait; an error from done ends it
func (c *Client) until(ctx context.Context, what string, done func() (bool, error)) error {
	return c.workload().Until(ctx, what, done)
}

// workload is the shared reader handling on this client's clients and timings
func (c *Client) workload() *workload.Client {
	return &workload.Client{Kube: c.Kube, Dyn: c.Dyn, Poll: c.Poll, Wait: c.Wait}
}

// restartAndWait is kubectl rollout restart for every Deployment and StatefulSet in the namespaces, all first, then
// waits for those rollouts: Argo CD's health lags a restart (it still says Healthy right after), and a Recreate
// workload (Forgejo) serves nothing meanwhile
func (c *Client) restartAndWait(ctx context.Context, namespaces []string) error {
	refs := map[string][]workload.Ref{}
	for _, ns := range namespaces {
		r, err := c.workload().Workloads(ctx, ns)
		if err != nil {
			return err
		}
		if err := c.workload().Restart(ctx, ns, r); err != nil {
			return err
		}
		refs[ns] = r
	}
	for _, ns := range namespaces {
		if err := c.workload().WaitReady(ctx, ns, refs[ns]); err != nil {
			return err
		}
	}
	return nil
}

// waitWorkloads waits for the app's Deployments and StatefulSets to be ready, restarting crash-looping pods once each
// like waitHealthy
func (c *Client) waitWorkloads(ctx context.Context, app string) error {
	return c.workload().WaitNamespaceReady(ctx, app)
}

// agentIDs names, by namespace, the StatefulSet whose claims hold only ids its pods registered with the app: they are
// not backed up (homelab/backup none), so after the app's data is restored they name what the restored data may not
// know (Woodpecker's agents: "AgentID not found in database") and start again on new claims
var agentIDs = map[string]string{"woodpecker": "woodpecker-agent"}

func (c *Client) resetAgentIDs(ctx context.Context, app string) error {
	name, ok := agentIDs[app]
	if !ok {
		return nil
	}
	sts, err := c.Kube.AppsV1().StatefulSets(app).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var claims []string
	for i := range ptr.Deref(sts.Spec.Replicas, 1) {
		for _, t := range sts.Spec.VolumeClaimTemplates {
			claims = append(claims, fmt.Sprintf("%s-%s-%d", t.Name, name, i))
		}
	}
	return c.workload().ResetClaims(ctx, app, claims)
}
