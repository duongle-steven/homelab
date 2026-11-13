package forgejo

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
)

// TokenValid reports whether user's token still reads what that account reads (the check Sync makes before keeping a
// stored token).
func TokenValid(ctx context.Context, ep Endpoint, user, token string) (bool, error) {
	a, err := findAccount(user)
	if err != nil {
		return false, err
	}
	return valid(ctx, ep, token, a.check)
}

func findAccount(user string) (account, error) {
	for _, a := range append(accounts, ciBot) {
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

// RotateToken gives user (deploy, argocd or ci-bot) a new token named newName beside its existing ones and keeps it in
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
			ok, err := valid(ctx, ep, stored["token"], a.check)
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

// NewTokenName names user's next token: its usual name and the time, so Sync and RotateToken recognize it as user's.
func NewTokenName(user string, at time.Time) (string, error) {
	a, err := findAccount(user)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%d", a.token, at.Unix()), nil
}
