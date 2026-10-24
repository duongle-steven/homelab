package cli

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/backup"
	"github.com/duongle-steven/homelab/cli/internal/rotate"
	"github.com/duongle-steven/homelab/cli/internal/workload"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

type countingMethod struct{ calls *int }

func (m countingMethod) Prepare(context.Context, rotate.Entry) error { *m.calls++; return nil }
func (m countingMethod) Switch(context.Context, rotate.Entry) error  { *m.calls++; return nil }
func (m countingMethod) Verify(context.Context, rotate.Entry) error  { *m.calls++; return nil }
func (m countingMethod) Revoke(context.Context, rotate.Entry) error  { *m.calls++; return nil }

type memoryRecords map[string]rotate.Record

func (m memoryRecords) Get(_ context.Context, e rotate.Entry) (rotate.Record, error) {
	return m[e.Name], nil
}
func (m memoryRecords) Set(_ context.Context, e rotate.Entry, r rotate.Record) error {
	m[e.Name] = r
	return nil
}

// fakeRotation replaces newRotation: app passwords and generated secrets use a counting method, every workload has
// one replica
func fakeRotation(t *testing.T, records memoryRecords) *int {
	t.Helper()
	calls := new(int)
	previous := newRotation
	newRotation = func(_ *cobra.Command, entries []rotate.Entry) (*rotation, error) {
		return &rotation{
			Rotator: &rotate.Rotator{
				Entries: entries,
				Methods: map[rotate.Kind]rotate.Method{rotate.KindAppPassword: countingMethod{calls}, rotate.KindGenerated: countingMethod{calls}},
				Store:   records,
				Backups: func(context.Context) ([]backup.Run, error) {
					return []backup.Run{{Name: "daily-1", Schedule: "daily", Phase: "Completed", Started: time.Now().Add(-time.Hour)}}, nil
				},
				Idle: func(context.Context, string) (bool, error) { return true, nil },
				Out:  io.Discard,
				Poll: time.Millisecond, Wait: time.Second,
				Lock: &rotate.LeaseLocker{Kube: fake.NewClientset(), Namespace: "secrets-rotation"},
			},
			Replicas: func(context.Context, string, workload.Ref) (int32, error) { return 1, nil },
		}, nil
	}
	t.Cleanup(func() { newRotation = previous })
	return calls
}

func TestSecretsRotateUnknownNameBeforeCluster(t *testing.T) {
	previous := newRotation
	newRotation = func(*cobra.Command, []rotate.Entry) (*rotation, error) {
		t.Fatal("a mistyped name reached the cluster")
		return nil, nil
	}
	t.Cleanup(func() { newRotation = previous })
	_, err := runLab(t, "", false, "secrets", "rotate", "grafana.admn")
	if err == nil || !strings.Contains(err.Error(), "grafana.admin") {
		t.Fatalf("error = %v", err)
	}
	if _, err := runLab(t, "", false, "secrets", "rotate", "telegram.bot-token"); err == nil || !strings.Contains(err.Error(), "not rotated yet") {
		t.Fatalf("a pending entry: error = %v", err)
	}
	if _, err := runLab(t, "", false, "secrets", "rotate", "--all", "--skip-backup-check="); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("an empty reason: error = %v", err)
	}
}

func TestSecretsRotateAsksFirst(t *testing.T) {
	calls := fakeRotation(t, memoryRecords{})
	out, err := runLab(t, "", false, "secrets", "rotate", "grafana.admin")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(out, "changes it in grafana first, then in OpenBao; no restart") || strings.Contains(out, "restarts") {
		t.Fatalf("plan = %q", out)
	}
	out, _ = runLab(t, "", false, "secrets", "rotate", "ntfy.alerts-password")
	if !strings.Contains(out, "restarts Deployment ntfy/ntfy (one replica: it stops briefly)") {
		t.Fatalf("plan = %q", out)
	}
	if *calls != 0 {
		t.Fatalf("%d method calls without confirmation", *calls)
	}
	out, err = runLab(t, "y\n", true, "secrets", "rotate", "grafana.admin")
	if err != nil || *calls != 4 || !strings.Contains(out, "grafana.admin: rotated") {
		t.Fatalf("after yes: %v, %d calls, %q", err, *calls, out)
	}
}

func TestSecretsRotateScheduledDoesNotAsk(t *testing.T) {
	records := memoryRecords{}
	fakeRotation(t, records)
	entries, err := rotate.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		records[e.Name] = rotate.Record{Phase: rotate.PhaseDone, At: time.Now()}
	}
	out, err := runLab(t, "", false, "secrets", "rotate", "--scheduled")
	if err != nil || !strings.Contains(out, "Nothing is due.") {
		t.Fatalf("nothing due: %v, %q", err, out)
	}
	records["ntfy.alerts-token"] = rotate.Record{Phase: rotate.PhaseDone, At: time.Now().Add(-120 * 24 * time.Hour)}
	out, err = runLab(t, "", false, "secrets", "rotate", "--scheduled")
	if err == nil || strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), "overdue") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(out, "ntfy.alerts-token: skipped: no method yet for kind ntfy-overlap (overdue)") {
		t.Fatalf("out = %q", out)
	}
}

