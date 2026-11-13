// Package forgejo provisions the GitOps repository ops/homelab on Forgejo and deploys to it, and provisions org ci, the
// CI's (upstream nixos's lab infra forgejo; ci's teams live there, so no team grants anything on ops/homelab): its
// bot and its webhook to Argo Events (step 10f).
package forgejo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"reflect"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
)

const (
	Org        = "ops"
	Repo       = "homelab"
	Branch     = "master"
	DeployPath = "forgejo/deploy"
	ArgoCDPath = "argocd/forgejo"

	// CIOrg holds the repositories the golden path builds (platform/ci); Forgejo's Kanidm login source maps
	// homelab-admins to its Owners and homelab-editors to CITeam
	CIOrg  = "ci"
	CITeam = "developers"
	// CIBot clones every repository of org ci and reports the golden path's commit statuses, through team CIBotTeam;
	// its token is namespace ci's (CIBotPath)
	CIBot     = "ci-bot"
	CIBotTeam = "pipeline"
	CIBotPath = "ci/forgejo-token"
	// WebhookPath holds org ci's webhook secret (platform/generated-secrets), WebhookAppliedPath which hook sync gave
	// which secret (Forgejo never shows a hook's authorization header)
	WebhookPath        = "ci/generated/webhook"
	WebhookAppliedPath = "ci/forgejo-webhook"
	// WebhookURL is Argo Events' EventSource (platform/ci's values.yaml: webhook.port and webhook.path)
	WebhookURL = "http://forgejo-eventsource-svc.ci:12000/forgejo"
	// woodpeckerApp is step 10e's OAuth2 application, which sync deletes
	woodpeckerApp = "woodpecker"
)

// Tokens is where Sync keeps the accounts' tokens (OpenBao).
type Tokens interface {
	Read(ctx context.Context, path string) (map[string]string, error)
	Write(ctx context.Context, path string, data map[string]string) error
}

type account struct {
	name       string
	permission sdk.AccessMode
	token      string
	scope      sdk.AccessTokenScope
	path       string
	// check is a path a valid token reads (GET /user needs read:user, which no account's scope has)
	check string
}

var accounts = []account{
	{name: "deploy", permission: sdk.AccessModeWrite, token: "lab", scope: sdk.AccessTokenScopeRepositoryWrite, path: DeployPath, check: opsCheck},
	{name: "argocd", permission: sdk.AccessModeRead, token: "argocd", scope: sdk.AccessTokenScopeRepositoryRead, path: ArgoCDPath, check: opsCheck},
}

const opsCheck = "/api/v1/repos/" + Org + "/" + Repo

// ciBot's token writes the commit statuses of org ci's repositories (Forgejo asks for write on code)
var ciBot = account{name: CIBot, token: "pipeline", scope: sdk.AccessTokenScopeRepositoryWrite, path: CIBotPath, check: "/api/v1/repos/search?limit=1"}

// Sync makes orgs ops and ci match the design: what is right is kept, what is wrong is fixed, what is missing is
// created; step 10e's Woodpecker OAuth client is deleted. It waits for Forgejo, and for the webhook's secret in
// OpenBao, until ctx ends.
func Sync(ctx context.Context, ep Endpoint, adminPassword string, tokens Tokens) error {
	if err := waitReady(ctx, ep); err != nil {
		return err
	}
	admin, err := newClient(ctx, ep, sdk.SetBasicAuth("forgejo_admin", adminPassword))
	if err != nil {
		return err
	}
	if err := ensureRepository(admin); err != nil {
		return err
	}
	for _, a := range accounts {
		if err := ensureUser(admin, a.name); err != nil {
			return err
		}
		if err := ensureCollaborator(admin, a); err != nil {
			return err
		}
	}
	if err := ensureProtection(admin); err != nil {
		return err
	}
	for _, a := range accounts {
		if err := ensureToken(ctx, ep, admin, tokens, a); err != nil {
			return err
		}
	}
	if err := ensureCIOrg(admin); err != nil {
		return err
	}
	if err := ensureUser(admin, CIBot); err != nil {
		return err
	}
	if err := ensureCIBotTeam(admin); err != nil {
		return err
	}
	if err := ensureToken(ctx, ep, admin, tokens, ciBot); err != nil {
		return err
	}
	if err := deleteWoodpeckerClient(admin); err != nil {
		return err
	}
	return ensureCIWebhook(ctx, admin, tokens)
}

