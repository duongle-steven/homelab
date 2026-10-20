package forgejo

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestSyncCreatesEverything(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if !f.org || !f.repo {
		t.Fatalf("org %v, repo %v", f.org, f.repo)
	}
	for name, permission := range map[string]string{"deploy": "write", "argocd": "read"} {
		if u := f.users[name]; u == nil || !u.restricted {
			t.Errorf("user %s: %+v", name, u)
		}
		if f.collaborators[name] != permission {
			t.Errorf("%s is %q on ops/homelab, want %s", name, f.collaborators[name], permission)
		}
	}
	if got := f.protection["push_whitelist_usernames"]; !reflect.DeepEqual(got, []any{"deploy"}) || f.protection["enable_push_whitelist"] != true {
		t.Errorf("protection = %v", f.protection)
	}
	for path, scope := range map[string]string{DeployPath: "write:repository", ArgoCDPath: "read:repository"} {
		token := tokens[path]["token"]
		found := false
		for _, u := range f.users {
			for _, tk := range u.tokens {
				if tk.sha1 == token && reflect.DeepEqual(tk.scopes, []string{scope}) {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s holds %q, not a %s token", path, token, scope)
		}
	}
}

func TestSyncTwiceWritesNothing(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	before, deploy := f.writes, tokens[DeployPath]["token"]
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if f.writes != before || tokens[DeployPath]["token"] != deploy {
		t.Fatalf("second Sync wrote %d times, deploy token %q → %q", f.writes-before, deploy, tokens[DeployPath]["token"])
	}
}

// Review Focus 3: OpenBao holds a token Forgejo no longer knows (Forgejo's data lost): it is replaced
func TestSyncReplacesAnInvalidToken(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	argocd := tokens[ArgoCDPath]["token"]
	tokens[DeployPath] = map[string]string{"token": "lost"}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if got := tokens[DeployPath]["token"]; got == "lost" || got == "" {
		t.Fatalf("deploy token = %q", got)
	}
	if tokens[ArgoCDPath]["token"] != argocd {
		t.Fatal("a valid token was replaced")
	}
	if n := len(f.users["deploy"].tokens); n != 1 {
		t.Fatalf("deploy has %d tokens, want only the new one", n)
	}
}

func TestSyncRestoresTheProtection(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	f.protection["push_whitelist_usernames"] = []any{"deploy", "someone"}
	f.collaborators["argocd"] = "write"
	// Files matching unprotected patterns can be pushed by any writer, outside the allowlist
	f.protection["unprotected_file_patterns"] = "**"
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if got := f.protection["push_whitelist_usernames"]; !reflect.DeepEqual(got, []any{"deploy"}) {
		t.Errorf("push allowlist = %v", got)
	}
	if f.protection["unprotected_file_patterns"] != "" {
		t.Errorf("unprotected file patterns = %v", f.protection["unprotected_file_patterns"])
	}
	if f.collaborators["argocd"] != "read" {
		t.Errorf("argocd is %s", f.collaborators["argocd"])
	}
}

func TestSyncRefusesAWrongAdminPassword(t *testing.T) {
	_, ep := newFakeForgejo(t)
	if err := Sync(context.Background(), ep, "wrong", memoryTokens{}); err == nil {
		t.Fatal("Sync succeeded with a wrong admin password")
	}
}

// A Kanidm person who logged in as deploy or argocd before the first sync must not become the deploy account
func TestSyncRefusesAnAccountItDidNotCreate(t *testing.T) {
	f, ep := newFakeForgejo(t)
	f.users["deploy"] = &fakeUser{loginName: "deploy", sourceID: 2, password: "kanidm", tokens: map[int64]fakeToken{}}
	err := Sync(context.Background(), ep, f.admin, memoryTokens{})
	if err == nil || !strings.Contains(err.Error(), "not a local account") {
		t.Fatalf("Sync() = %v", err)
	}
	if f.users["deploy"].restricted || f.collaborators["deploy"] != "" {
		t.Fatal("the existing account was changed")
	}
}

// A team in org ops granting more than sync's permission cannot be fixed by sync: it fails instead of re-adding the
// collaborator on every run
func TestSyncRefusesATeamGrant(t *testing.T) {
	f, ep := newFakeForgejo(t)
	f.teams["argocd"] = "admin"
	err := Sync(context.Background(), ep, f.admin, memoryTokens{})
	if err == nil || !strings.Contains(err.Error(), "team") {
		t.Fatalf("Sync() = %v", err)
	}
}
