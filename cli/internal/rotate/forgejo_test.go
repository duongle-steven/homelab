package rotate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeForgejo stands for Forgejo's API as lab uses it: users' tokens by name
type fakeForgejo struct {
	log     *[]string
	kv      *fakeKV
	tokens  map[string]map[string]string // user → name → token
	failing bool
	creates int
}

func (f *fakeForgejo) NewTokenName(user string, at time.Time) (string, error) {
	base := map[string]string{"deploy": "lab", "argocd": "argocd", "ci-bot": "pipeline"}[user]
	return fmt.Sprintf("%s-%d", base, at.Unix()), nil
}

func (f *fakeForgejo) RotateToken(ctx context.Context, user, newName string) ([]string, error) {
	if f.failing {
		return nil, fmt.Errorf("create %s's token: Forgejo answered with %v", user, f.tokens[user])
	}
	var old []string
	for name := range f.tokens[user] {
		if name != newName {
			old = append(old, name)
		}
	}
	slices.Sort(old)
	if _, ok := f.tokens[user][newName]; !ok {
		f.creates++
		f.tokens[user][newName] = fmt.Sprintf("token-%s-%d", user, f.creates)
	}
	path := map[string]string{"deploy": "forgejo/deploy", "argocd": "argocd/forgejo", "ci-bot": "ci/forgejo-token"}[user]
	return old, f.kv.Write(ctx, path, map[string]string{"username": user, "token": f.tokens[user][newName], "name": newName})
}

func (f *fakeForgejo) DeleteToken(_ context.Context, user, name string) error {
	if f.failing {
		return errors.New("delete failed")
	}
	*f.log = append(*f.log, "delete "+user+"/"+name)
	delete(f.tokens[user], name)
	return nil
}

func (f *fakeForgejo) TokenValid(_ context.Context, user, token string) (bool, error) {
	if f.failing {
		return false, fmt.Errorf("Forgejo refused %s", token)
	}
	for _, t := range f.tokens[user] {
		if t == token {
			return true, nil
		}
	}
	return false, nil
}

func newFakeForgejo(log *[]string, kv *fakeKV) *fakeForgejo {
	return &fakeForgejo{log: log, kv: kv, tokens: map[string]map[string]string{
		"deploy": {"lab": "token-deploy-old"},
		"argocd": {"argocd": "token-argocd-old"},
		"ci-bot": {"pipeline": "token-ci-bot-old"},
	}}
}

var rotatedAt = time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)

// Argo CD reads the new token before the old one goes; a rerun of every phase makes no third token
func TestForgejoTokenOverlap(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "forgejo.argocd-token")
	log := &[]string{}
	kv := newKV(log, map[string]map[string]string{"argocd/forgejo": {"username": "argocd", "token": "token-argocd-old"}})
	api := newFakeForgejo(log, kv)
	m := ForgejoToken{API: api, Store: kv, Workloads: fakeReaders{log: log}, Now: func() time.Time { return rotatedAt }}
	for range 2 {
		if err := m.Prepare(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if api.creates != 1 || kv.data["argocd/forgejo"]["name"] != "argocd-1792810800" {
		t.Fatalf("creates %d, stored %v", api.creates, kv.data["argocd/forgejo"])
	}
	if _, ok := api.tokens["argocd"]["argocd"]; !ok {
		t.Fatal("Prepare ended the old token")
	}
	if err := m.Switch(ctx, e); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(*log, "refresh argocd/argocd-repo-forgejo") {
		t.Fatalf("Switch did %v", *log)
	}
	if err := m.Verify(ctx, e); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := m.Revoke(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := api.tokens["argocd"]["argocd"]; ok || len(api.tokens["argocd"]) != 1 {
		t.Fatalf("tokens after Revoke: %v", api.tokens["argocd"])
	}
	if kv.data[RecordPath(e)]["token-name"] != "" {
		t.Fatalf("the rotation's state outlived it: %v", kv.data[RecordPath(e)])
	}
}

// A new token Forgejo refuses stops the run before the old one goes
func TestForgejoTokenVerifyRefusesABadToken(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "forgejo.deploy-token")
	log := &[]string{}
	kv := newKV(log, map[string]map[string]string{"forgejo/deploy": {"username": "deploy", "token": "token-deploy-old"}})
	api := newFakeForgejo(log, kv)
	m := ForgejoToken{API: api, Store: kv, Workloads: fakeReaders{log: log}, Now: func() time.Time { return rotatedAt }}
	if err := m.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	api.tokens["deploy"]["lab-1792810800"] = "replaced-in-forgejo"
	if err := m.Verify(ctx, e); err == nil || !strings.Contains(err.Error(), "deploy") {
		t.Fatalf("Verify = %v", err)
	}
}
