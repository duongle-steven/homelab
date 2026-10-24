package rotate

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/forgejo"
)

// ForgejoAPI is what the Forgejo rotations ask of Forgejo (ForgejoClient).
type ForgejoAPI interface {
	NewTokenName(user string, at time.Time) (string, error)
	RotateToken(ctx context.Context, user, newName string) ([]string, error)
	DeleteToken(ctx context.Context, user, name string) error
	TokenValid(ctx context.Context, token string) (bool, error)
	RotateWoodpeckerClient(ctx context.Context) error
	ClientSecretCurrent(ctx context.Context, id, secret, callback string) (bool, error)
}

// ForgejoClient reaches Forgejo as forgejo_admin, its password from OpenBao, keeping tokens in OpenBao (Tokens).
type ForgejoClient struct {
	Endpoint func(ctx context.Context) (forgejo.Endpoint, error)
	Tokens   KV
}

func (c ForgejoClient) admin(ctx context.Context) (forgejo.Endpoint, string, error) {
	ep, err := c.Endpoint(ctx)
	if err != nil {
		return forgejo.Endpoint{}, "", err
	}
	data, err := c.Tokens.Read(ctx, "forgejo/generated/admin")
	if err != nil {
		return forgejo.Endpoint{}, "", errors.New("read Forgejo's admin password at secret/forgejo/generated/admin failed")
	}
	if data["password"] == "" {
		return forgejo.Endpoint{}, "", errors.New("OpenBao has no Forgejo admin password at secret/forgejo/generated/admin")
	}
	return ep, data["password"], nil
}

func (c ForgejoClient) NewTokenName(user string, at time.Time) (string, error) {
	return forgejo.NewTokenName(user, at)
}

func (c ForgejoClient) RotateToken(ctx context.Context, user, newName string) ([]string, error) {
	ep, password, err := c.admin(ctx)
	if err != nil {
		return nil, err
	}
	old, err := forgejo.RotateToken(ctx, ep, password, c.Tokens, user, newName)
	return old, hide(err, password)
}

func (c ForgejoClient) DeleteToken(ctx context.Context, user, name string) error {
	ep, password, err := c.admin(ctx)
	if err != nil {
		return err
	}
	return hide(forgejo.DeleteToken(ctx, ep, password, user, name), password)
}

func (c ForgejoClient) TokenValid(ctx context.Context, token string) (bool, error) {
	ep, err := c.Endpoint(ctx)
	if err != nil {
		return false, err
	}
	ok, err := forgejo.TokenValid(ctx, ep, token)
	return ok, hide(err, token)
}

func (c ForgejoClient) RotateWoodpeckerClient(ctx context.Context) error {
	ep, password, err := c.admin(ctx)
	if err != nil {
		return err
	}
	return hide(forgejo.RotateWoodpeckerClient(ctx, ep, password, c.Tokens), password)
}

func (c ForgejoClient) ClientSecretCurrent(ctx context.Context, id, secret, callback string) (bool, error) {
	ep, err := c.Endpoint(ctx)
	if err != nil {
		return false, err
	}
	ok, err := forgejo.ClientSecretCurrent(ctx, ep, id, secret, callback)
	return ok, hide(err, secret)
}

// ForgejoToken replaces Forgejo account Settings account's token (deploy or argocd) with an overlap: Prepare makes a
// new token beside the old ones and keeps it in OpenBao, Switch refreshes the readers' ExternalSecrets (Argo CD's
// repository credential; lab reads deploy's from OpenBao), Verify checks Forgejo accepts it, Revoke deletes the old
// ones. The new token's name and the old ones' are kept in the rotation's record path, so every phase reruns.
type ForgejoToken struct {
	API       ForgejoAPI
	Store     KV
	Workloads Readers
	Now       func() time.Time
}

func (m ForgejoToken) Prepare(ctx context.Context, e Entry) error {
	user := e.Settings["account"]
	state, err := readState(ctx, m.Store, e)
	if err != nil {
		return err
	}
	name := state["token-name"]
	if name == "" {
		now := time.Now
		if m.Now != nil {
			now = m.Now
		}
		if name, err = m.API.NewTokenName(user, now()); err != nil {
			return err
		}
		if err := writeState(ctx, m.Store, e, map[string]string{"token-name": name}); err != nil {
			return err
		}
	}
	old, err := m.API.RotateToken(ctx, user, name)
	if err != nil {
		return m.hide(ctx, e, err)
	}
	return writeState(ctx, m.Store, e, map[string]string{"token-name": name, "old-tokens": strings.Join(old, ",")})
}

