package forgejo

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func tokenNames(f *fakeForgejo, user string) []string {
	var names []string
	for _, tk := range f.users[user].tokens {
		names = append(names, tk.name)
	}
	return names
}

// A rotation creates the new token beside the old one; the old one goes only when DeleteToken is called (once the
// readers moved), and a rerun after an interruption makes no third token
func TestRotateTokenKeepsOldUntilDeleted(t *testing.T) {
	ctx := context.Background()
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(ctx, ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	before := tokens[ArgoCDPath]["token"]
	old, err := RotateToken(ctx, ep, f.admin, tokens, "argocd", "argocd-1792800000")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old, []string{"argocd"}) {
		t.Fatalf("old = %v, want the token sync made", old)
	}
	stored := tokens[ArgoCDPath]
	if stored["token"] == before || stored["name"] != "argocd-1792800000" || stored["username"] != "argocd" {
		t.Fatalf("%s holds %v", ArgoCDPath, stored)
	}
	for _, token := range []string{before, stored["token"]} {
		if ok, err := TokenValid(ctx, ep, token); err != nil || !ok {
			t.Fatalf("a token stopped working before DeleteToken: %v %v", ok, err)
		}
	}
	// Interrupted after Forgejo made the token: the same name is found, nothing new is made
	again, err := RotateToken(ctx, ep, f.admin, tokens, "argocd", "argocd-1792800000")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, old) || len(f.users["argocd"].tokens) != 2 || tokens[ArgoCDPath]["token"] != stored["token"] {
		t.Fatalf("rerun: old %v, tokens %v", again, tokenNames(f, "argocd"))
	}
	if err := DeleteToken(ctx, ep, f.admin, "argocd", "argocd"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := TokenValid(ctx, ep, before); ok {
		t.Fatal("the old token still works after DeleteToken")
	}
	if ok, _ := TokenValid(ctx, ep, stored["token"]); !ok {
		t.Fatal("DeleteToken ended the new token")
	}
	// Deleting a token that is gone already is not an error (a rerun of the revoke)
	if err := DeleteToken(ctx, ep, f.admin, "argocd", "argocd"); err != nil {
		t.Fatal(err)
	}
}

// Interrupted after Forgejo made the token but before OpenBao kept it: Forgejo never shows a token twice, so the
// named token is replaced rather than left behind unknown
func TestRotateTokenReplacesAnUnstoredNewToken(t *testing.T) {
	ctx := context.Background()
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(ctx, ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	if _, err := RotateToken(ctx, ep, f.admin, tokens, "deploy", "lab-1792800000"); err != nil {
		t.Fatal(err)
	}
	lost := tokens[DeployPath]["token"]
	tokens[DeployPath] = map[string]string{"username": "deploy", "token": "never-stored"}
	if _, err := RotateToken(ctx, ep, f.admin, tokens, "deploy", "lab-1792800000"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := TokenValid(ctx, ep, lost); ok {
		t.Fatal("the token OpenBao never kept still works")
	}
	if ok, _ := TokenValid(ctx, ep, tokens[DeployPath]["token"]); !ok || len(f.users["deploy"].tokens) != 2 {
		t.Fatalf("tokens %v", tokenNames(f, "deploy"))
	}
}

// lab infra forgejo sync keeps a rotated token whatever its name: it replaces only a token that does not work
func TestSyncKeepsRotatedToken(t *testing.T) {
	ctx := context.Background()
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(ctx, ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	if _, err := RotateToken(ctx, ep, f.admin, tokens, "deploy", "lab-1792800000"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteToken(ctx, ep, f.admin, "deploy", "lab"); err != nil {
		t.Fatal(err)
	}
	rotated := tokens[DeployPath]["token"]
	if err := Sync(ctx, ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	if tokens[DeployPath]["token"] != rotated || !reflect.DeepEqual(tokenNames(f, "deploy"), []string{"lab-1792800000"}) {
		t.Fatalf("sync undid the rotation: %v, tokens %v", tokens[DeployPath], tokenNames(f, "deploy"))
	}
}

// Woodpecker's OAuth2 client gets a new secret: Forgejo has no overlap, so the old one stops at once
func TestRotateWoodpeckerClient(t *testing.T) {
	ctx := context.Background()
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(ctx, ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	before := tokens[WoodpeckerPath]
	if err := RotateWoodpeckerClient(ctx, ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	after := tokens[WoodpeckerPath]
	apps := woodpeckerApps(f)
	if len(apps) != 1 || after["client_secret"] == before["client_secret"] || after["client_secret"] != apps[0].secret ||
		after["client_id"] != before["client_id"] || after["redirect_uri"] != callback {
		t.Fatalf("before %v, after %v, apps %+v", before, after, apps)
	}
	if ok, err := ClientSecretCurrent(ctx, ep, after["client_id"], after["client_secret"], callback); err != nil || !ok {
		t.Fatalf("the new secret is not current: %v %v", ok, err)
	}
}

// Without Woodpecker's client (sync never ran) there is nothing to rotate: an error says what to run first
func TestRotateWoodpeckerClientNeedsSync(t *testing.T) {
	f, ep := newFakeForgejo(t)
	err := RotateWoodpeckerClient(context.Background(), ep, f.admin, memoryTokens{})
	if err == nil || !strings.Contains(err.Error(), "lab infra forgejo sync") {
		t.Fatalf("err = %v", err)
	}
}