func newClient(ctx context.Context, ep Endpoint, auth sdk.ClientOption) (*sdk.Client, error) {
	client, err := sdk.NewClient(ep.URL, auth, sdk.SetContext(ctx), sdk.SetHTTPClient(ep.HTTP))
	if err != nil {
		return nil, fmt.Errorf("Forgejo client: %w", err)
	}
	return client, nil
}

func waitReady(ctx context.Context, ep Endpoint) error {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL+"/api/healthz", nil)
		if err != nil {
			return err
		}
		if resp, err := ep.HTTP.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("Forgejo at %s is not ready: %w", ep.URL, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
}

func notFound(resp *sdk.Response) bool { return resp != nil && resp.StatusCode == http.StatusNotFound }

// ensureOrg creates org name private, or makes it private again (someone changed its visibility in Forgejo)
func ensureOrg(c *sdk.Client, name string) error {
	org, resp, err := c.GetOrg(name)
	if err != nil {
		if !notFound(resp) {
			return fmt.Errorf("org %s: %w", name, err)
		}
		if _, _, err := c.CreateOrg(sdk.CreateOrgOption{Name: name, Visibility: sdk.VisibleTypePrivate}); err != nil {
			return fmt.Errorf("create org %s: %w", name, err)
		}
		return nil
	}
	if org.Visibility == string(sdk.VisibleTypePrivate) {
		return nil
	}
	if _, err := c.EditOrg(name, sdk.EditOrgOption{Visibility: sdk.VisibleTypePrivate}); err != nil {
		return fmt.Errorf("make org %s private: %w", name, err)
	}
	return nil
}

func ensureRepository(c *sdk.Client) error {
	if err := ensureOrg(c, Org); err != nil {
		return err
	}
	if _, resp, err := c.GetRepo(Org, Repo); err != nil {
		if !notFound(resp) {
			return fmt.Errorf("repository %s/%s: %w", Org, Repo, err)
		}
		if _, _, err := c.CreateOrgRepo(Org, sdk.CreateRepoOption{Name: Repo, Private: true, DefaultBranch: Branch}); err != nil {
			return fmt.Errorf("create repository %s/%s: %w", Org, Repo, err)
		}
	}
	return nil
}

func randomPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ensureUser creates a restricted local user (it sees only the repositories it collaborates on) with a password
// nobody keeps: ensureToken sets a new one whenever it needs to log in as the user.
func ensureUser(c *sdk.Client, name string) error {
	// The SDK returns an empty value, not nil, with a 404: the status decides
	user, resp, err := c.GetUserInfo(name)
	if err != nil && !notFound(resp) {
		return fmt.Errorf("user %s: %w", name, err)
	}
	if notFound(resp) {
		password, err := randomPassword()
		if err != nil {
			return err
		}
		never := false
		private := sdk.VisibleTypePrivate
		if _, _, err := c.AdminCreateUser(sdk.CreateUserOption{
			Username: name, LoginName: name, Email: name + "@noreply.invalid", Password: password,
			MustChangePassword: &never, Visibility: &private,
		}); err != nil {
			return fmt.Errorf("create user %s: %w", name, err)
		}
	} else if user.SourceID != 0 {
		// Someone signed in with Kanidm under this name before sync created it: never turn that person into the
		// deploy account (their Kanidm login would keep working)
		return fmt.Errorf("user %s exists and is not a local account (authentication source %d): rename or delete it in Forgejo, then run sync again", name, user.SourceID)
	} else if user.Restricted {
		return nil
	}
	restricted := true
	if _, err := c.AdminEditUser(name, sdk.EditUserOption{LoginName: name, Restricted: &restricted}); err != nil {
		return fmt.Errorf("restrict user %s: %w", name, err)
	}
	return nil
}

