package rotate

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/backup"
	"github.com/duongle-steven/homelab/cli/internal/workload"
)

// A Method changes one entry in phases, so an interrupted run resumes: Prepare writes the new value, Switch moves the
// readers to it, Verify checks they work, Revoke ends the old value (a no-op without an overlap). Each phase may run
// again after an interruption. Prepare in particular must find and reuse a new value a previous run wrote (the
// record says preparing): it never creates a third one.
type Method interface {
	Prepare(ctx context.Context, e Entry) error
	Switch(ctx context.Context, e Entry) error
	Verify(ctx context.Context, e Entry) error
	Revoke(ctx context.Context, e Entry) error
}

// Options of one run.
type Options struct {
	// All rotates the whole catalog, Scheduled only the due entries (it implies Yes)
	All, Scheduled, Yes bool
	// Interactive: a person answers on a terminal, so manual entries can run
	Interactive     bool
	SkipBackupCheck string // the reason; empty = check
	Now             time.Time
	By              string // the person or job, recorded
	Env             string // the environment rotated (production, sandbox): entries of another one are left alone
}

// elsewhere names the environments an entry belongs to when env is not one of them
func elsewhere(e Entry, env string) string {
	if env == "" || len(e.Envs) == 0 || slices.Contains(e.Envs, env) {
		return ""
	}
	return strings.Join(e.Envs, ", ") + " only"
}

// Result is what happened to one entry.
type Result struct {
	Name    string
	Outcome string // rotated, skipped (and why), failed (and the phase), not started (after a failure)
	Err     error
	Overdue bool // not rotated and more than OverdueMargin past its due date
}

// OverdueMargin is how long past its due date an entry may stay before a run fails for it (SecretRotationOverdue).
const OverdueMargin = 14 * 24 * time.Hour

// BackupAge is how recent the off-site backup must be before a rotation: a day, plus the hours the daily run may still
// be copying when the 03:00 rotation starts (yesterday's completed run then counts)
const BackupAge = 30 * time.Hour

// Rotator runs rotations.
type Rotator struct {
	Entries []Entry
	Methods map[Kind]Method // by entry kind; a kind without one is skipped
	Store   Records
	// Backups lists the off-site runs (backup.Client.Runs)
	Backups func(ctx context.Context) ([]backup.Run, error)
	// Idle reports whether what (an entry's Idle: woodpecker, paperless) is idle
	Idle func(ctx context.Context, what string) (bool, error)
	Out  io.Writer
	// Poll is how often the idle wait looks again, Wait how long it may take
	Poll, Wait time.Duration
	// Lock keeps two runs from rotating one entry at once
	Lock Locker
}

// Plan picks the entries a run rotates, in the order --all uses: the named ones (unknown or pending names error),
// the whole catalog with All, or the due entries with Scheduled.
func (r *Rotator) Plan(ctx context.Context, names []string, o Options) ([]Entry, error) {
	var plan []Entry
	switch {
	case len(names) > 0 && (o.All || o.Scheduled):
		return nil, errors.New("name secrets, or pass --all or --scheduled, not both")
	case o.All && o.Scheduled:
		return nil, errors.New("--all and --scheduled exclude each other")
	case len(names) > 0:
		var problems []string
		for _, name := range names {
			e, err := Find(r.Entries, name)
			switch {
			case err != nil:
				problems = append(problems, fmt.Sprintf("no secret %q in the catalog; closest: %s", name, strings.Join(closest(r.Entries, name), ", ")))
			case e.Pending != "":
				problems = append(problems, fmt.Sprintf("%s is not rotated yet: pending %s", name, e.Pending))
			case elsewhere(e, o.Env) != "":
				problems = append(problems, fmt.Sprintf("%s is %s, not %s", name, elsewhere(e, o.Env), o.Env))
			case !slices.ContainsFunc(plan, func(p Entry) bool { return p.Name == name }):
				plan = append(plan, e)
			}
		}
		if len(problems) > 0 {
			return nil, fmt.Errorf("%s (lab secrets list)", strings.Join(problems, "; "))
		}
	case o.All:
		plan = slices.Clone(r.Entries)
	case o.Scheduled:
		// Due entries and interrupted rotations; never manual ones (a person acts) or OpenTofu's (from the
		// workstation only: make infra env=production warns when one is due)
		for _, e := range r.Entries {
			if slices.Contains([]Kind{KindManual, KindOpenTofu, KindNever, KindAutomatic, KindScript}, e.Kind) {
				continue
			}
			rec, err := r.Store.Get(ctx, e)
			if err != nil {
				return nil, err
			}
			// An interrupted one without a period is listed to be reported, not resumed (Skip)
			interrupted := rec.Phase != "" && rec.Phase != PhaseDone
			if due, periodic := Due(e, rec); interrupted || (periodic && !due.After(o.Now)) {
				plan = append(plan, e)
			}
		}
	default:
		return nil, errors.New("name a secret, or pass --all or --scheduled")
	}
	slices.SortStableFunc(plan, func(a, b Entry) int { return cmp.Compare(rank(a), rank(b)) })
	return plan, nil
}