func TestSecretsRotateScriptAnswersWithSteps(t *testing.T) {
	fakeRotation(t, memoryRecords{})
	out, err := runLab(t, "", false, "secrets", "rotate", "state.passphrase", "--yes")
	if err != nil || !strings.Contains(out, "scripts/rotate-state-passphrase") {
		t.Fatalf("script: %v, %q", err, out)
	}
	if _, err := runLab(t, "", false, "secrets", "rotate", "openbao.unseal-key", "--yes"); err == nil {
		t.Fatal("a never entry named alone exited 0")
	}
}

func TestSecretsList(t *testing.T) {
	at := time.Date(2026, 9, 1, 3, 0, 0, 0, time.Local)
	fakeRotation(t, memoryRecords{
		"ntfy.alerts-token": {Phase: rotate.PhaseDone, At: at, By: "rotation-job"},
		"grafana.admin":     {Phase: rotate.PhaseSwitched},
	})
	out, err := runLab(t, "", false, "secrets", "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if name, _, ok := strings.Cut(line, " "); ok {
			lines[name] = line
		}
	}
	for name, want := range map[string][]string{
		"ntfy.alerts-token":     {"ntfy-overlap", "90d", "2026-09-01 03:00:00 by rotation-job", "2026-11-30"},
		"grafana.admin":         {"app-password", "command", "never (interrupted after switched"},
		"openbao.unseal-key":    {"never", "-"},
		"woodpecker.agent":      {"90d", "now"},
		"kanidm-client.grafana": {"kaniop-client", "2"},
	} {
		for _, w := range want {
			if !strings.Contains(lines[name], w) {
				t.Errorf("%s: %q lacks %q", name, lines[name], w)
			}
		}
	}
}

// make infra env=production warns about OpenTofu's due secrets: only those, as one line each with the command
func TestSecretsListDueOpenTofu(t *testing.T) {
	recent := time.Now().Add(-24 * time.Hour)
	fakeRotation(t, memoryRecords{
		"cloudflare.cert-manager": {Phase: rotate.PhaseDone, At: time.Now().Add(-100 * 24 * time.Hour), By: "steven"},
		"cloudflare.external-dns": {Phase: rotate.PhaseDone, At: recent, By: "steven"},
		"backup.r2-token":         {Phase: rotate.PhaseDone, At: recent, By: "steven"},
		"ntfy.alerts-token":       {Phase: rotate.PhaseDone, At: time.Now().Add(-200 * 24 * time.Hour), By: "rotation-job"},
	})
	out, err := runLab(t, "", false, "secrets", "list", "--due", "--kind", "opentofu")
	if err != nil {
		t.Fatal(err)
	}
	want := "due: cloudflare.cert-manager (since " + time.Now().Add(-10*24*time.Hour).Local().Format(time.DateOnly) + "): lab secrets rotate cloudflare.cert-manager\n"
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
}

func TestIdleWoodpecker(t *testing.T) {
	ctx := context.Background()
	step := func(name string, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "woodpecker", Labels: map[string]string{"woodpecker-ci.org/step": "build"}},
			Status:     corev1.PodStatus{Phase: phase},
		}
	}
	server := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "woodpecker-server-0", Namespace: "woodpecker"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if ok, err := idle(ctx, fake.NewClientset(server, step("done", corev1.PodSucceeded)), nil, "woodpecker"); err != nil || !ok {
		t.Fatalf("no running step: %v, %v", ok, err)
	}
	if ok, err := idle(ctx, fake.NewClientset(server, step("build", corev1.PodRunning)), nil, "woodpecker"); err != nil || ok {
		t.Fatalf("a running step: %v, %v", ok, err)
	}
	if _, err := idle(ctx, fake.NewClientset(), nil, "nothing"); err == nil {
		t.Fatal("an unknown idle check reported idle")
	}
}

// redisExec answers paperless's queue count
type redisExec struct {
	out     string
	command *[]string
}

func (r redisExec) Exec(_ context.Context, namespace string, w workload.Ref, container string, command []string, _ io.Reader, stdout io.Writer) error {
	*r.command = append([]string{namespace, w.Name, container}, command...)
	_, err := io.WriteString(stdout, r.out)
	return err
}

func TestIdlePaperless(t *testing.T) {
	ctx := context.Background()
	var command []string
	if ok, err := idle(ctx, fake.NewClientset(), redisExec{out: "0\n", command: &command}, "paperless"); err != nil || !ok {
		t.Fatalf("an empty queue: %v, %v", ok, err)
	}
	if !slices.Equal(command[:4], []string{"paperless", "paperless", "redis", "redis-cli"}) {
		t.Fatalf("ran %q", command)
	}
	if ok, err := idle(ctx, fake.NewClientset(), redisExec{out: "2\n", command: &command}, "paperless"); err != nil || ok {
		t.Fatalf("two queued tasks: %v, %v", ok, err)
	}
	if _, err := idle(ctx, fake.NewClientset(), redisExec{out: "ERR\n", command: &command}, "paperless"); err == nil {
		t.Fatal("a redis error reported idle")
	}
}

func TestReplicas(t *testing.T) {
	kube := fake.NewClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "grafana", Namespace: "grafana"}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](2)}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "zot", Namespace: "zot"}},
	)
	if n, err := replicas(context.Background(), kube, "grafana", workload.Ref{Kind: "Deployment", Name: "grafana"}); err != nil || n != 2 {
		t.Fatalf("Deployment: %d, %v", n, err)
	}
	if n, err := replicas(context.Background(), kube, "zot", workload.Ref{Kind: "StatefulSet", Name: "zot"}); err != nil || n != 1 {
		t.Fatalf("StatefulSet without replicas: %d, %v", n, err)
	}
}
