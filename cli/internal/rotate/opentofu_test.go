package rotate

import (
	"context"
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
	m := OpenTofu{Root: "/repo", Run: run, Store: kv, Workloads: fakeReaders{log: log}, Waits: fastWaiter{}}
	for range 2 {
		if err := m.Prepare(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
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
	m := OpenTofu{Root: "/repo", Run: func(context.Context, string, ...string) error { return nil }, Store: kv, Workloads: fakeReaders{log: log}, Waits: fastWaiter{fail: true}}
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