// rank is an entry's place in a run: generated secrets and ntfy, Forgejo, Kanidm, Woodpecker and zot (with the idle
// wait), OpenTofu's, manual ones, then the entries that are only listed.
func rank(e Entry) int {
	switch {
	case e.Kind == KindNever || e.Kind == KindAutomatic || e.Kind == KindScript:
		return 7
	case e.Kind == KindManual:
		return 6
	case e.Kind == KindOpenTofu:
		return 5
	case e.Idle == "woodpecker" || e.Kind == KindWoodpeckerAgent || e.Kind == KindWoodpeckerToken:
		return 4
	case e.Kind == KindKaniopClient || e.Kind == KindKanidmAdmin:
		return 3
	case e.Kind == KindForgejoToken || e.Kind == KindForgejoOAuth || e.Kind == KindCNPG:
		return 2
	default:
		return 1
	}
}

// Due is when an entry is next due, and whether it rotates on a schedule: a period after its last rotation or, never
// rotated, after rec.Since (its value's creation or first sighting); with neither it is due at once.
func Due(e Entry, rec Record) (time.Time, bool) {
	if e.Period == 0 {
		return time.Time{}, false
	}
	from := rec.At
	if from.IsZero() {
		from = rec.Since
	}
	if from.IsZero() {
		return time.Time{}, true
	}
	return from.Add(e.Period), true
}

// Skip is why a run leaves this entry alone, or "" when it rotates it.
func (r *Rotator) Skip(e Entry, o Options) string {
	switch e.Kind {
	case KindNever:
		return "never rotated: " + e.Reason
	case KindAutomatic:
		return "automatic: " + e.Reason
	case KindScript:
		return "by a script: " + e.Steps
	}
	if e.Pending != "" {
		return "pending " + e.Pending
	}
	if where := elsewhere(e, o.Env); where != "" {
		return where
	}
	if o.Scheduled && e.Period == 0 {
		// Only an interrupted rotation by command reaches a scheduled plan: a person finishes it
		return fmt.Sprintf("interrupted, resume with `lab secrets rotate %s`", e.Name)
	}
	if e.Kind == KindManual {
		if o.Scheduled {
			return "manual, never on a schedule"
		}
		if !o.Interactive {
			return "manual, it needs a person at a terminal: " + e.Steps
		}
	}
	if r.Methods[e.Kind] == nil {
		return fmt.Sprintf("no method yet for kind %s", e.Kind)
	}
	return ""
}