// hide keeps the token OpenBao holds out of an error Forgejo's answer may have filled
func (m ForgejoToken) hide(ctx context.Context, e Entry, err error) error {
	var token string
	if len(e.Paths) > 0 {
		if data, readErr := m.Store.Read(ctx, e.Paths[0]); readErr == nil {
			token = data["token"]
		}
	}
	return hide(err, token)
}

func (m ForgejoToken) Switch(ctx context.Context, e Entry) error { return refresh(ctx, m.Workloads, e) }

func (m ForgejoToken) Verify(ctx context.Context, e Entry) error {
	user := e.Settings["account"]
	if len(e.Paths) == 0 {
		return fmt.Errorf("%s has no paths", e.Name)
	}
	data, err := m.Store.Read(ctx, e.Paths[0])
	if err != nil {
		return fmt.Errorf("read secret/%s failed", e.Paths[0])
	}
	ok, err := m.API.TokenValid(ctx, data["token"])
	if err != nil {
		return hide(err, data["token"])
	}
	if !ok {
		return fmt.Errorf("Forgejo refuses %s's new token at secret/%s", user, e.Paths[0])
	}
	return nil
}

func (m ForgejoToken) Revoke(ctx context.Context, e Entry) error {
	state, err := readState(ctx, m.Store, e)
	if err != nil {
		return err
	}
	for _, name := range strings.Split(state["old-tokens"], ",") {
		if name == "" {
			continue
		}
		if err := m.API.DeleteToken(ctx, e.Settings["account"], name); err != nil {
			return m.hide(ctx, e, err)
		}
	}
	return writeState(ctx, m.Store, e, nil)
}

// ForgejoOAuth gives Woodpecker's OAuth2 client a new secret (Forgejo has no overlap: class 2), restarts Woodpecker's
// server on it and checks Forgejo accepts it.
type ForgejoOAuth struct {
	API       ForgejoAPI
	Store     KV
	Workloads Readers
}

func (m ForgejoOAuth) Prepare(ctx context.Context, e Entry) error {
	state, err := readState(ctx, m.Store, e)
	if err != nil {
		return err
	}
	if state["rotated"] != "" {
		// A previous run regenerated it and OpenBao kept it
		return nil
	}
	if err := m.API.RotateWoodpeckerClient(ctx); err != nil {
		var secret string
		if len(e.Paths) > 0 {
			if data, readErr := m.Store.Read(ctx, e.Paths[0]); readErr == nil {
				secret = data["client_secret"]
			}
		}
		return hide(err, secret)
	}
	return writeState(ctx, m.Store, e, map[string]string{"rotated": "yes"})
}

func (m ForgejoOAuth) Switch(ctx context.Context, e Entry) error {
	if err := refresh(ctx, m.Workloads, e); err != nil {
		return err
	}
	for _, r := range namespaced(e) {
		if len(r.Workloads) > 0 {
			if err := m.Workloads.Restart(ctx, r.Namespace, r.Workloads); err != nil {
				return fmt.Errorf("restart %s's workloads: %w", r.Namespace, err)
			}
		}
	}
	return nil
}

func (m ForgejoOAuth) Verify(ctx context.Context, e Entry) error {
	for _, r := range namespaced(e) {
		if len(r.Workloads) > 0 {
			if err := m.Workloads.WaitReady(ctx, r.Namespace, r.Workloads); err != nil {
				return err
			}
		}
	}
	if len(e.Paths) == 0 {
		return fmt.Errorf("%s has no paths", e.Name)
	}
	data, err := m.Store.Read(ctx, e.Paths[0])
	if err != nil {
		return fmt.Errorf("read secret/%s failed", e.Paths[0])
	}
	ok, err := m.API.ClientSecretCurrent(ctx, data["client_id"], data["client_secret"], data["redirect_uri"])
	if err != nil {
		return hide(err, data["client_secret"])
	}
	if !ok {
		return fmt.Errorf("Forgejo refuses Woodpecker's client secret at secret/%s", e.Paths[0])
	}
	return nil
}

func (m ForgejoOAuth) Revoke(ctx context.Context, e Entry) error {
	return writeState(ctx, m.Store, e, nil)
}

// readState and writeState keep a rotation's working names (never values) in its record path's data
func readState(ctx context.Context, store KV, e Entry) (map[string]string, error) {
	data, err := store.Read(ctx, RecordPath(e))
	if err != nil {
		return nil, errors.New("read secret/" + RecordPath(e) + " failed")
	}
	return data, nil
}

func writeState(ctx context.Context, store KV, e Entry, state map[string]string) error {
	next := maps.Clone(state)
	if next == nil {
		next = map[string]string{}
	}
	if err := store.Write(ctx, RecordPath(e), next); err != nil {
		return errors.New("write secret/" + RecordPath(e) + " failed")
	}
	return nil
}
