package rotate

import (
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/backup"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

var now = time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

// fakeMethod records "<entry> <phase>" for every call and fails the phases in fail
type fakeMethod struct {
	calls *[]string
	fail  map[string]error
}

func (m fakeMethod) call(e Entry, phase string) error {
	*m.calls = append(*m.calls, e.Name+" "+phase)
	return m.fail[phase]
}
func (m fakeMethod) Prepare(_ context.Context, e Entry) error { return m.call(e, "prepare") }
func (m fakeMethod) Switch(_ context.Context, e Entry) error  { return m.call(e, "switch") }
func (m fakeMethod) Verify(_ context.Context, e Entry) error  { return m.call(e, "verify") }
func (m fakeMethod) Revoke(_ context.Context, e Entry) error  { return m.call(e, "revoke") }

// fakeRecords keeps records by entry name and every phase written, in order
type fakeRecords struct {
	records map[string]Record
	sets    []string
}

func (f *fakeRecords) Get(_ context.Context, e Entry) (Record, error) { return f.records[e.Name], nil }
func (f *fakeRecords) Set(ctx context.Context, e Entry, r Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.records[e.Name] = r
	f.sets = append(f.sets, e.Name+" "+r.Phase)
	return nil
}

func recentBackup() []backup.Run {
	return []backup.Run{{Name: "daily-1", Schedule: "daily", Phase: "Completed", Started: now.Add(-3 * time.Hour)}}
}

// rotator builds a Rotator over entries whose kinds all use one fake method that fails the phases in fail
func rotator(entries []Entry, fail map[string]error) (*Rotator, *[]string, *fakeRecords) {
	calls := &[]string{}
	method := fakeMethod{calls: calls, fail: fail}
	records := &fakeRecords{records: map[string]Record{}}
	r := &Rotator{
		Entries: entries,
		Methods: map[Kind]Method{},
		Store:   records,
		Backups: func(context.Context) ([]backup.Run, error) { return recentBackup(), nil },
		Idle:    func(context.Context, string) (bool, error) { return true, nil },
		Out:     io.Discard,
		Poll:    time.Millisecond,
		Wait:    50 * time.Millisecond,
		Lock:    &LeaseLocker{Kube: fake.NewClientset(), Namespace: "secrets-rotation"},
	}
	for _, k := range kinds {
		r.Methods[k] = method
	}
	return r, calls, records
}

// cancelAfterPrepare is a person pressing Ctrl-C just after the new value was written
type cancelAfterPrepare struct {
	fakeMethod
	cancel context.CancelFunc
}

func (m cancelAfterPrepare) Prepare(ctx context.Context, e Entry) error {
	err := m.fakeMethod.Prepare(ctx, e)
	m.cancel()
	return err
}
func (m cancelAfterPrepare) Switch(ctx context.Context, e Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.fakeMethod.Switch(ctx, e)
}

func generated(name string) Entry {
	return Entry{Name: name, Kind: KindGenerated, Class: 1, Paths: []string{name + "/generated/x"}}
}

func TestRotateRunsPhasesInOrder(t *testing.T) {
	r, calls, records := rotator([]Entry{generated("grafana.admin")}, nil)
	o := Options{Yes: true, Now: now, By: "steven@workstation"}
	plan, err := r.Plan(context.Background(), []string{"grafana.admin"}, o)
	if err != nil {
		t.Fatal(err)
	}
	results, err := r.Run(context.Background(), plan, o)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"grafana.admin prepare", "grafana.admin switch", "grafana.admin verify", "grafana.admin revoke"}
	if !slices.Equal(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
	wantSets := []string{"grafana.admin preparing", "grafana.admin prepared", "grafana.admin switched", "grafana.admin verified", "grafana.admin done"}
	if !slices.Equal(records.sets, wantSets) {
		t.Fatalf("records = %v, want %v", records.sets, wantSets)
	}
	got := records.records["grafana.admin"]
	if got.Phase != PhaseDone || !got.At.Equal(now) || got.By != "steven@workstation" || got.Method != "generated" || got.Reason != "" {
		t.Fatalf("record = %+v", got)
	}
	if len(results) != 1 || results[0].Outcome != "rotated" || results[0].Err != nil {
		t.Fatalf("results = %+v", results)
	}
}

func TestRotateResumesAfterInterruption(t *testing.T) {
	ctx := context.Background()
	o := Options{Yes: true, Now: now}
	t.Run("recorded switched", func(t *testing.T) {
		r, calls, records := rotator([]Entry{generated("grafana.admin")}, nil)
		records.records["grafana.admin"] = Record{Phase: PhaseSwitched, At: now.Add(-100 * day)}
		if _, err := r.Run(ctx, r.Entries, o); err != nil {
			t.Fatal(err)
		}
		want := []string{"grafana.admin verify", "grafana.admin revoke"}
		if !slices.Equal(*calls, want) {
			t.Fatalf("calls = %v, want %v: a resumed rotation must not prepare a third value", *calls, want)
		}
	})
	t.Run("interrupted, then run again", func(t *testing.T) {
		r, calls, records := rotator([]Entry{generated("grafana.admin")}, map[string]error{"switch": context.Canceled})
		if _, err := r.Run(ctx, r.Entries, o); err == nil {
			t.Fatal("an interrupted run reported success")
		}
		if got := records.records["grafana.admin"].Phase; got != PhasePrepared {
			t.Fatalf("phase after the interruption = %q, want prepared", got)
		}
		*calls = nil
		r.Methods[KindGenerated] = fakeMethod{calls: calls}
		if _, err := r.Run(ctx, r.Entries, o); err != nil {
			t.Fatal(err)
		}
		want := []string{"grafana.admin switch", "grafana.admin verify", "grafana.admin revoke"}
		if !slices.Equal(*calls, want) {
			t.Fatalf("calls = %v, want %v", *calls, want)
		}
	})
	t.Run("Ctrl-C right after prepare", func(t *testing.T) {
		r, calls, records := rotator([]Entry{generated("grafana.admin")}, nil)
		ctx, cancel := context.WithCancel(ctx)
		r.Methods[KindGenerated] = cancelAfterPrepare{fakeMethod{calls: calls}, cancel}
		if _, err := r.Run(ctx, r.Entries, o); err == nil {
			t.Fatal("an interrupted run reported success")
		}
		if got := records.records["grafana.admin"].Phase; got != PhasePrepared {
			t.Fatalf("phase = %q: the new value was written but not recorded, so a rerun would prepare a third", got)
		}
	})
	t.Run("recorded preparing", func(t *testing.T) {
		r, calls, records := rotator([]Entry{generated("grafana.admin")}, nil)
		records.records["grafana.admin"] = Record{Phase: PhasePreparing}
		if _, err := r.Run(ctx, r.Entries, o); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 4 || (*calls)[0] != "grafana.admin prepare" {
			t.Fatalf("calls = %v: Prepare must run again and find the value it may have written", *calls)
		}
	})
	t.Run("keeps the earlier backup-check reason", func(t *testing.T) {
		r, _, records := rotator([]Entry{generated("grafana.admin")}, nil)
		records.records["grafana.admin"] = Record{Phase: PhaseSwitched, Reason: "leaked token"}
		if _, err := r.Run(ctx, r.Entries, o); err != nil {
			t.Fatal(err)
		}
		if got := records.records["grafana.admin"].Reason; got != "leaked token" {
			t.Fatalf("reason = %q", got)
		}
	})
	t.Run("done starts a new rotation", func(t *testing.T) {
		r, calls, records := rotator([]Entry{generated("grafana.admin")}, nil)
		records.records["grafana.admin"] = Record{Phase: PhaseDone, At: now.Add(-100 * day)}
		if _, err := r.Run(ctx, r.Entries, o); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 4 {
			t.Fatalf("calls = %v", *calls)
		}
	})
}

func TestRotateKeepsOldOnReaderFailure(t *testing.T) {
	entries := []Entry{
		{Name: "ntfy.alerts-token", Kind: KindNtfyOverlap, Paths: []string{"ntfy/generated/alerts-token"}},
		generated("grafana.admin"),
	}
	r, calls, records := rotator(entries, map[string]error{"verify": errors.New("deployment ntfy/ntfy not ready")})
	results, err := r.Run(context.Background(), entries, Options{Yes: true, All: true, Now: now})
	if err == nil {
		t.Fatal("Run succeeded although a reader failed")
	}
	if slices.Contains(*calls, "ntfy.alerts-token revoke") {
		t.Fatalf("calls = %v: the old value was revoked although a reader failed", *calls)
	}
	if slices.ContainsFunc(*calls, func(c string) bool { return strings.HasPrefix(c, "grafana.admin") }) {
		t.Fatalf("calls = %v: the run went on after a failure", *calls)
	}
	if got := records.records["ntfy.alerts-token"].Phase; got != PhaseSwitched {
		t.Fatalf("phase = %q, want switched (so a rerun verifies again)", got)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want one per entry", results)
	}
	if !strings.Contains(results[0].Outcome, "failed") || !strings.Contains(results[0].Outcome, "verify") ||
		!strings.Contains(results[0].Err.Error(), "ntfy/ntfy") {
		t.Fatalf("result = %+v, want the phase and the reader named", results[0])
	}
	if !strings.Contains(results[1].Outcome, "not started") {
		t.Fatalf("result = %+v, want it reported as left", results[1])
	}
}

func TestRotateSkipsManualWithoutTerminal(t *testing.T) {
	entries := []Entry{
		{Name: "renovate.github-app-key", Kind: KindManual, Steps: "Generate a new private key"},
		generated("grafana.admin"),
	}
	r, calls, _ := rotator(entries, nil)
	o := Options{All: true, Yes: true, Interactive: false, Now: now}
	plan, err := r.Plan(context.Background(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var results []Result
	go func() {
		defer close(done)
		results, err = r.Run(context.Background(), plan, o)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run hung on a manual entry")
	}
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(*calls, func(c string) bool { return strings.HasPrefix(c, "renovate") }) {
		t.Fatalf("calls = %v: a manual entry ran without a terminal", *calls)
	}
	byName := map[string]Result{}
	for _, res := range results {
		byName[res.Name] = res
	}
	if got := byName["renovate.github-app-key"].Outcome; !strings.HasPrefix(got, "skipped") || !strings.Contains(got, "terminal") {
		t.Fatalf("manual outcome = %q", got)
	}
	if got := byName["grafana.admin"].Outcome; got != "rotated" {
		t.Fatalf("grafana.admin outcome = %q", got)
	}
}

func TestRotateUnknownName(t *testing.T) {
	r, calls, records := rotator([]Entry{generated("grafana.admin"), generated("paperless.admin"), generated("forgejo.admin")}, nil)
	_, err := r.Plan(context.Background(), []string{"grafana.admn"}, Options{Now: now})
	if err == nil || !strings.Contains(err.Error(), `"grafana.admn"`) || !strings.Contains(err.Error(), "grafana.admin") {
		t.Fatalf("Plan error = %v, want the name and the closest one", err)
	}
	if len(*calls) != 0 || len(records.sets) != 0 {
		t.Fatalf("something ran: %v %v", *calls, records.sets)
	}
}

func TestRotatePendingRefused(t *testing.T) {
	pending := Entry{Name: "woodpecker.agent", Kind: KindWoodpeckerAgent, Pending: "Task 8 (per-agent tokens)"}
	r, calls, _ := rotator([]Entry{pending, generated("grafana.admin")}, nil)
	if _, err := r.Plan(context.Background(), []string{"woodpecker.agent"}, Options{Now: now}); err == nil ||
		!strings.Contains(err.Error(), "Task 8 (per-agent tokens)") {
		t.Fatalf("Plan error = %v, want the pending reason", err)
	}
	o := Options{All: true, Yes: true, Now: now}
	plan, err := r.Plan(context.Background(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	results, err := r.Run(context.Background(), plan, o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(results[0].Outcome+results[1].Outcome, "Task 8 (per-agent tokens)") {
		t.Fatalf("results = %+v", results)
	}
	if slices.ContainsFunc(*calls, func(c string) bool { return strings.HasPrefix(c, "woodpecker") }) {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestRotateNoMethod(t *testing.T) {
	entries := []Entry{{Name: "kanidm-client.grafana", Kind: KindKaniopClient}}
	r, calls, records := rotator(entries, nil)
	delete(r.Methods, KindKaniopClient)
	results, err := r.Run(context.Background(), entries, Options{Yes: true, Now: now})
	if err == nil {
		t.Fatal("a named entry that was not rotated reported success")
	}
	if !strings.Contains(results[0].Outcome, "no method yet for kind kaniop-client") || len(*calls) != 0 || len(records.sets) != 0 {
		t.Fatalf("results = %+v, calls %v, records %v", results, *calls, records.sets)
	}
}

func TestRotateRefusesWithoutRecentBackup(t *testing.T) {
	stale := []backup.Run{
		{Name: "push-1", App: "paperless", Phase: "Completed", Started: now.Add(-time.Hour)},
		{Name: "daily-2", Schedule: "daily", Phase: "PartiallyFailed", Started: now.Add(-2 * time.Hour)},
		{Name: "daily-1", Schedule: "daily", Phase: "Completed", Started: now.Add(-25 * time.Hour)},
	}
	r, calls, records := rotator([]Entry{generated("grafana.admin")}, nil)
	r.Backups = func(context.Context) ([]backup.Run, error) { return stale, nil }
	_, err := r.Run(context.Background(), r.Entries, Options{Yes: true, Now: now})
	if err == nil || !strings.Contains(err.Error(), "24 hours") || !strings.Contains(err.Error(), "--skip-backup-check") {
		t.Fatalf("Run error = %v", err)
	}
	if len(*calls) != 0 || len(records.sets) != 0 {
		t.Fatalf("something ran: %v %v", *calls, records.sets)
	}
}

func TestRotateSkipBackupCheckRecordsReason(t *testing.T) {
	r, calls, records := rotator([]Entry{generated("grafana.admin")}, nil)
	r.Backups = func(context.Context) ([]backup.Run, error) { return nil, errors.New("must not be called") }
	if _, err := r.Run(context.Background(), r.Entries, Options{Yes: true, Now: now, SkipBackupCheck: "restore drill"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 4 {
		t.Fatalf("calls = %v", *calls)
	}
	if got := records.records["grafana.admin"].Reason; got != "restore drill" {
		t.Fatalf("recorded reason = %q", got)
	}
}

func TestRotateWaitsForIdle(t *testing.T) {
	entry := generated("registry.ci")
	entry.Idle = "woodpecker"
	t.Run("busy", func(t *testing.T) {
		r, calls, _ := rotator([]Entry{entry}, nil)
		var asked []string
		r.Idle = func(_ context.Context, what string) (bool, error) {
			asked = append(asked, what)
			return false, nil
		}
		results, err := r.Run(context.Background(), r.Entries, Options{Yes: true, Now: now})
		if err == nil || results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "Woodpecker") {
			t.Fatalf("Run = %+v, %v; want a timeout naming Woodpecker", results, err)
		}
		if len(asked) < 2 || asked[0] != "woodpecker" {
			t.Fatalf("Idle was asked %v", asked)
		}
		if len(*calls) != 0 {
			t.Fatalf("calls = %v: it rotated while Woodpecker was busy", *calls)
		}
	})
	t.Run("idle after a while", func(t *testing.T) {
		r, calls, _ := rotator([]Entry{entry}, nil)
		n := 0
		r.Idle = func(context.Context, string) (bool, error) { n++; return n > 2, nil }
		if _, err := r.Run(context.Background(), r.Entries, Options{Yes: true, Now: now}); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 4 {
			t.Fatalf("calls = %v", *calls)
		}
	})
}

func scheduledEntries() []Entry {
	period := 90 * day
	return []Entry{
		{Name: "due", Kind: KindGenerated, Period: period},
		{Name: "fresh", Kind: KindGenerated, Period: period},
		{Name: "never-rotated", Kind: KindGenerated, Period: period},
		{Name: "old-never-rotated", Kind: KindGenerated, Period: period},
		{Name: "by-command", Kind: KindGenerated},
		{Name: "interrupted", Kind: KindKaniopClient},
		{Name: "interrupted-periodic", Kind: KindGenerated, Period: period},
		{Name: "tofu", Kind: KindOpenTofu, Period: period},
		{Name: "manual", Kind: KindManual},
		{Name: "never", Kind: KindNever, Reason: "all of OpenBao"},
	}
}

func TestScheduledPicksDueOnly(t *testing.T) {
	r, _, records := rotator(scheduledEntries(), nil)
	records.records["due"] = Record{Phase: PhaseDone, At: now.Add(-91 * day)}
	records.records["fresh"] = Record{Phase: PhaseDone, At: now.Add(-10 * day)}
	records.records["never-rotated"] = Record{Since: now.Add(-10 * day)}
	records.records["old-never-rotated"] = Record{Since: now.Add(-91 * day)}
	records.records["interrupted"] = Record{Phase: PhaseSwitched, At: now.Add(-day)}
	records.records["interrupted-periodic"] = Record{Phase: PhaseSwitched, At: now.Add(-day)}
	records.records["tofu"] = Record{Phase: PhaseDone, At: now.Add(-200 * day)}
	plan, err := r.Plan(context.Background(), nil, Options{Scheduled: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range plan {
		names = append(names, e.Name)
	}
	if want := []string{"due", "old-never-rotated", "interrupted-periodic", "interrupted"}; !slices.Equal(names, want) {
		t.Fatalf("plan = %v, want %v", names, want)
	}

	// The periodic one is finished; the one by command only is reported for a person, and the run fails (it alerts)
	o := Options{Scheduled: true, Yes: true, Now: now}
	results, err := r.Run(context.Background(), plan, o)
	if err == nil || !strings.Contains(err.Error(), "interrupted") || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("Run error = %v", err)
	}
	byName := map[string]Result{}
	for _, res := range results {
		byName[res.Name] = res
	}
	if got := byName["interrupted-periodic"].Outcome; got != "rotated" {
		t.Fatalf("interrupted-periodic: %q", got)
	}
	if got := byName["interrupted"].Outcome; !strings.Contains(got, "lab secrets rotate interrupted") {
		t.Fatalf("interrupted: %q", got)
	}
	if got := records.records["interrupted"].Phase; got != PhaseSwitched {
		t.Fatalf("the scheduled run resumed a rotation by command: phase %q", got)
	}
}

func TestScheduledFailsWhenOverdue(t *testing.T) {
	ctx := context.Background()
	o := Options{Scheduled: true, Yes: true, Now: now}
	entries := []Entry{
		{Name: "late", Kind: KindWoodpeckerAgent, Period: 90 * day, Pending: "Task 8"},
		{Name: "just-due", Kind: KindWoodpeckerToken, Period: 90 * day, Pending: "Task 8"},
	}
	r, _, records := rotator(entries, nil)
	records.records["late"] = Record{Phase: PhaseDone, At: now.Add(-(90 + 15) * day)}
	records.records["just-due"] = Record{Phase: PhaseDone, At: now.Add(-(90 + 1) * day)}
	plan, err := r.Plan(ctx, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	results, err := r.Run(ctx, plan, o)
	if err == nil || !strings.Contains(err.Error(), "overdue") || !strings.Contains(err.Error(), "late") {
		t.Fatalf("Run error = %v, want late named overdue", err)
	}
	if strings.Contains(err.Error(), "just-due") {
		t.Fatalf("Run error = %v: just-due is within the margin", err)
	}
	for _, res := range results {
		if res.Overdue != (res.Name == "late") {
			t.Fatalf("result %+v", res)
		}
	}

	// Within the margin a skipped due entry does not fail the run
	r, _, records = rotator(entries[1:], nil)
	records.records["just-due"] = Record{Phase: PhaseDone, At: now.Add(-(90 + 1) * day)}
	plan, _ = r.Plan(ctx, nil, o)
	if _, err := r.Run(ctx, plan, o); err != nil {
		t.Fatalf("Run error = %v", err)
	}
}

func TestPlanOrderAll(t *testing.T) {
	entries := []Entry{
		{Name: "manual", Kind: KindManual},
		{Name: "tofu", Kind: KindOpenTofu},
		{Name: "never", Kind: KindNever, Reason: "x"},
		{Name: "registry", Kind: KindGenerated, Idle: "woodpecker"},
		{Name: "kanidm-client", Kind: KindKaniopClient},
		{Name: "forgejo-token", Kind: KindForgejoToken},
		{Name: "woodpecker-agent", Kind: KindWoodpeckerAgent, Idle: "woodpecker"},
		{Name: "kanidm-admin", Kind: KindKanidmAdmin},
		{Name: "ntfy", Kind: KindNtfyOverlap},
		{Name: "forgejo-db", Kind: KindCNPG},
		{Name: "generated", Kind: KindGenerated},
		{Name: "app-password", Kind: KindAppPassword},
		{Name: "automatic", Kind: KindAutomatic, Reason: "x"},
		{Name: "script", Kind: KindScript},
	}
	r, _, _ := rotator(entries, nil)
	plan, err := r.Plan(context.Background(), nil, Options{All: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range plan {
		names = append(names, e.Name)
	}
	want := []string{
		"ntfy", "generated", "app-password", // generated secrets and ntfy, in catalog order
		"forgejo-token", "forgejo-db", // Forgejo
		"kanidm-client", "kanidm-admin", // Kanidm
		"registry", "woodpecker-agent", // Woodpecker and zot
		"tofu", "manual", // OpenTofu, then manual
		"never", "automatic", "script", // listed and skipped
	}
	if !slices.Equal(names, want) {
		t.Fatalf("plan = %v\nwant   %v", names, want)
	}

	// The real catalog plans every entry once
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	r.Entries = catalog
	plan, err = r.Plan(context.Background(), nil, Options{All: true, Now: now})
	if err != nil || len(plan) != len(catalog) {
		t.Fatalf("Plan(catalog) = %d entries, %v; want %d", len(plan), err, len(catalog))
	}
}

func TestDue(t *testing.T) {
	e := Entry{Period: 90 * day}
	if due, ok := Due(e, Record{}); !ok || !due.IsZero() {
		t.Fatalf("no record at all: %v %v, want due at once", due, ok)
	}
	created := now.Add(-day)
	if due, ok := Due(e, Record{Since: created}); !ok || !due.Equal(created.Add(90*day)) {
		t.Fatalf("never rotated, created a day ago: %v %v", due, ok)
	}
	at := now.Add(-10 * day)
	if due, ok := Due(e, Record{Phase: PhaseDone, At: at}); !ok || !due.Equal(at.Add(90*day)) {
		t.Fatalf("Due = %v %v", due, ok)
	}
	if _, ok := Due(Entry{}, Record{}); ok {
		t.Fatal("an entry without a period has a due date")
	}
}

// fakeMetadata is an OpenBao KV v2 metadata store: custom metadata and created_time by path
type fakeMetadata struct {
	meta    map[string]map[string]string
	created map[string]time.Time
	writes  int
}

func (f *fakeMetadata) Created(_ context.Context, path string) (time.Time, error) {
	return f.created[path], nil
}
func (f *fakeMetadata) Metadata(_ context.Context, path string) (map[string]string, error) {
	return maps.Clone(f.meta[path]), nil
}
func (f *fakeMetadata) SetMetadata(_ context.Context, path string, set map[string]string) error {
	f.writes++
	if f.meta[path] == nil {
		f.meta[path] = map[string]string{}
	}
	for k, v := range set {
		if v == "" {
			delete(f.meta[path], k)
		} else {
			f.meta[path][k] = v
		}
	}
	return nil
}

func TestOpenBaoRecords(t *testing.T) {
	ctx := context.Background()
	createdAt := now.Add(-30 * day)
	store := &fakeMetadata{
		meta:    map[string]map[string]string{"grafana/generated/admin": {"managed-by": "external-secrets"}},
		created: map[string]time.Time{"grafana/generated/admin": createdAt},
	}
	records := OpenBaoRecords{Store: store, Now: func() time.Time { return now }}
	grafana := Entry{Name: "grafana.admin", Kind: KindGenerated, Period: 90 * day, Paths: []string{"grafana/generated/admin", "other/generated/admin"}}

	// Never rotated: due counts from the value's creation, and nothing is written
	if got, err := records.Get(ctx, grafana); err != nil || got != (Record{Since: createdAt}) {
		t.Fatalf("Get of a never rotated entry = %+v, %v", got, err)
	}
	if store.writes != 0 {
		t.Fatalf("%d writes for an entry whose value has a creation time", store.writes)
	}
	want := Record{Phase: PhaseDone, At: now, By: "steven@workstation", Method: "generated", Reason: "restore drill"}
	if err := records.Set(ctx, grafana, want); err != nil {
		t.Fatal(err)
	}
	// The record lives at secret/rotation/<name>, never on the value's path (ESO's PushSecret owns its metadata)
	if got := store.meta["grafana/generated/admin"]; len(got) != 1 {
		t.Fatalf("the value's metadata changed: %v", got)
	}
	if got := store.meta["rotation/grafana.admin"]; got["rotation-phase"] != "done" ||
		got["rotated-at"] != "2026-10-24T03:00:00Z" || got["rotation-reason"] != "restore drill" {
		t.Fatalf("record = %v", got)
	}
	if got, err := records.Get(ctx, grafana); err != nil || got != want {
		t.Fatalf("Get = %+v, %v; want %+v", got, err, want)
	}
	want.Reason = ""
	if err := records.Set(ctx, grafana, want); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.meta["rotation/grafana.admin"]["rotation-reason"]; ok {
		t.Fatal("an empty reason kept the previous one")
	}

	store.meta["rotation/grafana.admin"]["rotated-at"] = "yesterday"
	if _, err := records.Get(ctx, grafana); err == nil || !strings.Contains(err.Error(), "rotation/grafana.admin") {
		t.Fatalf("Get of a bad time = %v", err)
	}
}

func TestOpenBaoRecordsBaselineWrittenOnce(t *testing.T) {
	ctx := context.Background()
	store := &fakeMetadata{meta: map[string]map[string]string{}}
	clock := now
	records := OpenBaoRecords{Store: store, Now: func() time.Time { return clock }}
	// No OpenBao value path (OpenTofu's state holds it), so no creation time to count from
	tofu := Entry{Name: "cloudflare.cert-manager", Kind: KindOpenTofu, Period: 90 * day}
	got, err := records.Get(ctx, tofu)
	if err != nil || !got.Since.Equal(now) || store.meta["rotation/cloudflare.cert-manager"]["first-seen"] != "2026-10-24T03:00:00Z" {
		t.Fatalf("first Get = %+v, %v, metadata %v", got, err, store.meta)
	}
	clock = now.Add(10 * day)
	if got, err := records.Get(ctx, tofu); err != nil || !got.Since.Equal(now) || store.writes != 1 {
		t.Fatalf("second Get = %+v, %v, %d writes; want the first baseline, written once", got, err, store.writes)
	}
	// An entry without a period needs no baseline
	if _, err := records.Get(ctx, Entry{Name: "state.passphrase", Kind: KindScript}); err != nil || store.writes != 1 {
		t.Fatalf("Get of an entry without a period wrote: %v, %d", err, store.writes)
	}
}

func TestFreshSecretNeitherDueNorOverdue(t *testing.T) {
	fresh := Entry{Name: "fresh", Kind: KindWoodpeckerToken, Period: 90 * day, Pending: "Task 8"}
	r, _, records := rotator([]Entry{fresh}, nil)
	records.records["fresh"] = Record{Since: now.Add(-day)}
	if due, _ := Due(fresh, records.records["fresh"]); !due.After(now) {
		t.Fatalf("a secret created yesterday is due %v", due)
	}
	o := Options{Scheduled: true, Now: now}
	if plan, err := r.Plan(context.Background(), nil, o); err != nil || len(plan) != 0 {
		t.Fatalf("scheduled plan = %v, %v", plan, err)
	}
	o = Options{All: true, Yes: true, Now: now}
	results, err := r.Run(context.Background(), r.Entries, o)
	if err != nil || results[0].Overdue {
		t.Fatalf("Run = %+v, %v", results, err)
	}
}

func TestRotateNamedListedOnly(t *testing.T) {
	entries := []Entry{
		{Name: "state.passphrase", Kind: KindScript, Steps: "Run scripts/rotate-state-passphrase"},
		{Name: "renovate.token", Kind: KindAutomatic, Reason: "minted per run"},
		{Name: "openbao.unseal-key", Kind: KindNever, Reason: "all of OpenBao"},
	}
	r, _, _ := rotator(entries, nil)
	o := Options{Yes: true, Now: now}
	for _, e := range entries[:2] {
		results, err := r.Run(context.Background(), []Entry{e}, o)
		if err != nil || !strings.Contains(results[0].Outcome, e.Steps+e.Reason) {
			t.Fatalf("%s: %+v, %v; want its steps or reason and no error", e.Name, results, err)
		}
	}
	if _, err := r.Run(context.Background(), entries[2:], o); err == nil {
		t.Fatal("rotating a never entry by name succeeded")
	}
}

// blockingPrepare signals entered and waits for release inside Prepare
type blockingPrepare struct {
	fakeMethod
	entered, release chan struct{}
}

func (m blockingPrepare) Prepare(ctx context.Context, e Entry) error {
	close(m.entered)
	<-m.release
	return m.fakeMethod.Prepare(ctx, e)
}

func TestRotateLockedByAnotherRun(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientset()
	first, firstCalls, records := rotator([]Entry{generated("grafana.admin")}, nil)
	first.Lock = &LeaseLocker{Kube: kube, Namespace: "secrets-rotation"}
	block := blockingPrepare{fakeMethod{calls: firstCalls}, make(chan struct{}), make(chan struct{})}
	first.Methods[KindGenerated] = block
	done := make(chan error)
	go func() {
		_, err := first.Run(ctx, first.Entries, Options{Yes: true, Now: now, By: "rotation-job"})
		done <- err
	}()
	<-block.entered

	second, secondCalls, _ := rotator([]Entry{generated("grafana.admin")}, nil)
	second.Store = records
	second.Lock = &LeaseLocker{Kube: kube, Namespace: "secrets-rotation"}
	results, err := second.Run(ctx, second.Entries, Options{Yes: true, Now: now, By: "steven@workstation"})
	if err == nil || results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "rotation-job") {
		t.Fatalf("second Run = %+v, %v; want a refusal naming the holder", results, err)
	}
	if len(*secondCalls) != 0 {
		t.Fatalf("the second run called %v", *secondCalls)
	}
	close(block.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := kube.CoordinationV1().Leases("secrets-rotation").Get(ctx, "rotate-grafana.admin", metav1.GetOptions{}); err == nil {
		t.Fatal("the lock was not released")
	}
}

func TestRotateTakesOverExpiredLock(t *testing.T) {
	ctx := context.Background()
	stale := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "rotate-grafana.admin", Namespace: "secrets-rotation"},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       ptr.To("rotation-job/0a1b"),
			LeaseDurationSeconds: ptr.To[int32](60),
			RenewTime:            &metav1.MicroTime{Time: time.Now().Add(-time.Hour)},
		},
	}
	kube := fake.NewClientset(stale)
	r, calls, _ := rotator([]Entry{generated("grafana.admin")}, nil)
	r.Lock = &LeaseLocker{Kube: kube, Namespace: "secrets-rotation"}
	if _, err := r.Run(ctx, r.Entries, Options{Yes: true, Now: now, By: "steven@workstation"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 4 {
		t.Fatalf("calls = %v", *calls)
	}
}

// leaseObject reads the Lease from the fake cluster's tracker
func leaseObject(t *testing.T, kube *fake.Clientset) *coordinationv1.Lease {
	t.Helper()
	obj, err := kube.Tracker().Get(leaseGVR, "secrets-rotation", "rotate-grafana.admin")
	if err != nil {
		t.Fatal(err)
	}
	return obj.(*coordinationv1.Lease)
}

var leaseGVR = coordinationv1.SchemeGroupVersion.WithResource("leases")

func TestLockReleaseKeepsATakenOverLease(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientset()
	locker := &LeaseLocker{Kube: kube, Namespace: "secrets-rotation"}
	_, release, err := locker.Lock(ctx, generated("grafana.admin"), "steven@workstation/1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mine := leaseObject(t, kube)
	mine.ResourceVersion = "1"
	if err := kube.Tracker().Update(leaseGVR, mine, "secrets-rotation"); err != nil {
		t.Fatal(err)
	}
	// Between release's Get and its Delete another run takes the Lease over (an Update: same UID, new version)
	kube.PrependReactor("get", "leases", func(action k8stesting.Action) (bool, runtime.Object, error) {
		current := leaseObject(t, kube)
		taken := current.DeepCopy()
		taken.Spec.HolderIdentity = ptr.To("rotation-job/2")
		taken.ResourceVersion = "2"
		if err := kube.Tracker().Update(leaseGVR, taken, "secrets-rotation"); err != nil {
			t.Error(err)
		}
		return true, current, nil
	})
	// The API server's precondition check, which the fake does not do
	kube.PrependReactor("delete", "leases", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pre, current := action.(k8stesting.DeleteActionImpl).DeleteOptions.Preconditions, leaseObject(t, kube)
		if pre != nil && ((pre.UID != nil && *pre.UID != current.UID) ||
			(pre.ResourceVersion != nil && *pre.ResourceVersion != current.ResourceVersion)) {
			return true, nil, apierrors.NewConflict(leaseGVR.GroupResource(), "rotate-grafana.admin", errors.New("precondition failed"))
		}
		return false, nil, nil
	})
	release()
	if got := *leaseObject(t, kube).Spec.HolderIdentity; got != "rotation-job/2" {
		t.Fatalf("holder = %q: release deleted a Lease another run took over", got)
	}
}

func TestRotateStopsWhenItLosesTheLock(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientset()
	var out strings.Builder
	r, calls, records := rotator([]Entry{generated("grafana.admin")}, nil)
	r.Out = &syncWriter{w: &out}
	r.Lock = &LeaseLocker{Kube: kube, Namespace: "secrets-rotation", Renew: 5 * time.Millisecond, Out: r.Out}
	// The takeover and every renewal are serialized: the fake tracker does not check resourceVersion, so a renewal
	// landing between the takeover's two writes would put our holder back
	var mu sync.Mutex
	taken := false
	kube.PrependReactor("update", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		if !taken {
			return false, nil, nil
		}
		return true, nil, apierrors.NewConflict(leaseGVR.GroupResource(), "rotate-grafana.admin", errors.New("modified"))
	})
	// Prepare runs until its context ends; meanwhile another run takes the lock over, so the renewal conflicts
	r.Methods[KindGenerated] = untilCancelled{fakeMethod{calls: calls}, func() {
		mu.Lock()
		defer mu.Unlock()
		lease := leaseObject(t, kube).DeepCopy()
		lease.Spec.HolderIdentity = ptr.To("rotation-job/2")
		if err := kube.Tracker().Update(leaseGVR, lease, "secrets-rotation"); err != nil {
			t.Error(err)
		}
		taken = true
	}}
	done := make(chan struct{})
	var results []Result
	var err error
	go func() {
		defer close(done)
		results, err = r.Run(ctx, r.Entries, Options{Yes: true, Now: now, By: "steven@workstation"})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run went on after losing its lock")
	}
	if err == nil || results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "lost the lock to rotation-job/2") {
		t.Fatalf("Run = %+v, %v", results, err)
	}
	if slices.Contains(*calls, "grafana.admin switch") {
		t.Fatalf("calls = %v: the run went on to the next phase", *calls)
	}
	if got := records.records["grafana.admin"].Phase; got != PhasePreparing {
		t.Fatalf("phase = %q: a run that lost its lock must not record over the new holder", got)
	}
}

func TestLockRenewalFailureIsLogged(t *testing.T) {
	kube := fake.NewClientset()
	var out strings.Builder
	w := &syncWriter{w: &out}
	locker := &LeaseLocker{Kube: kube, Namespace: "secrets-rotation", Renew: 2 * time.Millisecond, Out: w}
	var down atomic.Bool
	kube.PrependReactor("update", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !down.Load() {
			return false, nil, nil
		}
		return true, nil, apierrors.NewServiceUnavailable("etcd is down")
	})
	lockCtx, release, err := locker.Lock(context.Background(), generated("grafana.admin"), "steven@workstation/1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	down.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(w.String(), "renew") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(w.String(), "renew") || lockCtx.Err() != nil {
		t.Fatalf("output %q, context %v: want the failure logged and the run going on", w.String(), lockCtx.Err())
	}
	release()
}

// untilCancelled runs hook in Prepare, then waits for the context to end
type untilCancelled struct {
	fakeMethod
	hook func()
}

func (m untilCancelled) Prepare(ctx context.Context, e Entry) error {
	_ = m.fakeMethod.Prepare(ctx, e)
	m.hook()
	<-ctx.Done()
	return ctx.Err()
}

type syncWriter struct {
	mu sync.Mutex
	w  *strings.Builder
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.String()
}

// A renewal that reached the API server but whose answer was lost conflicts at the next renewal: the Lease is still
// ours, so the run keeps it instead of reporting a lost lock
func TestLockKeepsItsOwnLeaseAfterAConflict(t *testing.T) {
	kube := fake.NewClientset()
	locker := &LeaseLocker{Kube: kube, Namespace: "secrets-rotation", Renew: 2 * time.Millisecond, Out: io.Discard}
	var conflicts atomic.Int32
	kube.PrependReactor("update", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts.Add(1) <= 3 {
			return true, nil, apierrors.NewConflict(leaseGVR.GroupResource(), "rotate-grafana.admin", errors.New("modified"))
		}
		return false, nil, nil
	})
	lockCtx, release, err := locker.Lock(context.Background(), generated("grafana.admin"), "steven@workstation/1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	deadline := time.Now().Add(5 * time.Second)
	for conflicts.Load() < 6 {
		if time.Now().After(deadline) {
			t.Fatalf("renewals stopped after %d conflicts: %v", conflicts.Load(), context.Cause(lockCtx))
		}
		time.Sleep(time.Millisecond)
	}
	if cause := context.Cause(lockCtx); cause != nil {
		t.Fatalf("the run gave up its own lock: %v", cause)
	}
}