// Run checks the off-site backup, then rotates the plan's entries one after another, resuming each from its recorded
// phase, and stops at the first failure. It returns every entry's result, and an error when an entry failed, is
// overdue, or was named but not rotated.
func (r *Rotator) Run(ctx context.Context, plan []Entry, o Options) ([]Result, error) {
	if o.SkipBackupCheck == "" && slices.ContainsFunc(plan, func(e Entry) bool { return r.Skip(e, o) == "" }) {
		if err := r.checkBackup(ctx, o.Now); err != nil {
			return nil, err
		}
	}
	named := !o.All && !o.Scheduled
	holder := holderID(o.By)
	results := make([]Result, 0, len(plan))
	var failed, overdue, left, interrupted []string
	for _, e := range plan {
		if len(failed) > 0 {
			results = append(results, Result{Name: e.Name, Outcome: fmt.Sprintf("not started: %s failed first", failed[0])})
			continue
		}
		res := r.rotate(ctx, e, o, holder)
		if res.Err == nil && res.Outcome != "rotated" {
			if late, err := r.overdue(ctx, e, o); err != nil {
				res.Outcome, res.Err = "failed reading its record", err
			} else {
				res.Overdue = late
			}
		}
		switch {
		case res.Err != nil:
			failed = append(failed, e.Name)
		case res.Overdue:
			overdue = append(overdue, e.Name)
		case o.Scheduled && e.Period == 0:
			interrupted = append(interrupted, e.Name)
		// A script or automatic entry named alone is answered by its steps or reason; never is a refusal
		case named && res.Outcome != "rotated" && e.Kind != KindScript && e.Kind != KindAutomatic:
			left = append(left, e.Name)
		}
		results = append(results, res)
	}
	var problems []string
	if len(failed) > 0 {
		problems = append(problems, "failed: "+strings.Join(failed, ", "))
	}
	if len(overdue) > 0 {
		problems = append(problems, fmt.Sprintf("overdue by more than %d days: %s", int(OverdueMargin.Hours()/24), strings.Join(overdue, ", ")))
	}
	if len(interrupted) > 0 {
		problems = append(problems, "interrupted, resume by command: "+strings.Join(interrupted, ", "))
	}
	if len(left) > 0 {
		problems = append(problems, "not rotated: "+strings.Join(left, ", "))
	}
	if len(problems) > 0 {
		return results, errors.New(strings.Join(problems, "; "))
	}
	return results, nil
}

// phase is one step of a Method and the phase recorded once it finished
type phase struct {
	step, done string
	run        func(Method, context.Context, Entry) error
}

var phases = []phase{
	{"prepare", PhasePrepared, Method.Prepare},
	{"switch", PhaseSwitched, Method.Switch},
	{"verify", PhaseVerified, Method.Verify},
	{"revoke", PhaseDone, Method.Revoke},
}

func (r *Rotator) rotate(ctx context.Context, e Entry, o Options, holder string) Result {
	if reason := r.Skip(e, o); reason != "" {
		return Result{Name: e.Name, Outcome: "skipped: " + reason}
	}
	// lost is why the lock ended under the run, if another run took it over
	lost := func() error {
		var l *LostLockError
		if errors.As(context.Cause(ctx), &l) {
			return l
		}
		return nil
	}
	fail := func(what string, err error) Result {
		if l := lost(); l != nil {
			err = l
		}
		return Result{Name: e.Name, Outcome: "failed " + what, Err: err}
	}
	if r.Lock == nil {
		return fail("locking", errors.New("no lock to keep two rotations apart"))
	}
	// Held from before the idle wait and the record's read to the end, renewed meanwhile; ctx ends if it is lost
	locked, release, err := r.Lock.Lock(ctx, e, holder, r.Wait+5*time.Minute)
	if err != nil {
		return fail("locking", err)
	}
	defer release()
	ctx = locked
	if e.Idle != "" {
		if err := r.waitIdle(ctx, e.Idle); err != nil {
			return fail("waiting for idle", err)
		}
	}
	rec, err := r.Store.Get(ctx, e)
	if err != nil {
		return fail("reading its record", err)
	}
	start := 0
	resuming := rec.Phase != "" && rec.Phase != PhaseDone
	if resuming {
		i := slices.IndexFunc(phases, func(p phase) bool { return p.done == rec.Phase })
		if rec.Phase == PhasePreparing {
			i = -1 // Prepare again: it finds the value it may have written
		} else if i < 0 {
			return fail("reading its record", fmt.Errorf("unknown phase %q at secret/%s", rec.Phase, RecordPath(e)))
		}
		if rec.Method != "" && rec.Method != string(e.Kind) {
			return fail("resuming", fmt.Errorf("an interrupted rotation by method %s, now %s: finish it by hand", rec.Method, e.Kind))
		}
		start = i + 1
		fmt.Fprintf(r.Out, "%s: resuming after %s\n", e.Name, rec.Phase)
	}
	// The reason a resumed rotation skipped the backup check stays unless this run gives its own
	reason := o.SkipBackupCheck
	if reason == "" && resuming {
		reason = rec.Reason
	}
	record := func(phase string) error {
		// The new holder owns the record now
		if l := lost(); l != nil {
			return l
		}
		rec.Phase, rec.By, rec.Method, rec.Reason = phase, o.By, string(e.Kind), reason
		if phase == PhaseDone {
			rec.At = o.Now
		}
		// A phase that finished is recorded even after Ctrl-C, so the rerun does not repeat it
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return r.Store.Set(ctx, e, rec)
	}
	if start == 0 {
		if err := record(PhasePreparing); err != nil {
			return fail("recording "+PhasePreparing, err)
		}
	}
	method := r.Methods[e.Kind]
	for _, p := range phases[start:] {
		if l := lost(); l != nil {
			return fail("before "+p.step, l)
		}
		fmt.Fprintf(r.Out, "%s: %s\n", e.Name, p.step)
		if err := p.run(method, ctx, e); err != nil {
			return fail("in "+p.step, err)
		}
		if err := record(p.done); err != nil {
			return fail("recording "+p.done, err)
		}
	}
	return Result{Name: e.Name, Outcome: "rotated"}
}

