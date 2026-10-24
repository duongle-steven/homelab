package forgejo

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
)

// TokenValid reports whether token reads ops/homelab (the check Sync makes before keeping a stored token).
func TokenValid(ctx context.Context, ep Endpoint, token string) (bool, error) {
	return valid(ctx, ep, token)
}

// ClientSecretCurrent reports whether secret is Woodpecker's OAuth2 client's current secret.
func ClientSecretCurrent(ctx context.Context, ep Endpoint, id, secret, callback string) (bool, error) {
	return clientSecretCurrent(ctx, ep, id, secret, callback)
}

func findAccount(user string) (account, error) {
	for _, a := range accounts {
		if a.name == user {
			return a, nil
		}
	}
	return account{}, fmt.Errorf("Forgejo user %s is not one lab manages", user)
}

// asUser logs in as a's user: forgejo_admin sets a new random password first (the accounts sign in with tokens only)
func asUser(ctx context.Context, ep Endpoint, adminPassword string, a account) (*sdk.Client, error) {
	admin, err := newClient(ctx, ep, sdk.SetBasicAuth("forgejo_admin", adminPassword))
	if err != nil {
		return nil, err
	}
	password, err := randomPassword()
	if err != nil {
		return nil, err
	}
	never := false
	if _, err := admin.AdminEditUser(a.name, sdk.EditUserOption{LoginName: a.name, Password: password, MustChangePassword: &never}); err != nil {
		return nil, fmt.Errorf("reset %s's password: %w", a.name, err)
	}
	return newClient(ctx, ep, sdk.SetBasicAuth(a.name, password))
}

// RotateToken gives user (deploy or argocd) a new token named newName beside its existing ones and keeps it in
// OpenBao (with its name), returning the names of the tokens it replaces: they keep working until DeleteToken, so the
// readers move first. Run again with the same newName it makes nothing new once OpenBao holds a working token of that
// name; a token of that name OpenBao never kept (an interruption in between) is replaced, since Forgejo shows a token
// only when it makes it.
func RotateToken(ctx context.Context, ep Endpoint, adminPassword string, tokens Tokens, user, newName string) ([]string, error) {
	a, err := findAccount(user)
	if err != nil {
		return nil, err
	}
	c, err := asUser(ctx, ep, adminPassword, a)
	if err != nil {
		return nil, err
	}
	existing, _, err := c.ListAccessTokens(a.name, sdk.ListAccessTokensOptions{})
	if err != nil {
		return nil, fmt.Errorf("list %s's tokens: %w", a.name, err)
	}
	var old []string
	var named *sdk.AccessToken
	for _, t := range existing {
		switch {
		case t.Name == newName:
			named = t
		case t.Name == a.token || strings.HasPrefix(t.Name, a.token+"-"):
			old = append(old, t.Name)
		}
	}
	if named != nil {
		stored, err := tokens.Read(ctx, a.path)
		if err != nil {
			return nil, err
		}
		if stored["name"] == newName && stored["token"] != "" {
			ok, err := valid(ctx, ep, stored["token"])
			if err != nil {
				return nil, err
			}
			if ok {
				return old, nil
			}
		}
		if _, err := c.DeleteAccessToken(a.name, named.ID); err != nil {
			return nil, fmt.Errorf("delete %s's unkept token %s: %w", a.name, newName, err)
		}
	}
	created, _, err := c.CreateAccessToken(a.name, sdk.CreateAccessTokenOption{Name: newName, Scopes: []sdk.AccessTokenScope{a.scope}})
	if err != nil {
		return nil, fmt.Errorf("create %s's token %s: %w", a.name, newName, err)
	}
	if created.Token == "" {
		return nil, fmt.Errorf("Forgejo returned an empty token for %s", a.name)
	}
	if err := tokens.Write(ctx, a.path, map[string]string{"username": a.name, "token": created.Token, "name": newName}); err != nil {
		return nil, err
	}
	return old, nil
}

// DeleteToken ends user's token named name; a token already gone is no error.
func DeleteToken(ctx context.Context, ep Endpoint, adminPassword, user, name string) error {
	a, err := findAccount(user)
	if err != nil {
		return err
	}
	c, err := asUser(ctx, ep, adminPassword, a)
	if err != nil {
		return err
	}
	existing, _, err := c.ListAccessTokens(a.name, sdk.ListAccessTokensOptions{})
	if err != nil {
		return fmt.Errorf("list %s's tokens: %w", a.name, err)
	}
	for _, t := range existing {
		if t.Name == name {
			if _, err := c.DeleteAccessToken(a.name, t.ID); err != nil {
				return fmt.Errorf("delete %s's token %s: %w", a.name, name, err)
			}
		}
	}
	return nil
}

// RotateWoodpeckerClient gives Woodpecker's OAuth2 application a new secret (Forgejo issues one on every update; the
// old one stops at once) and keeps it in OpenBao.
func RotateWoodpeckerClient(ctx context.Context, ep Endpoint, adminPassword string, tokens Tokens) error {
	stored, err := tokens.Read(ctx, WoodpeckerPath)
	if err != nil {
		return err
	}
	if stored["client_id"] == "" || stored["redirect_uri"] == "" {
		return fmt.Errorf("OpenBao has no Woodpecker client at %s: run lab infra forgejo sync first", WoodpeckerPath)
	}
	admin, err := newClient(ctx, ep, sdk.SetBasicAuth("forgejo_admin", adminPassword))
	if err != nil {
		return err
	}
	var found *sdk.Oauth2
	for page := 1; found == nil; page++ {
		apps, _, err := admin.ListOauth2(sdk.ListOauth2Option{ListOptions: sdk.ListOptions{Page: page, PageSize: 50}})
		if err != nil {
			return fmt.Errorf("OAuth2 applications: %w", err)
		}
		for _, app := range apps {
			if app.Name == woodpeckerApp && app.ClientID == stored["client_id"] {
				found = app
			}
		}
		if len(apps) < 50 {
			break
		}
	}
	if found == nil {
		return fmt.Errorf("Forgejo has no OAuth2 application %s with the stored client id: run lab infra forgejo sync first", woodpeckerApp)
	}
	app, _, err := admin.UpdateOauth2(found.ID, sdk.CreateOauth2Option{Name: woodpeckerApp, ConfidentialClient: true, RedirectURIs: found.RedirectURIs})
	if err != nil {
		return fmt.Errorf("OAuth2 application %s: %w", woodpeckerApp, err)
	}
	if app.ClientSecret == "" {
		return fmt.Errorf("Forgejo returned no secret for OAuth2 application %s", woodpeckerApp)
	}
	return tokens.Write(ctx, WoodpeckerPath, map[string]string{"client_id": app.ClientID, "client_secret": app.ClientSecret, "redirect_uri": stored["redirect_uri"]})
}

// NewTokenName names user's next token: its usual name and the time, so Sync and RotateToken recognize it as user's.
func NewTokenName(user string, at time.Time) (string, error) {
	a, err := findAccount(user)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%d", a.token, at.Unix()), nil
}
