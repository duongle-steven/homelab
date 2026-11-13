package forgejo

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ciTokens is OpenBao as a first sync finds it: platform/generated-secrets has pushed the webhook's secret
func ciTokens() memoryTokens {
	return memoryTokens{WebhookPath: {"token": "webhook-secret"}}
}

func TestSyncCreatesEverything(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := ciTokens()
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
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
	tokens := ciTokens()
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
	tokens := ciTokens()
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
	tokens := ciTokens()
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
	if err := Sync(context.Background(), ep, "wrong", ciTokens()); err == nil {
		t.Fatal("Sync succeeded with a wrong admin password")
	}
}

// A Kanidm person who logged in as deploy or argocd before the first sync must not become the deploy account
func TestSyncRefusesAnAccountItDidNotCreate(t *testing.T) {
	f, ep := newFakeForgejo(t)
	f.users["deploy"] = &fakeUser{loginName: "deploy", sourceID: 2, password: "kanidm", tokens: map[int64]fakeToken{}}
	err := Sync(context.Background(), ep, f.admin, ciTokens())
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
	err := Sync(context.Background(), ep, f.admin, ciTokens())
	if err == nil || !strings.Contains(err.Error(), "team") {
		t.Fatalf("Sync() = %v", err)
	}
}

// Org ci and its team developers (Forgejo's Kanidm login maps homelab-editors there): created, then kept
func TestSyncCreatesCIOrg(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := ciTokens()
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if f.orgs[CIOrg] != "private" {
		t.Fatalf("org ci: %q", f.orgs[CIOrg])
	}
	want := fakeTeam{org: CIOrg, name: CITeam, permission: "write", canCreateOrgRepo: true,
		units: map[string]string{"repo.code": "write", "repo.pulls": "write"}}
	var teams []fakeTeam
	names := map[string]bool{}
	for _, team := range f.orgTeams {
		names[team.name] = true
		if team.name == CITeam {
			teams = append(teams, *team)
		}
	}
	if len(teams) != 1 || !reflect.DeepEqual(teams[0], want) {
		t.Fatalf("teams = %+v, want only %+v", teams, want)
	}
	// developers and the bot's pipeline (TestSyncCreatesCIBot), nothing else
	if !reflect.DeepEqual(names, map[string]bool{CITeam: true, CIBotTeam: true}) {
		t.Fatalf("org ci's teams: %v", names)
	}
	before := f.writes
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if f.writes != before {
		t.Fatalf("second Sync wrote %d times", f.writes-before)
	}
}

func TestSyncFixesCITeam(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := ciTokens()
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	for _, team := range f.orgTeams {
		if team.name == CITeam {
			team.permission, team.includesAllRepositories, team.units = "admin", true, map[string]string{"repo.code": "admin"}
		}
	}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	for _, team := range f.orgTeams {
		if team.name == CITeam && (team.permission != "write" || team.includesAllRepositories || !reflect.DeepEqual(team.units, map[string]string{"repo.code": "write", "repo.pulls": "write"})) {
			t.Fatalf("team not restored: %+v", team)
		}
	}
}

func TestSyncKeepsCIOutOfOps(t *testing.T) {
	f, ep := newFakeForgejo(t)
	if err := Sync(context.Background(), ep, f.admin, ciTokens()); err != nil {
		t.Fatal(err)
	}
	for _, team := range f.orgTeams {
		if team.org != CIOrg {
			t.Fatalf("a team in %s: %+v", team.org, team)
		}
	}
}

// An org made public by hand is made private again (ops/homelab's org and the CI's)
func TestSyncMakesOrgsPrivate(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := ciTokens()
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	f.orgs[Org], f.orgs[CIOrg] = "public", "limited"
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if f.orgs[Org] != "private" || f.orgs[CIOrg] != "private" {
		t.Fatalf("orgs = %v", f.orgs)
	}
}

