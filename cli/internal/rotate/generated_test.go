package rotate

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/cli/internal/workload"
)

// fakeKV is OpenBao's KV store: every write is logged as "write <path>"; failing makes every call fail with an error
// that names the data, the way a careless store would, and failWrites only its writes
type fakeKV struct {
	data       map[string]map[string]string
	log        *[]string
	failing    bool
	failWrites bool
}

func newKV(log *[]string, data map[string]map[string]string) *fakeKV {
	if data == nil {
		data = map[string]map[string]string{}
	}
	return &fakeKV{data: data, log: log}
}

func (f *fakeKV) Read(_ context.Context, path string) (map[string]string, error) {
	if f.failing {
		return nil, fmt.Errorf("read secret/%s: it holds %v", path, f.data[path])
	}
	if f.data[path] == nil {
		return nil, nil
	}
	return maps.Clone(f.data[path]), nil
}

func (f *fakeKV) Write(_ context.Context, path string, data map[string]string) error {
	if f.failing || f.failWrites {
		return fmt.Errorf("write secret/%s with %v", path, data)
	}
	*f.log = append(*f.log, "write "+path)
	f.data[path] = maps.Clone(data)
	return nil
}

// fakeReaders logs what the method does to the readers; failing makes every call fail
type fakeReaders struct {
	log     *[]string
	failing bool
}

func (f fakeReaders) do(what, namespace string, names []string) error {
	if f.failing {
		return fmt.Errorf("%s %s failed", what, namespace)
	}
	*f.log = append(*f.log, what+" "+namespace+"/"+strings.Join(names, ","))
	return nil
}

func (f fakeReaders) RefreshExternalSecrets(_ context.Context, namespace string, names []string) error {
	return f.do("refresh", namespace, names)
}

func refNames(refs []workload.Ref) []string {
	var names []string
	for _, r := range refs {
		names = append(names, r.Name)
	}
	return names
}

func (f fakeReaders) Restart(_ context.Context, namespace string, refs []workload.Ref) error {
	return f.do("restart", namespace, refNames(refs))
}

func (f fakeReaders) WaitReady(_ context.Context, namespace string, refs []workload.Ref) error {
	return f.do("wait", namespace, refNames(refs))
}

const newValue = "N3w-value-0123456789"

// fixed returns newValue and counts its calls
func fixed(n *int) func(Entry) (string, error) {
	return func(Entry) (string, error) { *n++; return newValue, nil }
}

