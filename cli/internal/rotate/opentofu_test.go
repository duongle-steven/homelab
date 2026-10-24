package rotate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// OpenTofu's secrets rotate from the workstation: -replace in the secret's unit, then the secrets unit's apply writes
// the new value into namespace infra-secrets, from where infra-secrets pushes it to OpenBao; the readers move once
// OpenBao holds it
func TestOpenTofuRunsReplaceThenSecrets(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "cloudflare.external-dns")
	log := &[]string{}
	kv := newKV(&[]string{}, map[string]map[string]string{"external-dns/cloudflare": {"api_token": "old-token"}})
	var runs []string
	applies := 0
	run := func(_ context.Context, dir string, args ...string) error {
		runs = append(runs, dir+": "+strings.Join(args, " "))
		if strings.HasSuffix(dir, "/secrets") {
			// infra-secrets pushes the new value to OpenBao
			applies++
			kv.data["external-dns/cloudflare"] = map[string]string{"api_token": "new-token"}
		}
		return nil
	}
	m := OpenTofu{Root: "/repo", Run: run, Store: kv, Workloads: fakeReaders{log: log}, Waits: fastWaiter{},
		Confirm: func(context.Context, string) error { return nil }}
	for range 2 {
		if err := m.Prepare(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		// The plan first, for the person to read: it may hold more than the replacement (drift, someone's change)
		"/repo/infra/production/cloudflare: plan --non-interactive -- -replace=cloudflare_account_token.external_dns",
		"/repo/infra/production/cloudflare: apply --non-interactive -- -auto-approve -replace=cloudflare_account_token.external_dns",
		"/repo/infra/production/secrets: apply --non-interactive -- -auto-approve",
		// The rerun applies the secrets unit again (idempotent), never the replace
		"/repo/infra/production/secrets: apply --non-interactive -- -auto-approve",
	}
	if !slices.Equal(runs, want) || applies != 2 {
		t.Fatalf("runs = %q, want %q (a rerun must not replace the token again)", runs, want)
	}
	if got := kv.data[RecordPath(e)]["value-hash"]; got != valueHash(map[string]string{"api_token": "old-token"}) || strings.Contains(got, "old-token") {
		t.Fatalf("the state keeps %q, want the old value's hash (never the value)", got)
	}
	for _, phase := range []func(context.Context, Entry) error{m.Switch, m.Verify, m.Revoke} {
		if err := phase(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(*log, []string{"refresh external-dns/cloudflare-api-token", "restart external-dns/external-dns", "wait external-dns/external-dns"}) {
		t.Fatalf("readers: %v", *log)
	}
}

// OpenBao still holding the old value after the apply (infra-secrets did not push) stops the rotation before the
// readers restart
func TestOpenTofuWaitsForThePush(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "cloudflare.cert-manager")
	log := &[]string{}
	kv := newKV(&[]string{}, map[string]map[string]string{"cert-manager/cloudflare": {"api_token": "old-token"}})
	m := OpenTofu{Root: "/repo", Run: func(context.Context, string, ...string) error { return nil }, Store: kv, Workloads: fakeReaders{log: log}, Waits: fastWaiter{fail: true},
		Confirm: func(context.Context, string) error { return nil }}
	if err := m.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := m.Switch(ctx, e); err == nil || !strings.Contains(err.Error(), "cert-manager/cloudflare") {
		t.Fatalf("Switch = %v", err)
	}
	if len(*log) != 0 {
		t.Fatalf("the readers moved before OpenBao held the new value: %v", *log)
	}
}

// A person does what lab cannot (GitHub has no API for an App's keys): lab gives the steps, waits for the person,
// then moves the readers
func TestManualNeedsConfirmation(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "renovate.github-app-key")
	log := &[]string{}
	var out strings.Builder
	m := Manual{Out: &out, Workloads: fakeReaders{log: log}}
	if err := m.Prepare(ctx, e); err == nil {
		t.Fatal("Prepare without a person went on")
	}
	var asked string
	m.Confirm = func(_ context.Context, q string) error { asked = q; return nil }
	if err := m.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "GitHub has no API") || asked == "" {
		t.Fatalf("out %q, asked %q", out.String(), asked)
	}
	for _, phase := range []func(context.Context, Entry) error{m.Switch, m.Verify, m.Revoke} {
		if err := phase(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(*log, []string{"refresh renovate/renovate-github-app"}) {
		t.Fatalf("readers: %v", *log)
	}
}

// fastWaiter checks once: done or not
type fastWaiter struct{ fail bool }

func (f fastWaiter) Until(ctx context.Context, what string, done func() (bool, error)) error {
	ok, err := done()
	if err != nil {
		return err
	}
	if !ok || f.fail {
		return fmt.Errorf("waited for %s", what)
	}
	return nil
}

// Without a person's yes after the plan nothing is applied: one apply changes everything pending in the unit
func TestOpenTofuAsksAfterThePlan(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "cloudflare.cert-manager")
	var runs []string
	run := func(_ context.Context, dir string, args ...string) error {
		runs = append(runs, args[0])
		return nil
	}
	kv := newKV(&[]string{}, map[string]map[string]string{"cert-manager/cloudflare": {"api_token": "old-token"}})
	m := OpenTofu{Root: "/repo", Run: run, Store: kv, Workloads: fakeReaders{log: &[]string{}}, Waits: fastWaiter{}}
	if err := m.Prepare(ctx, e); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("Prepare without a person = %v", err)
	}
	m.Confirm = func(context.Context, string) error { return errors.New("no") }
	if err := m.Prepare(ctx, e); err == nil {
		t.Fatal("Prepare went on after a no")
	}
	if slices.Contains(runs, "apply") {
		t.Fatalf("runs = %v: applied without a yes", runs)
	}
}
