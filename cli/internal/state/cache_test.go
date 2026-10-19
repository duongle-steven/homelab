package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCachedR2CredentialsReusesFreshCredentials(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	calls := 0
	create := func() (R2Credentials, error) {
		calls++
		return R2Credentials{Version: 1, AccessKeyID: "key", SecretAccessKey: "secret", Expiration: now.Add(time.Hour)}, nil
	}
	first, err := CachedR2Credentials(dir, "acc", "bucket", now, create)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CachedR2Credentials(dir, "acc", "bucket", now.Add(40*time.Minute), create)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || second != first {
		t.Fatalf("calls = %d, second = %+v; want one token reused", calls, second)
	}
	info, err := os.Stat(filepath.Join(dir, "homelab-lab", "r2-acc-bucket.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestCachedR2CredentialsRefreshesNearExpiry(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	calls := 0
	create := func() (R2Credentials, error) {
		calls++
		return R2Credentials{Version: 1, AccessKeyID: "key", Expiration: now.Add(time.Duration(calls) * time.Hour)}, nil
	}
	if _, err := CachedR2Credentials(dir, "acc", "bucket", now, create); err != nil {
		t.Fatal(err)
	}
	// 14 minutes left: too little for a long apply
	if _, err := CachedR2Credentials(dir, "acc", "bucket", now.Add(46*time.Minute), create); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want a new token near expiry", calls)
	}
}

func TestCachedR2CredentialsSeparatesBucketsAndIgnoresCorruptCache(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	calls := 0
	create := func() (R2Credentials, error) {
		calls++
		return R2Credentials{Version: 1, AccessKeyID: "key", Expiration: now.Add(time.Hour)}, nil
	}
	if err := os.MkdirAll(filepath.Join(dir, "homelab-lab"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "homelab-lab", "r2-acc-a.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bucket := range []string{"a", "b"} {
		if _, err := CachedR2Credentials(dir, "acc", bucket, now, create); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want one token per bucket", calls)
	}
}

func TestCachedR2CredentialsWithoutDirAlwaysCreates(t *testing.T) {
	calls := 0
	create := func() (R2Credentials, error) { calls++; return R2Credentials{}, nil }
	for range 2 {
		if _, err := CachedR2Credentials("", "acc", "bucket", time.Now(), create); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want no cache without a runtime directory", calls)
	}
	failing := func() (R2Credentials, error) { return R2Credentials{}, errors.New("boom") }
	if _, err := CachedR2Credentials(t.TempDir(), "acc", "bucket", time.Now(), failing); err == nil {
		t.Fatal("a creation error was swallowed")
	}
}