// The golden path's bot (step 10f): a restricted local user in org ci's team pipeline, which has write on code of every
// repository of the org (clones and commit statuses), and its token in OpenBao for namespace ci
func TestSyncCreatesCIBot(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := ciTokens()
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if u := f.users[CIBot]; u == nil || !u.restricted || u.sourceID != 0 {
		t.Fatalf("user %s: %+v", CIBot, u)
	}
	var team *fakeTeam
	for _, tm := range f.orgTeams {
		if tm.org == CIOrg && tm.name == CIBotTeam {
			team = tm
		}
	}
	if team == nil || team.permission != "write" || !team.includesAllRepositories || team.canCreateOrgRepo ||
		!reflect.DeepEqual(team.units, map[string]string{"repo.code": "write"}) || !reflect.DeepEqual(team.members, []string{CIBot}) {
		t.Fatalf("team %s/%s: %+v", CIOrg, CIBotTeam, team)
	}
	token := tokens[CIBotPath]["token"]
	found := false
	for _, tk := range f.users[CIBot].tokens {
		found = found || (tk.sha1 == token && reflect.DeepEqual(tk.scopes, []string{"write:repository"}))
	}
	if !found {
		t.Fatalf("%s holds %q, not ci-bot's write:repository token", CIBotPath, token)
	}
	// Someone widened the team or added a person: restored
	team.units["repo.releases"] = "write"
	team.members = append(team.members, "someone")
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(team.units, map[string]string{"repo.code": "write"}) || !reflect.DeepEqual(team.members, []string{CIBot}) {
		t.Fatalf("team not restored: %+v", team)
	}
}

func TestSyncRefusesAKanidmCIBot(t *testing.T) {
	f, ep := newFakeForgejo(t)
	f.users[CIBot] = &fakeUser{loginName: CIBot, sourceID: 2, password: "kanidm", tokens: map[int64]fakeToken{}}
	err := Sync(context.Background(), ep, f.admin, ciTokens())
	if err == nil || !strings.Contains(err.Error(), "not a local account") {
		t.Fatalf("Sync() = %v", err)
	}
}

// Org ci's webhook to Argo Events: push and pull request events, the bearer secret from OpenBao; Forgejo never shows
// the header, so sync rewrites it only when the secret changed or the hook did, and leaves other hooks alone
func TestSyncCIWebhook(t *testing.T) {
	f, ep := newFakeForgejo(t)
	tokens := ciTokens()
	f.orgs[CIOrg] = "private"
	f.hooks[500] = &fakeHook{org: CIOrg, url: "https://elsewhere.test/hook", events: []string{"push"}, active: true, header: "kept"}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	ours := func() []*fakeHook {
		var list []*fakeHook
		for _, h := range f.hooks {
			if h.url == WebhookURL {
				list = append(list, h)
			}
		}
		return list
	}
	hooks := ours()
	if len(hooks) != 1 || hooks[0].org != CIOrg || hooks[0].kind != "forgejo" || !hooks[0].active || hooks[0].contentType != "json" ||
		!reflect.DeepEqual(hooks[0].events, []string{"push", "pull_request"}) || hooks[0].header != "Bearer webhook-secret" {
		t.Fatalf("hooks %+v", hooks)
	}
	if f.hooks[500].header != "kept" {
		t.Fatal("another hook was changed")
	}

	tokens[WebhookPath] = map[string]string{"token": "rotated"}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if hooks := ours(); len(hooks) != 1 || hooks[0].header != "Bearer rotated" {
		t.Fatalf("after a new secret: %+v", hooks)
	}

	ours()[0].events = []string{"push"}
	f.nextID++
	f.hooks[f.nextID] = &fakeHook{org: CIOrg, url: WebhookURL, kind: "forgejo", events: []string{"push"}, header: "stale"}
	if err := Sync(context.Background(), ep, f.admin, tokens); err != nil {
		t.Fatal(err)
	}
	if hooks := ours(); len(hooks) != 1 || !reflect.DeepEqual(hooks[0].events, []string{"push", "pull_request"}) || hooks[0].header != "Bearer rotated" {
		t.Fatalf("after a change in Forgejo: %+v", hooks)
	}
}

// A first install: sync waits for platform/generated-secrets to push the webhook's secret, until its context ends
func TestSyncWaitsForTheWebhookSecret(t *testing.T) {
	f, ep := newFakeForgejo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := Sync(ctx, ep, f.admin, memoryTokens{})
	if err == nil || !strings.Contains(err.Error(), WebhookPath) {
		t.Fatalf("Sync() = %v", err)
	}
}

// Woodpecker's OAuth2 application (step 10e) goes; any other application stays
func TestSyncDeletesWoodpeckerClient(t *testing.T) {
	f, ep := newFakeForgejo(t)
	f.apps[900] = &fakeApp{name: "woodpecker", clientID: "a", secret: "secret-a", confidential: true}
	f.apps[901] = &fakeApp{name: "other", clientID: "b", secret: "secret-b", confidential: true}
	if err := Sync(context.Background(), ep, f.admin, ciTokens()); err != nil {
		t.Fatal(err)
	}
	if f.apps[900] != nil || f.apps[901] == nil {
		t.Fatalf("apps %v", f.apps)
	}
}