// holderID names one run in a lock: the person or job, and a random suffix
func holderID(by string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return cmp.Or(by, "lab") + "/" + hex.EncodeToString(b)
}

func (r *Rotator) overdue(ctx context.Context, e Entry, o Options) (bool, error) {
	// OpenTofu's rotate from the workstation, so the scheduled run never counts them
	if e.Period == 0 || (o.Scheduled && e.Kind == KindOpenTofu) {
		return false, nil
	}
	rec, err := r.Store.Get(ctx, e)
	if err != nil {
		return false, err
	}
	due, _ := Due(e, rec)
	return o.Now.Sub(due) > OverdueMargin, nil
}

// idleWhat names what an idle wait waits for
var idleWhat = map[string]string{
	"woodpecker": "Woodpecker to have no running pipeline",
	"paperless":  "paperless to have no queued task",
}

func (r *Rotator) waitIdle(ctx context.Context, what string) error {
	if r.Idle == nil {
		return fmt.Errorf("no idle check for %s", what)
	}
	description := cmp.Or(idleWhat[what], what+" to be idle")
	fmt.Fprintf(r.Out, "waiting for %s\n", description)
	waiter := workload.Client{Poll: r.Poll, Wait: r.Wait}
	return waiter.Until(ctx, description, func() (bool, error) { return r.Idle(ctx, what) })
}

// checkBackup wants a completed scheduled off-site run (not a push) started less than BackupAge before now.
func (r *Rotator) checkBackup(ctx context.Context, now time.Time) error {
	if r.Backups == nil {
		return errors.New("no way to check the off-site backup: pass --skip-backup-check=<reason>")
	}
	runs, err := r.Backups(ctx)
	if err != nil {
		return fmt.Errorf("check the off-site backup: %w", err)
	}
	newest := "none"
	for _, run := range runs {
		if run.Schedule == "" || run.App != "" {
			continue
		}
		if run.Phase == "Completed" && !run.Started.After(now) && now.Sub(run.Started) < BackupAge {
			return nil
		}
		if newest == "none" {
			newest = fmt.Sprintf("%s, %s, started %s", run.Name, run.Phase, run.Started.Local().Format(time.DateTime))
		}
	}
	return fmt.Errorf("no completed scheduled off-site backup started in the last %d hours (newest: %s): wait for "+
		"the daily run or run lab backup push <app>, or pass --skip-backup-check=<reason>", int(BackupAge.Hours()), newest)
}

// closest returns the three catalog names nearest to name
func closest(entries []Entry, name string) []string {
	type candidate struct {
		name     string
		distance int
	}
	candidates := make([]candidate, 0, len(entries))
	for _, e := range entries {
		candidates = append(candidates, candidate{e.Name, levenshtein(name, e.Name)})
	}
	slices.SortStableFunc(candidates, func(a, b candidate) int { return cmp.Compare(a.distance, b.distance) })
	var names []string
	for _, c := range candidates[:min(3, len(candidates))] {
		names = append(names, c.name)
	}
	return names
}

func levenshtein(a, b string) int {
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}