func ensureCollaborator(c *sdk.Client, a account) error {
	permission := func() (sdk.AccessMode, error) {
		result, resp, err := c.CollaboratorPermission(Org, Repo, a.name)
		if err != nil && !notFound(resp) {
			return "", fmt.Errorf("%s's permission on %s/%s: %w", a.name, Org, Repo, err)
		}
		if notFound(resp) || result == nil {
			return sdk.AccessModeNone, nil
		}
		return result.Permission, nil
	}
	current, err := permission()
	if err != nil || current == a.permission {
		return err
	}
	desired := a.permission
	if _, err := c.AddCollaborator(Org, Repo, a.name, sdk.AddCollaboratorOption{Permission: &desired}); err != nil {
		return fmt.Errorf("make %s a %s collaborator on %s/%s: %w", a.name, a.permission, Org, Repo, err)
	}
	// Forgejo reports the highest grant: more than the collaborator's comes from a team (sync owns org ops; none
	// should grant anything)
	if current, err = permission(); err != nil {
		return err
	}
	if current != a.permission {
		return fmt.Errorf("%s has %s on %s/%s through a team or the org, not only %s as a collaborator: remove that grant in Forgejo", a.name, current, Org, Repo, a.permission)
	}
	return nil
}

// ensureProtection lets only deploy push to master; a protected branch cannot be deleted, and Forgejo refuses force
// pushes to it (the sandbox tests prove both).
func ensureProtection(c *sdk.Client) error {
	allow := []string{"deploy"}
	current, resp, err := c.GetBranchProtection(Org, Repo, Branch)
	if err != nil && !notFound(resp) {
		return fmt.Errorf("protection of %s: %w", Branch, err)
	}
	if notFound(resp) {
		_, _, err := c.CreateBranchProtection(Org, Repo, sdk.CreateBranchProtectionOption{
			RuleName: Branch, EnablePush: true, EnablePushWhitelist: true, PushWhitelistUsernames: allow,
		})
		if err != nil {
			return fmt.Errorf("protect %s: %w", Branch, err)
		}
		return nil
	}
	if current.EnablePush && current.EnablePushWhitelist && reflect.DeepEqual(current.PushWhitelistUsernames, allow) &&
		len(current.PushWhitelistTeams) == 0 && !current.PushWhitelistDeployKeys &&
		current.ProtectedFilePatterns == "" && current.UnprotectedFilePatterns == "" {
		return nil
	}
	// Unprotected file patterns let any writer push matching files outside the allowlist
	on, off, none := true, false, ""
	_, _, err = c.EditBranchProtection(Org, Repo, Branch, sdk.EditBranchProtectionOption{
		EnablePush: &on, EnablePushWhitelist: &on, PushWhitelistUsernames: allow, PushWhitelistTeams: []string{}, PushWhitelistDeployKeys: &off,
		ProtectedFilePatterns: &none, UnprotectedFilePatterns: &none,
	})
	if err != nil {
		return fmt.Errorf("restore the protection of %s: %w", Branch, err)
	}
	return nil
}

// valid tells whether token still reads what its account reads (account.check).
func valid(ctx context.Context, ep Endpoint, token, check string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL+check, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "token "+token)
	resp, err := ep.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("check a token against Forgejo: %w", err)
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("check a token against Forgejo: %s", resp.Status)
}

