package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/repo"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// labE runs lab like lab() but returns its error, for the refusals
func labE(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", append([]string{"run", "./cmd/lab"}, args...)...)
	cmd.Dir = filepath.Join(repo.Root(), "cli")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runCronJob starts a Job from the CronJob and waits for it to succeed
func runCronJob(ctx context.Context, namespace, cronJob string) {
	GinkgoHelper()
	name := fmt.Sprintf("%s-test-%d", cronJob, time.Now().Unix())
	out, err := kube.Kubectl(ctx, namespace, "create", "job", name, "--from=cronjob/"+cronJob)
	Expect(err).NotTo(HaveOccurred(), out)
	out, err = kube.Kubectl(ctx, namespace, "wait", "--for=condition=Complete", "--timeout=5m", "job/"+name)
	Expect(err).NotTo(HaveOccurred(), out)
}

func waitBackup(ctx context.Context, name string) {
	GinkgoHelper()
	Eventually(ctx, func(g Gomega) {
		var b struct {
			Status struct{ Phase string } `json:"status"`
		}
		kubectlJSON(ctx, &b, "-n", "velero", "get", "backups.velero.io", name)
		// A failed backup stops the wait at once
		Expect([]string{"Failed", "PartiallyFailed", "FailedValidation"}).NotTo(ContainElement(b.Status.Phase), "backup %s", name)
		g.Expect(b.Status.Phase).To(Equal("Completed"))
	}).WithTimeout(20*time.Minute).WithPolling(10*time.Second).Should(Succeed(), "backup %s did not complete", name)
}

func allSyncedHealthy(ctx context.Context) []string {
	GinkgoHelper()
	var apps struct {
		Items []struct {
			Metadata struct{ Name string } `json:"metadata"`
			Status   struct {
				Sync   struct{ Status string } `json:"sync"`
				Health struct{ Status string } `json:"health"`
			} `json:"status"`
		} `json:"items"`
	}
	kubectlJSON(ctx, &apps, "-n", "argocd", "get", "applications.argoproj.io")
	var bad []string
	for _, a := range apps.Items {
		if a.Status.Sync.Status != "Synced" || a.Status.Health.Status != "Healthy" {
			bad = append(bad, fmt.Sprintf("%s %s/%s", a.Metadata.Name, a.Status.Sync.Status, a.Status.Health.Status))
		}
	}
	return bad
}

// BackupRestore backs up the sandbox off-site and locally, loses a repository, its issue and an OpenBao key, and gets
// them back with lab backup restore (docs/superpowers/specs/2026-10-22-backups-design.md, decision 9). It runs alone:
// a restore rolls back all of OpenBao and restarts every reader.
var _ = Describe("BackupRestore", Ordered, Serial, Label("sandbox", "testbed", "backup-restore", "alone"), func() {
	env := []string{"--env", labEnv}
	// What the steps share: the probe repository, its first commit and issue, the key's value and the off-site backup
	var owner, name, commit, marker, backup string
	var issue struct{ Number int }

	BeforeAll(func() {
		DeferCleanup(func(ctx SpecContext) { _, _ = labE(ctx, append([]string{"backup", "disable"}, env...)...) }, NodeTimeout(10*time.Minute))
	})
	// The data the restores bring back is removed once every step ran
	AfterAll(func(ctx SpecContext) {
		if marker != "" {
			baoPut(ctx, "smoke/backup-marker", "", true)
		}
		if owner != "" {
			forgejoAs(ctx, "", "DELETE", "/repos/"+owner+"/"+name, nil, nil)
		}
	}, NodeTimeout(5*time.Minute))

	It("a fresh cluster is read-only", func(ctx SpecContext) {
		mode, err := kube.Kubectl(ctx, "velero", "get", "backupstoragelocations.velero.io", "default", "-o", "jsonpath={.spec.accessMode}")
		Expect(err).NotTo(HaveOccurred())
		Expect(mode).To(Equal("ReadOnly"))
		paused, err := kube.Kubectl(ctx, "velero", "get", "schedules.velero.io", "daily", "-o", "jsonpath={.spec.paused}")
		Expect(err).NotTo(HaveOccurred())
		Expect(paused).To(Equal("true"))
		runCronJob(ctx, "velero", "prune")
		logs, err := kube.Kubectl(ctx, "velero", "logs", "-l", "job-name", "--tail=-1", "--prefix")
		Expect(err).NotTo(HaveOccurred())
		Expect(logs).To(ContainSubstring("no backup pruned"))
	}, SpecTimeout(10*time.Minute))

	It("backs up a repository, its issue and a key, then loses them", func(ctx SpecContext) {
		name = fmt.Sprintf("backup-probe-%d", time.Now().Unix())
		var created struct {
			FullName string `json:"full_name"`
		}
		Expect(forgejoAs(ctx, "", "POST", "/user/repos", map[string]any{"name": name, "auto_init": true, "private": true}, &created)).To(Equal(201))
		owner = strings.Split(created.FullName, "/")[0]
		var commits []struct{ SHA string }
		Expect(forgejoAs(ctx, "", "GET", "/repos/"+owner+"/"+name+"/commits", nil, &commits)).To(Equal(200))
		Expect(commits).NotTo(BeEmpty())
		commit = commits[0].SHA
		Expect(forgejoAs(ctx, "", "POST", "/repos/"+owner+"/"+name+"/issues", map[string]any{"title": "kept by the backup"}, &issue)).To(Equal(201))
		marker = fmt.Sprintf("marker-%d", time.Now().UnixNano())
		baoPut(ctx, "smoke/backup-marker", marker, false)

		// The off-site copy, as the daily schedule takes it (OpenBao's snapshot first), and one round of local snapshots
		lab(ctx, append([]string{"backup", "enable", "--yes"}, env...)...)
		runCronJob(ctx, "openbao", "openbao-snapshot")
		var schedule struct {
			Spec struct {
				Template map[string]any `json:"template"`
			} `json:"spec"`
		}
		kubectlJSON(ctx, &schedule, "-n", "velero", "get", "schedules.velero.io", "daily")
		backup = fmt.Sprintf("daily-test-%d", time.Now().Unix())
		manifest, err := json.Marshal(map[string]any{
			"apiVersion": "velero.io/v1", "kind": "Backup",
			"metadata": map[string]any{"name": backup, "namespace": "velero", "labels": map[string]string{"velero.io/schedule-name": "daily"}},
			"spec":     schedule.Spec.Template,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(kube.Apply(ctx, "velero", string(manifest))).To(Succeed())
		waitBackup(ctx, backup)
		// Snapshots are one per claim and hour (snapshots.sh skips an hour's existing one): this hour's, taken before the
		// spec's data, would be what the local restore reads
		for _, claim := range []string{"forgejo-data", "forgejo-db-1"} {
			_, _ = kube.Kubectl(ctx, "forgejo", "delete", "volumesnapshots.snapshot.storage.k8s.io",
				claim+"-"+time.Now().UTC().Format("2006010215"), "--ignore-not-found")
		}
		// This run's snapshots, not an older hour's (already ready): csi-hostpath copies a volume with tar while it is
		// written, so changing the data before its snapshot is ready puts the change in it (the restore then brought the
		// repository back deleted)
		started := time.Now().Truncate(time.Second)
		runCronJob(ctx, "velero", "snapshots")
		for _, claim := range []string{"forgejo-data", "forgejo-db-1"} {
			Eventually(ctx, func() bool {
				out, err := kube.Kubectl(ctx, "forgejo", "get", "volumesnapshots.snapshot.storage.k8s.io",
					"-l", "homelab/snapshot=hourly,homelab/pvc="+claim, "-o", `jsonpath={range .items[*]}{.metadata.creationTimestamp} {.status.readyToUse}{"\n"}{end}`)
				if err != nil {
					return false
				}
				for _, line := range strings.Split(out, "\n") {
					created, ready, _ := strings.Cut(line, " ")
					at, err := time.Parse(time.RFC3339, created)
					if err == nil && !at.Before(started) && ready == "true" {
						return true
					}
				}
				return false
			}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "no ready snapshot of %s from this run", claim)
		}

		// Lost: the repository with its issue, and the key
		Expect(forgejoAs(ctx, "", "DELETE", "/repos/"+owner+"/"+name, nil, nil)).To(Equal(204))
		baoPut(ctx, "smoke/backup-marker", "", true)
	}, SpecTimeout(45*time.Minute))

	It("a whole-cluster restore brings them back", func(ctx SpecContext) {
		lab(ctx, append([]string{"backup", "disable", "--yes"}, env...)...)
		// Every backup here was taken after this cluster was created, of its own volumes: the default refuses them
		out, err := labE(ctx, append([]string{"backup", "restore", "--yes"}, env...)...)
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("--backup"))
		lab(ctx, append([]string{"backup", "restore", "--backup", backup, "--yes"}, env...)...)
		Expect(baoValue(ctx, "smoke/backup-marker", "value")).To(Equal(marker))
		var got []struct{ SHA string }
		Expect(forgejoRestored(ctx, "/repos/"+owner+"/"+name+"/commits", &got)).To(Equal(200))
		Expect(got).NotTo(BeEmpty())
		Expect(got[0].SHA).To(Equal(commit))
		var back struct{ Title string }
		Expect(forgejoAs(ctx, "", "GET", fmt.Sprintf("/repos/%s/%s/issues/%d", owner, name, issue.Number), nil, &back)).To(Equal(200))
		Expect(back.Title).To(Equal("kept by the backup"))
		var bad []string
		Eventually(ctx, func() bool { bad = allSyncedHealthy(ctx); return len(bad) == 0 }).
			WithTimeout(20*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), func() string { return fmt.Sprintf("after the restore: %v", bad) })
	}, SpecTimeout(45*time.Minute))

	It("one app from its local snapshot", func(ctx SpecContext) {
		Expect(forgejoAs(ctx, "", "DELETE", "/repos/"+owner+"/"+name, nil, nil)).To(Equal(204))
		lab(ctx, append([]string{"backup", "restore", "forgejo", "--yes"}, env...)...)
		var back struct{ Title string }
		Expect(forgejoRestored(ctx, fmt.Sprintf("/repos/%s/%s/issues/%d", owner, name, issue.Number), &back)).To(Equal(200))
		Expect(back.Title).To(Equal("kept by the backup"))
	}, SpecTimeout(20*time.Minute))

	It("push asks first", func(ctx SpecContext) {
		lab(ctx, append([]string{"backup", "enable", "--yes"}, env...)...)
		out, err := labE(ctx, append([]string{"backup", "push", "ntfy"}, env...)...)
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("--yes"))
		Expect(out).To(ContainSubstring("of 10 GB free"))
		out = lab(ctx, append([]string{"backup", "push", "ntfy", "--yes"}, env...)...)
		Expect(out).To(ContainSubstring("completed"))
	}, SpecTimeout(20*time.Minute))
})

// forgejoRestored reads Forgejo's API as its admin right after a restore: lab returns once the new pod is ready, and the
// Gateway learns its endpoint a moment later, answering 503 until then; after a whole-cluster restore Forgejo may still
// be starting (an absent repository is 404, never retried)
func forgejoRestored(ctx context.Context, path string, out any) int {
	GinkgoHelper()
	admin, password := secretValue(ctx, "forgejo", "forgejo-admin", "username"), secretValue(ctx, "forgejo", "forgejo-admin", "password")
	code := 0
	var err error
	Eventually(ctx, func() bool {
		code, err = getJSONE(ctx, "GET", "https://"+forgejoHost+"/api/v1"+path, nil, admin, password, nil, out)
		return err == nil && code != 503
	}).WithTimeout(5*time.Minute).WithPolling(2*time.Second).Should(BeTrue(), func() string {
		return fmt.Sprintf("Forgejo still unavailable through the Gateway: %d %v", code, err)
	})
	return code
}
