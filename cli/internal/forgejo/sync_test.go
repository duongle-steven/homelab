package forgejo

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

const callback = "https://ci.example.test/authorize"

func TestSyncCreatesEverything(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	if f.orgs["ops"] != "private" || !f.repo {
		t.Fatalf("org %q, repo %v", f.orgs["ops"], f.repo)
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
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	before, deploy := f.writes, tokens[DeployPath]["token"]
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
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
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	argocd := tokens[ArgoCDPath]["token"]
	tokens[DeployPath] = map[string]string{"token": "lost"}
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
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
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	f.protection["push_whitelist_usernames"] = []any{"deploy", "someone"}
	f.collaborators["argocd"] = "write"
	// Files matching unprotected patterns can be pushed by any writer, outside the allowlist
	f.protection["unprotected_file_patterns"] = "**"
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
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
	if err := Sync(context.Background(), ep, "wrong", memoryTokens{}, callback); err == nil {
		t.Fatal("Sync succeeded with a wrong admin password")
	}
}

// A Kanidm person who logged in as deploy or argocd before the first sync must not become the deploy account
func TestSyncRefusesAnAccountItDidNotCreate(t *testing.T) {
	f, ep := newFakeForgejo(t)
	f.users["deploy"] = &fakeUser{loginName: "deploy", sourceID: 2, password: "kanidm", tokens: map[int64]fakeToken{}}
	err := Sync(context.Background(), ep, f.admin, memoryTokens{}, callback)
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
	err := Sync(context.Background(), ep, f.admin, memoryTokens{}, callback)
	if err == nil || !strings.Contains(err.Error(), "team") {
		t.Fatalf("Sync() = %v", err)
	}
}

// Org ci and its team developers (Woodpecker's sign-in, spec 2026-10-21-woodpecker decision 1): created, then kept
func TestSyncCreatesCIOrg(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	if f.orgs[CIOrg] != "private" {
		t.Fatalf("org ci: %q", f.orgs[CIOrg])
	}
	want := fakeTeam{org: CIOrg, name: CITeam, permission: "write", canCreateOrgRepo: true,
		units: map[string]string{"repo.code": "write", "repo.pulls": "write"}}
	var teams []fakeTeam
	for _, team := range f.orgTeams {
		teams = append(teams, *team)
	}
	if len(teams) != 1 || !reflect.DeepEqual(teams[0], want) {
		t.Fatalf("teams = %+v, want only %+v", teams, want)
	}
	before := f.writes
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	if f.writes != before {
		t.Fatalf("second Sync wrote %d times", f.writes-before)
	}
}

func TestSyncFixesCITeam(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	for _, team := range f.orgTeams {
		team.permission, team.includesAllRepositories, team.units = "admin", true, map[string]string{"repo.code": "admin"}
	}
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	for _, team := range f.orgTeams {
		if team.permission != "write" || team.includesAllRepositories || !reflect.DeepEqual(team.units, map[string]string{"repo.code": "write", "repo.pulls": "write"}) {
			t.Fatalf("team not restored: %+v", team)
		}
	}
}

func woodpeckerApps(f *fakeForgejo) []*fakeApp {
	var apps []*fakeApp
	for _, app := range f.apps {
		if app.name == "woodpecker" {
			apps = append(apps, app)
		}
	}
	return apps
}

func TestSyncCreatesWoodpeckerClient(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	apps := woodpeckerApps(f)
	if len(apps) != 1 || !apps[0].confidential || !reflect.DeepEqual(apps[0].redirects, []string{callback}) {
		t.Fatalf("apps = %+v", apps)
	}
	stored := tokens[WoodpeckerPath]
	if !reflect.DeepEqual(stored, map[string]string{"client_id": apps[0].clientID, "client_secret": apps[0].secret, "redirect_uri": callback}) {
		t.Fatalf("%s holds %v", WoodpeckerPath, stored)
	}
	before := f.writes
	if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
		t.Fatal(err)
	}
	if f.writes != before || !reflect.DeepEqual(tokens[WoodpeckerPath], stored) {
		t.Fatalf("second Sync wrote %d times; %s %v → %v", f.writes-before, WoodpeckerPath, stored, tokens[WoodpeckerPath])
	}
}

func TestSyncReplacesWoodpeckerClient(t *testing.T) {
	for name, change := range map[string]func(f *fakeForgejo, tokens memoryTokens){
		"redirect changed in Forgejo": func(f *fakeForgejo, _ memoryTokens) { woodpeckerApps(f)[0].redirects = []string{"https://evil.test/authorize"} },
		"public client":               func(f *fakeForgejo, _ memoryTokens) { woodpeckerApps(f)[0].confidential = false },
		"OpenBao lost the secret":     func(_ *fakeForgejo, tokens memoryTokens) { delete(tokens, WoodpeckerPath) },
		"another client id stored":    func(_ *fakeForgejo, tokens memoryTokens) { tokens[WoodpeckerPath]["client_id"] = "stale" },
	} {
		t.Run(name, func(t *testing.T) {
			f, ep := newFakeForgejo(t)
			tokens := memoryTokens{}
			if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
				t.Fatal(err)
			}
			change(f, tokens)
			if err := Sync(context.Background(), ep, f.admin, tokens, callback); err != nil {
				t.Fatal(err)
			}
			apps := woodpeckerApps(f)
			if len(apps) != 1 || !apps[0].confidential || !reflect.DeepEqual(apps[0].redirects, []string{callback}) {
				t.Fatalf("apps = %+v", apps)
			}
			if got := tokens[WoodpeckerPath]; got["client_id"] != apps[0].clientID || got["client_secret"] != apps[0].secret {
				t.Fatal("OpenBao does not hold the application's current credentials")
			}
		})
	}
}

func TestSyncRefusesTwoWoodpeckerClients(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := memoryTokens{}
	f.apps[900] = &fakeApp{name: "woodpecker", clientID: "a", secret: "secret-a", confidential: true}
	f.apps[901] = &fakeApp{name: "woodpecker", clientID: "b", secret: "secret-b", confidential: true}
	err := Sync(context.Background(), ep, f.admin, tokens, callback)
	if err == nil || !strings.Contains(err.Error(), "woodpecker") {
		t.Fatalf("Sync() = %v", err)
	}
	if strings.Contains(err.Error(), "secret-") {
		t.Fatal("the error shows a secret")
	}
	if tokens[WoodpeckerPath] != nil {
		t.Fatal("credentials were written")
	}
}

// Org ci never touches ops: sync still refuses a team grant on ops/homelab, and ci's team has no repository of ops
func TestSyncKeepsCIOutOfOps(t *testing.T) {
	f, ep := newFakeForgejo(t)
	if err := Sync(context.Background(), ep, f.admin, memoryTokens{}, callback); err != nil {
		t.Fatal(err)
	}
	for _, team := range f.orgTeams {
		if team.org != CIOrg {
			t.Fatalf("a team in %s: %+v", team.org, team)
		}
	}
}
