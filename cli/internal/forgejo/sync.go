// Package forgejo provisions the GitOps repository ops/homelab on Forgejo and deploys to it (upstream nixos's
// lab infra forgejo, without its developers team and Woodpecker client).
package forgejo

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
}

var accounts = []account{
	{name: "deploy", permission: sdk.AccessModeWrite, token: "lab", scope: sdk.AccessTokenScopeRepositoryWrite, path: DeployPath},
	{name: "argocd", permission: sdk.AccessModeRead, token: "argocd", scope: sdk.AccessTokenScopeRepositoryRead, path: ArgoCDPath},
}

// Sync makes org ops match the design: what is right is kept, what is wrong is fixed, what is missing is created.
// It waits for Forgejo until ctx ends.
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
	return nil
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

func ensureRepository(c *sdk.Client) error {
	if _, resp, err := c.GetOrg(Org); err != nil {
		if !notFound(resp) {
			return fmt.Errorf("org %s: %w", Org, err)
		}
		private := sdk.VisibleTypePrivate
		if _, _, err := c.CreateOrg(sdk.CreateOrgOption{Name: Org, Visibility: private}); err != nil {
			return fmt.Errorf("create org %s: %w", Org, err)
		}
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

// valid tells whether token still reads ops/homelab (both accounts' scopes allow it; GET /user needs read:user).
func valid(ctx context.Context, ep Endpoint, token string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL+"/api/v1/repos/"+Org+"/"+Repo, nil)
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
		ok, err := valid(ctx, ep, token)
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