func ensureToken(ctx context.Context, ep Endpoint, admin *sdk.Client, tokens Tokens, a account) error {
	stored, err := tokens.Read(ctx, a.path)
	if err != nil {
		return err
	}
	if token := stored["token"]; token != "" {
		ok, err := valid(ctx, ep, token, a.check)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	password, err := randomPassword()
	if err != nil {
		return err
	}
	never := false
	if _, err := admin.AdminEditUser(a.name, sdk.EditUserOption{LoginName: a.name, Password: password, MustChangePassword: &never}); err != nil {
		return fmt.Errorf("reset %s's password: %w", a.name, err)
	}
	user, err := newClient(ctx, ep, sdk.SetBasicAuth(a.name, password))
	if err != nil {
		return err
	}
	existing, _, err := user.ListAccessTokens(a.name, sdk.ListAccessTokensOptions{})
	if err != nil {
		return fmt.Errorf("list %s's tokens: %w", a.name, err)
	}
	for _, t := range existing {
		if t.Name == a.token {
			if _, err := user.DeleteAccessToken(a.name, t.ID); err != nil {
				return fmt.Errorf("delete %s's old token: %w", a.name, err)
			}
		}
	}
	created, _, err := user.CreateAccessToken(a.name, sdk.CreateAccessTokenOption{Name: a.token, Scopes: []sdk.AccessTokenScope{a.scope}})
	if err != nil {
		return fmt.Errorf("create %s's token: %w", a.name, err)
	}
	if created.Token == "" {
		return fmt.Errorf("Forgejo returned an empty token for %s", a.name)
	}
	return tokens.Write(ctx, a.path, map[string]string{"username": a.name, "token": created.Token})
}

// ensureCIOrg creates org ci and its team developers: write on code and pull requests of the repositories it is given
// or creates, never all of the org's
func ensureCIOrg(c *sdk.Client) error {
	if err := ensureOrg(c, CIOrg); err != nil {
		return err
	}
	_, err := ensureTeam(c, CITeam, true, false, map[string]string{"repo.code": "write", "repo.pulls": "write"})
	return err
}

// ensureCIBotTeam keeps org ci's team pipeline: write on code of every repository of the org (clones, commit
// statuses), creating none, and ci-bot its only member
func ensureCIBotTeam(c *sdk.Client) error {
	id, err := ensureTeam(c, CIBotTeam, false, true, map[string]string{"repo.code": "write"})
	if err != nil {
		return err
	}
	var members []*sdk.User
	for page := 1; ; page++ {
		list, _, err := c.ListTeamMembers(id, sdk.ListTeamMembersOptions{ListOptions: sdk.ListOptions{Page: page, PageSize: 50}})
		if err != nil {
			return fmt.Errorf("members of team %s/%s: %w", CIOrg, CIBotTeam, err)
		}
		members = append(members, list...)
		if len(list) < 50 {
			break
		}
	}
	bot := false
	for _, m := range members {
		if m.UserName == CIBot {
			bot = true
			continue
		}
		if _, err := c.RemoveTeamMember(id, m.UserName); err != nil {
			return fmt.Errorf("remove %s from team %s/%s: %w", m.UserName, CIOrg, CIBotTeam, err)
		}
	}
	if !bot {
		if _, err := c.AddTeamMember(id, CIBot); err != nil {
			return fmt.Errorf("add %s to team %s/%s: %w", CIBot, CIOrg, CIBotTeam, err)
		}
	}
	return nil
}

// ensureTeam keeps org ci's team name with write on units, creating org repositories or not, on all of the org's
// repositories or only those it is given; it returns the team's id
func ensureTeam(c *sdk.Client, name string, canCreate, all bool, units map[string]string) (int64, error) {
	var team *sdk.Team
	for page := 1; team == nil; page++ {
		teams, _, err := c.ListOrgTeams(CIOrg, sdk.ListTeamsOptions{ListOptions: sdk.ListOptions{Page: page, PageSize: 50}})
		if err != nil {
			return 0, fmt.Errorf("teams of org %s: %w", CIOrg, err)
		}
		for _, t := range teams {
			if t.Name == name {
				team = t
			}
		}
		if len(teams) < 50 {
			break
		}
	}
	if team == nil {
		created, _, err := c.CreateTeam(CIOrg, sdk.CreateTeamOption{Name: name, Permission: sdk.AccessModeWrite, CanCreateOrgRepo: canCreate,
			IncludesAllRepositories: all, UnitsMap: units})
		if err != nil {
			return 0, fmt.Errorf("create team %s/%s: %w", CIOrg, name, err)
		}
		return created.ID, nil
	}
	if team.Permission == sdk.AccessModeWrite && team.CanCreateOrgRepo == canCreate && team.IncludesAllRepositories == all && reflect.DeepEqual(team.UnitsMap, units) {
		return team.ID, nil
	}
	if _, err := c.EditTeam(team.ID, sdk.EditTeamOption{Name: name, Permission: sdk.AccessModeWrite, CanCreateOrgRepo: &canCreate,
		IncludesAllRepositories: &all, UnitsMap: units}); err != nil {
		return 0, fmt.Errorf("restore team %s/%s: %w", CIOrg, name, err)
	}
	return team.ID, nil
}

// deleteWoodpeckerClient deletes step 10e's OAuth2 application woodpecker (forgejo_admin's), if any
func deleteWoodpeckerClient(c *sdk.Client) error {
	for page := 1; ; page++ {
		apps, _, err := c.ListOauth2(sdk.ListOauth2Option{ListOptions: sdk.ListOptions{Page: page, PageSize: 50}})
		if err != nil {
			return fmt.Errorf("OAuth2 applications: %w", err)
		}
		for _, app := range apps {
			if app.Name == woodpeckerApp {
				if _, err := c.DeleteOauth2(app.ID); err != nil {
					return fmt.Errorf("delete OAuth2 application %s: %w", woodpeckerApp, err)
				}
			}
		}
		if len(apps) < 50 {
			return nil
		}
	}
}

// ensureCIWebhook keeps org ci's webhook to Argo Events (WebhookURL): push and pull request events as JSON, with
// Authorization: Bearer <WebhookPath's secret>, which the EventSource checks. Forgejo never shows the header, so
// WebhookAppliedPath records which hook got which secret's hash: the hook is rewritten only when the secret or the
// hook changed. Other hooks are left alone; a second hook to the same URL is deleted.
func ensureCIWebhook(ctx context.Context, c *sdk.Client, tokens Tokens) error {
	secret, err := webhookSecret(ctx, tokens)
	if err != nil {
		return err
	}
	header := "Bearer " + secret
	sum := sha256.Sum256([]byte(header))
	hash := hex.EncodeToString(sum[:])
	events := []string{"push", "pull_request"}
	config := map[string]string{"url": WebhookURL, "content_type": "json"}

	var ours []*sdk.Hook
	for page := 1; ; page++ {
		hooks, _, err := c.ListOrgHooks(CIOrg, sdk.ListHooksOptions{ListOptions: sdk.ListOptions{Page: page, PageSize: 50}})
		if err != nil {
			return fmt.Errorf("webhooks of org %s: %w", CIOrg, err)
		}
		for _, h := range hooks {
			if h.Config["url"] == WebhookURL {
				ours = append(ours, h)
			}
		}
		if len(hooks) < 50 {
			break
		}
	}
	for _, extra := range ours[min(1, len(ours)):] {
		if _, err := c.DeleteOrgHook(CIOrg, extra.ID); err != nil {
			return fmt.Errorf("delete a second webhook of org %s to %s: %w", CIOrg, WebhookURL, err)
		}
	}
	applied, err := tokens.Read(ctx, WebhookAppliedPath)
	if err != nil {
		return err
	}
	var id int64
	if len(ours) == 0 {
		created, _, err := c.CreateOrgHook(CIOrg, sdk.CreateHookOption{Type: sdk.HookTypeForgejo, Config: config, Events: events, Active: true,
			AuthorizationHeader: header})
		if err != nil {
			return fmt.Errorf("create org %s's webhook: %w", CIOrg, err)
		}
		id = created.ID
	} else {
		hook := ours[0]
		id = hook.ID
		right := hook.Type == string(sdk.HookTypeForgejo) && hook.Active && reflect.DeepEqual(hook.Events, events) &&
			hook.Config["content_type"] == "json" && applied["id"] == fmt.Sprint(id) && applied["header_sha256"] == hash
		if right {
			return nil
		}
		active := true
		if _, err := c.EditOrgHook(CIOrg, id, sdk.EditHookOption{Config: config, Events: events, Active: &active, AuthorizationHeader: header}); err != nil {
			return fmt.Errorf("restore org %s's webhook: %w", CIOrg, err)
		}
	}
	return tokens.Write(ctx, WebhookAppliedPath, map[string]string{"id": fmt.Sprint(id), "header_sha256": hash})
}

// webhookSecret waits for platform/generated-secrets to push the webhook's secret (a first install), until ctx ends
func webhookSecret(ctx context.Context, tokens Tokens) (string, error) {
	for {
		stored, err := tokens.Read(ctx, WebhookPath)
		if err != nil {
			return "", err
		}
		if secret := stored["token"]; secret != "" {
			return secret, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("%s is not in OpenBao (platform/generated-secrets pushes it): %w", WebhookPath, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
}