func catalogEntry(t *testing.T, name string) Entry {
	t.Helper()
	entries, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	e, err := Find(entries, name)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestGeneratedWritesEveryDestination(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "ntfy.alerts-token")
	if len(e.Paths) != 2 {
		t.Fatalf("ntfy.alerts-token has paths %v, want two", e.Paths)
	}
	log := &[]string{}
	kv := newKV(log, map[string]map[string]string{
		e.Paths[0]: {"token": "old"},
		e.Paths[1]: {"token": "old", "other": "kept"},
	})
	n := 0
	g := Generated{Store: kv, Workloads: fakeReaders{log: log}, Random: fixed(&n)}
	if err := g.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	for _, p := range e.Paths {
		if got := kv.data[p]["token"]; got != newValue {
			t.Errorf("secret/%s token is not the new value", p)
		}
	}
	if kv.data[e.Paths[1]]["other"] != "kept" {
		t.Error("another key of the path was lost")
	}
	// An interrupted run's Prepare finds the value it wrote: never a third one
	kv.data[e.Paths[1]]["token"] = "old"
	if err := g.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	if n != 1 || kv.data[e.Paths[1]]["token"] != newValue {
		t.Fatalf("a second Prepare generated %d values or did not finish the write", n)
	}
	if err := g.Switch(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := g.Verify(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := g.Revoke(ctx, e); err != nil {
		t.Fatal(err)
	}
	if kv.data[RecordPath(e)][pendingKey] != "" {
		t.Error("Revoke left the pending value at the record's path")
	}
	for _, want := range []string{"refresh ntfy/ntfy-auth", "refresh monitoring-system/ntfy", "restart ntfy/ntfy", "wait ntfy/ntfy"} {
		if !slices.Contains(*log, want) {
			t.Errorf("log %q lacks %q", *log, want)
		}
	}
	// The next rotation starts from a new value
	if err := g.Prepare(ctx, e); err != nil || n != 2 {
		t.Fatalf("the next rotation's Prepare: %v, %d values", err, n)
	}
}

func TestGeneratedMatchesGeneratorSettings(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := checkGeneratorSettings("../../..", entries); len(got) != 0 {
		t.Fatalf("the catalog and platform/generated-secrets differ:\n%s", strings.Join(got, "\n"))
	}
	// A changed length, a dropped special and a missing destination are caught
	root := t.TempDir()
	write(t, root, "platform/generated-secrets/values.yaml", "secrets:\n  - name: a\n    key: password\n    length: 64\n    special: true\n    destinations: [a/generated/x, b/generated/x]\n")
	stale, err := parse([]byte("- {name: a, kind: generated, paths: [a/generated/x], settings: {key: password, length: 32}}\n- {name: b, kind: generated, paths: [b/generated/y], settings: {key: k, length: 32, special: false}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(checkGeneratorSettings(root, stale), "\n")
	for _, want := range []string{"a: length", "a: special", "a: paths", "b: not in platform/generated-secrets"} {
		if !strings.Contains(got, want) {
			t.Errorf("checkGeneratorSettings = %q, missing %q", got, want)
		}
	}
}

func TestPasswordFollowsSettings(t *testing.T) {
	plain, err := Password(Entry{Settings: map[string]string{"length": "29", "special": "false"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 29 || strings.ContainsAny(plain, symbols) {
		t.Fatalf("a 29-character value without symbols has length %d or symbols", len(plain))
	}
	special, err := Password(Entry{Settings: map[string]string{"length": "32", "special": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(special) != 32 || !strings.ContainsAny(special, symbols) || !strings.ContainsAny(special, "0123456789") {
		t.Fatal("a special value lacks symbols or digits")
	}
	if _, err := Password(Entry{Settings: map[string]string{"length": "eight"}}); err == nil {
		t.Fatal("a bad length was accepted")
	}
}

// TestMethodErrorsHideValues feeds every method in the package failing fakes whose errors name the values they saw,
// and checks no error a method returns contains one
func TestMethodErrorsHideValues(t *testing.T) {
	ctx := context.Background()
	const current = "Curr3nt-value-9876543210"
	values := []string{newValue, current}
	type phaseFunc func(Method, context.Context, Entry) error
	run := func(t *testing.T, m Method, e Entry) {
		t.Helper()
		for name, p := range map[string]phaseFunc{"prepare": Method.Prepare, "switch": Method.Switch, "verify": Method.Verify, "revoke": Method.Revoke} {
			err := p(m, ctx, e)
			if err == nil {
				continue
			}
			for _, v := range values {
				if strings.Contains(err.Error(), v) {
					t.Errorf("%s: %s's error contains a value: %v", e.Name, name, err)
				}
			}
		}
	}
	// stage: the store fails at once, its writes fail, or the store works and the app and the readers fail
	for _, stage := range []string{"store", "write", "app"} {
		t.Run(stage, func(t *testing.T) {
			for _, name := range []string{"ntfy.alerts-password", "paperless.secret-key"} {
				e := catalogEntry(t, name)
				log := &[]string{}
				kv := newKV(log, map[string]map[string]string{e.Paths[0]: {e.Settings["key"]: current}, RecordPath(e): {pendingKey: newValue}})
				kv.failing, kv.failWrites = stage == "store", stage == "write"
				n := 0
				run(t, Generated{Store: kv, Workloads: fakeReaders{log: log, failing: true}, Random: fixed(&n)}, e)
			}
			for _, name := range []string{"ntfy.alerts-token", "ntfy.phone-token"} {
				e := catalogEntry(t, name)
				log := &[]string{}
				kv := newKV(log, map[string]map[string]string{e.Paths[0]: {e.Settings["key"]: current}, RecordPath(e): {pendingKey: newValue}})
				kv.failing, kv.failWrites = stage == "store", stage == "write"
				n := 0
				failing := func(_ context.Context, _, token string) error { return fmt.Errorf("ntfy refused %s", token) }
				run(t, NtfyOverlap{Store: kv, Workloads: fakeReaders{log: log, failing: true}, Random: fixed(&n), Check: failing}, e)
				run(t, NtfyOverlap{Store: kv, Workloads: fakeReaders{log: log}, Random: fixed(&n), Check: NtfyCheck(fakeExec{log: log, failing: true})}, e)
			}
			for _, name := range []string{"forgejo.deploy-token", "forgejo.argocd-token", "forgejo.woodpecker-oauth"} {
				e := catalogEntry(t, name)
				log := &[]string{}
				kv := newKV(log, map[string]map[string]string{e.Paths[0]: {"token": current, "client_secret": current}})
				kv.failing, kv.failWrites = stage == "store", stage == "write"
				api := newFakeForgejo(log, kv)
				api.tokens["deploy"]["lab"], api.tokens["argocd"]["argocd"] = current, current
				api.failing = stage == "app"
				run(t, ForgejoToken{API: api, Store: kv, Workloads: fakeReaders{log: log, failing: true}}, e)
				run(t, ForgejoOAuth{API: api, Store: kv, Workloads: fakeReaders{log: log, failing: true}}, e)
			}
			for _, name := range []string{"forgejo.admin", "grafana.admin", "paperless.admin"} {
				e := catalogEntry(t, name)
				log := &[]string{}
				kv := newKV(log, map[string]map[string]string{e.Paths[0]: {e.Settings["key"]: current}})
				kv.failing, kv.failWrites = stage == "store", stage == "write"
				n := 0
				apps := failingApps(t, log)
				run(t, AppPassword{Store: kv, Workloads: fakeReaders{log: log, failing: true}, Random: fixed(&n), Apps: apps}, e)
			}
		})
	}
}
