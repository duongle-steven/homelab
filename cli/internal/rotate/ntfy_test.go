package rotate

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

const oldToken = "0ldtokenabcdefghijklmnopqrst"

func ntfyKV(log *[]string, e Entry) *fakeKV {
	data := map[string]map[string]string{}
	for _, p := range e.Paths {
		data[p] = map[string]string{"token": oldToken}
	}
	return newKV(log, data)
}

// The new token is listed beside the old one until the readers moved and it works; only then is the old one dropped
// (ntfy removes a provisioned token no longer listed when it restarts)
func TestNtfyOverlapRevokesAfterVerify(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "ntfy.alerts-token")
	log := &[]string{}
	kv := ntfyKV(log, e)
	var checked []string
	n := 0
	m := NtfyOverlap{Sleep: noSleep, Store: kv, Workloads: fakeReaders{log: log}, Random: fixed(&n), Check: func(_ context.Context, user, token string) error {
		checked = append(checked, user+":"+token)
		return nil
	}}
	if err := m.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	for _, p := range e.Paths {
		if got := kv.data[p]; got["token"] != newValue || got["previous_token"] != oldToken {
			t.Fatalf("%s after Prepare = %v, want the new token and the old one as previous_token", p, got)
		}
	}
	// A rerun after an interruption keeps the same pair: the old token stays previous_token
	if err := m.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	if n != 1 || kv.data[e.Paths[0]]["previous_token"] != oldToken {
		t.Fatalf("a second Prepare made %d values, data %v", n, kv.data[e.Paths[0]])
	}
	if err := m.Switch(ctx, e); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(*log, "refresh ntfy/ntfy-auth") || !slices.Contains(*log, "refresh monitoring-system/ntfy") || !slices.Contains(*log, "restart ntfy/ntfy") {
		t.Fatalf("Switch did %v", *log)
	}
	if slices.ContainsFunc(*log, func(s string) bool { return strings.HasPrefix(s, "restart monitoring-system") }) {
		t.Fatalf("Alertmanager reads its credentials file at every notification, no restart: %v", *log)
	}
	if err := m.Verify(ctx, e); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(checked, []string{"alerts:" + newValue}) {
		t.Fatalf("Verify checked %v, want the new token", checked)
	}
	*log = nil
	if err := m.Revoke(ctx, e); err != nil {
		t.Fatal(err)
	}
	for _, p := range e.Paths {
		if got := kv.data[p]; got["token"] != newValue || got["previous_token"] != "" {
			t.Fatalf("%s after Revoke = %v, want only the new token", p, got)
		}
	}
	if !slices.Contains(*log, "refresh ntfy/ntfy-auth") || !slices.Contains(*log, "restart ntfy/ntfy") || !slices.Contains(*log, "wait ntfy/ntfy") {
		t.Fatalf("Revoke did %v: ntfy drops the old token only when it restarts", *log)
	}
	if kv.data[RecordPath(e)][pendingKey] != "" {
		t.Fatal("the pending value outlived the rotation")
	}
}

// A failing check keeps the old token listed: Verify fails and Revoke is never reached
func TestNtfyOverlapVerifyFailsOnABadToken(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "ntfy.alerts-token")
	log := &[]string{}
	n := 0
	m := NtfyOverlap{Sleep: noSleep, Store: ntfyKV(log, e), Workloads: fakeReaders{log: log}, Random: fixed(&n), Check: func(context.Context, string, string) error {
		return errors.New("ntfy answered 401")
	}}
	if err := m.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(ctx, e); err == nil || strings.Contains(err.Error(), newValue) {
		t.Fatalf("Verify = %v, want an error without the token", err)
	}
}

// People re-enter the phone token in their apps: its old token goes only once a person confirms they did
func TestNtfyOverlapPhoneWaitsForPeople(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "ntfy.phone-token")
	log := &[]string{}
	n := 0
	kv := ntfyKV(log, e)
	m := NtfyOverlap{Sleep: noSleep, Store: kv, Workloads: fakeReaders{log: log}, Random: fixed(&n), Check: func(context.Context, string, string) error { return nil }}
	if err := m.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := m.Revoke(ctx, e); !errors.Is(err, ErrWaitingForPeople) || !strings.Contains(err.Error(), "lab secrets rotate ntfy.phone-token") {
		t.Fatalf("Revoke without a person = %v, want an error saying how to finish", err)
	}
	if kv.data[e.Paths[0]]["previous_token"] != oldToken {
		t.Fatal("the old phone token was dropped without a person's confirmation")
	}
	var asked string
	m.Confirm = func(_ context.Context, question string) error { asked = question; return nil }
	if err := m.Revoke(ctx, e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asked, "phone") || kv.data[e.Paths[0]]["previous_token"] != "" {
		t.Fatalf("asked %q, data %v", asked, kv.data[e.Paths[0]])
	}
}

// Alertmanager reads its token from a mounted file, which the kubelet updates within about a minute of the Secret:
// the old token is dropped only once that has passed, so no notification meets a 401
func TestNtfyOverlapWaitsForMountedFiles(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "ntfy.alerts-token")
	log := &[]string{}
	n := 0
	var slept []time.Duration
	m := NtfyOverlap{Store: ntfyKV(log, e), Workloads: fakeReaders{log: log}, Random: fixed(&n),
		Check: func(context.Context, string, string) error { return nil }, Sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }}
	if err := m.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := m.Revoke(ctx, e); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 1 || slept[0] < 90*time.Second {
		t.Fatalf("slept %v before dropping the old token, want at least 90s for Alertmanager's file", slept)
	}
}

func noSleep(context.Context, time.Duration) error { return nil }
