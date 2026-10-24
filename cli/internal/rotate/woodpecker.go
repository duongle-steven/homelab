package rotate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/duongle-steven/homelab/cli/internal/workload"
)

// WoodpeckerClient calls Woodpecker's API as its admin (WOODPECKER_ADMIN), with the token kept in OpenBao at
// secret/woodpecker/admin-token (a person writes the first one: docs/how-to-guides/woodpecker.md).
type WoodpeckerClient struct {
	URL   func(ctx context.Context) (string, *http.Client, error)
	Store KV
}

const woodpeckerTokenPath = "woodpecker/admin-token"

func (c WoodpeckerClient) token(ctx context.Context) (string, error) {
	data, err := c.Store.Read(ctx, woodpeckerTokenPath)
	if err != nil {
		return "", errors.New("read secret/" + woodpeckerTokenPath + " failed")
	}
	if data["token"] == "" {
		return "", errors.New("OpenBao has no Woodpecker admin token at secret/" + woodpeckerTokenPath + ": write one first (docs/how-to-guides/woodpecker.md)")
	}
	return data["token"], nil
}

// do sends a request with token (the stored one when empty) and decodes a JSON answer into out (nil: none);
// notFound reports a 404 apart
func (c WoodpeckerClient) do(ctx context.Context, token, method, path string, body any, out any) (int, []byte, error) {
	if token == "" {
		var err error
		if token, err = c.token(ctx); err != nil {
			return 0, nil, err
		}
	}
	base, client, err := c.URL(ctx)
	if err != nil {
		return 0, nil, err
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("Woodpecker %s %s: %w", method, path, hide(err, token))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return resp.StatusCode, nil, fmt.Errorf("Woodpecker %s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, nil, fmt.Errorf("Woodpecker %s %s: unreadable answer", method, path)
		}
	}
	return resp.StatusCode, raw, nil
}

// Ready checks, before anything changes, that Woodpecker's API takes the stored admin token: a rotation that changes
// the agent secret or zot's password and then cannot reach the API would leave CI half switched
func (c WoodpeckerClient) Ready(ctx context.Context) error {
	token, err := c.token(ctx)
	if err != nil {
		return err
	}
	ok, err := c.TokenWorks(ctx, token)
	if err != nil {
		return fmt.Errorf("Woodpecker's API with the admin token at secret/%s: %w", woodpeckerTokenPath, err)
	}
	if !ok {
		return errors.New("Woodpecker refuses the admin token at secret/" + woodpeckerTokenPath + ": write a new one (docs/how-to-guides/woodpecker.md)")
	}
	return nil
}

// ResetToken makes the admin a new token (Woodpecker ends every earlier one) and returns it.
func (c WoodpeckerClient) ResetToken(ctx context.Context) (string, error) {
	_, raw, err := c.do(ctx, "", http.MethodDelete, "/api/user/token", nil, nil)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("Woodpecker returned an empty token")
	}
	return token, nil
}

// TokenWorks reports whether Woodpecker accepts token.
func (c WoodpeckerClient) TokenWorks(ctx context.Context, token string) (bool, error) {
	status, _, err := c.do(ctx, token, http.MethodGet, "/api/user", nil, nil)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return false, nil
	}
	return err == nil, hide(err, token)
}

// Agents lists the agents' ids.
func (c WoodpeckerClient) Agents(ctx context.Context) ([]int64, error) {
	var agents []struct {
		ID int64 `json:"id"`
	}
	if _, _, err := c.do(ctx, "", http.MethodGet, "/api/agents", nil, &agents); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.ID)
	}
	return ids, nil
}

// DeleteAgent deletes an agent's row, which ends the token it holds.
func (c WoodpeckerClient) DeleteAgent(ctx context.Context, id int64) error {
	status, _, err := c.do(ctx, "", http.MethodDelete, "/api/agents/"+strconv.FormatInt(id, 10), nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// SetOrgSecret replaces org's secret name with value; it reports false when Woodpecker has no such org or secret.
func (c WoodpeckerClient) SetOrgSecret(ctx context.Context, org, name, value string) (bool, error) {
	var found struct {
		ID int64 `json:"id"`
	}
	status, _, err := c.do(ctx, "", http.MethodGet, "/api/orgs/lookup/"+org, nil, &found)
	if status == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// push and tag only, as the docs set it: a manual run lets any writer of a repository start one with the secrets
	body := map[string]any{"name": name, "value": value, "events": []string{"push", "tag"}}
	status, _, err = c.do(ctx, "", http.MethodPatch, fmt.Sprintf("/api/orgs/%d/secrets/%s", found.ID, name), body, nil)
	if status == http.StatusNotFound {
		return false, nil
	}
	return err == nil, hide(err, value)
}

// WoodpeckerToken replaces the admin's Woodpecker token (the one lab uses): the reset ends the old one at once, and
// only lab reads it (class 1). A token lost between the reset and OpenBao's write needs a person again.
type WoodpeckerToken struct {
	API   WoodpeckerClient
	Store KV
}

func (m WoodpeckerToken) Prepare(ctx context.Context, e Entry) error {
	state, err := readState(ctx, m.Store, e)
	if err != nil {
		return err
	}
	if state["reset"] != "" {
		return nil
	}
	token, err := m.API.ResetToken(ctx)
	if err != nil {
		return err
	}
	if err := m.Store.Write(ctx, woodpeckerTokenPath, map[string]string{"token": token}); err != nil {
		return errors.New("Woodpecker made a new admin token but secret/" + woodpeckerTokenPath + " could not keep it: write a new one by hand (docs/how-to-guides/woodpecker.md)")
	}
	return writeState(ctx, m.Store, e, map[string]string{"reset": "yes"})
}

func (m WoodpeckerToken) Switch(context.Context, Entry) error { return nil }

func (m WoodpeckerToken) Verify(ctx context.Context, e Entry) error {
	token, err := m.API.token(ctx)
	if err != nil {
		return err
	}
	ok, err := m.API.TokenWorks(ctx, token)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("Woodpecker refuses the admin token at secret/" + woodpeckerTokenPath)
	}
	return nil
}

func (m WoodpeckerToken) Revoke(ctx context.Context, e Entry) error {
	return writeState(ctx, m.Store, e, nil)
}

// WoodpeckerAgent replaces the agents' shared secret. Woodpecker keeps the secret an agent registered with as its
// row's token and accepts it as long as the row exists, so the rotation deletes every row and the agents' ids (their
// config volumes) and the agents register again with the new secret: CI stops meanwhile (class 2, after the idle
// wait). Settings server and agents name the StatefulSets.
type WoodpeckerAgent struct {
	API       WoodpeckerClient
	Kube      kubernetes.Interface
	Store     KV
	Workloads Readers
	Waits     Waiter
	Random    func(Entry) (string, error) // Password when nil
}

func (m WoodpeckerAgent) Prepare(ctx context.Context, e Entry) error {
	if err := m.API.Ready(ctx); err != nil {
		return err
	}
	value, err := pending(ctx, m.Store, m.Random, e)
	if err != nil {
		return hide(err, value)
	}
	return hide(writeValue(ctx, m.Store, e, value), value)
}

func (m WoodpeckerAgent) Switch(ctx context.Context, e Entry) error {
	ns := e.Readers[0].Namespace
	server, agents := e.Settings["server"], e.Settings["agents"]
	sts, err := m.Kube.AppsV1().StatefulSets(ns).Get(ctx, agents, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("StatefulSet %s/%s: %w", ns, agents, err)
	}
	if err := refresh(ctx, m.Workloads, e); err != nil {
		return err
	}
	server0 := []workload.Ref{{Kind: "StatefulSet", Name: server}}
	if err := m.Workloads.Restart(ctx, ns, server0); err != nil {
		return fmt.Errorf("restart %s/%s: %w", ns, server, err)
	}
	if err := m.Workloads.WaitReady(ctx, ns, server0); err != nil {
		return err
	}
	ids, err := m.API.Agents(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := m.API.DeleteAgent(ctx, id); err != nil {
			return err
		}
	}
	// Each agent starts again without its id: its config claims (homelab/backup none) are deleted, which waits for
	// the pod, then the pod; the StatefulSet makes both again. Never scaled: Argo CD would put the replicas back.
	claims := m.Kube.CoreV1().PersistentVolumeClaims(ns)
	pods := m.Kube.CoreV1().Pods(ns)
	for i := range ptrValue(sts.Spec.Replicas, 1) {
		pod := fmt.Sprintf("%s-%d", agents, i)
		old := map[string]types.UID{}
		for _, t := range sts.Spec.VolumeClaimTemplates {
			name := t.Name + "-" + pod
			c, err := claims.Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("claim %s/%s: %w", ns, name, err)
			}
			old[name] = c.UID
			if err := claims.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &c.UID}}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete claim %s/%s: %w", ns, name, err)
			}
		}
		if err := pods.Delete(ctx, pod, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete pod %s/%s: %w", ns, pod, err)
		}
		// The StatefulSet may have made the pod again before the old claim went: once the claim is gone, the pod goes
		// once more, so the StatefulSet makes it with a new claim
		if err := m.Waits.Until(ctx, "the old claims of "+ns+"/"+pod+" deleted", func() (bool, error) {
			for name, uid := range old {
				if c, err := claims.Get(ctx, name, metav1.GetOptions{}); err == nil && c.UID == uid {
					return false, nil
				}
			}
			return true, nil
		}); err != nil {
			return err
		}
		if err := pods.Delete(ctx, pod, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete pod %s/%s: %w", ns, pod, err)
		}
	}
	return nil
}

func (m WoodpeckerAgent) Verify(ctx context.Context, e Entry) error {
	if err := waitAll(ctx, m.Workloads, e); err != nil {
		return err
	}
	return m.Waits.Until(ctx, "a Woodpecker agent registered with the new secret", func() (bool, error) {
		ids, err := m.API.Agents(ctx)
		return err == nil && len(ids) > 0, nil
	})
}

func (m WoodpeckerAgent) Revoke(ctx context.Context, e Entry) error {
	if err := dropPending(ctx, m.Store, e); err != nil {
		return err
	}
	return writeState(ctx, m.Store, e, nil)
}

func ptrValue(p *int32, fallback int32) int32 {
	if p == nil {
		return fallback
	}
	return *p
}

// RegistryCI replaces zot's ci password like a generated secret, and Woodpecker's org secret (Settings
// woodpecker-org, org-secret) with it, which the pipelines push images with.
type RegistryCI struct {
	Generated
	API WoodpeckerClient
}

func (m RegistryCI) Prepare(ctx context.Context, e Entry) error {
	if err := m.API.Ready(ctx); err != nil {
		return err
	}
	return m.Generated.Prepare(ctx, e)
}

func (m RegistryCI) Switch(ctx context.Context, e Entry) error {
	if err := m.Generated.Switch(ctx, e); err != nil {
		return err
	}
	data, err := m.Store.Read(ctx, e.Paths[0])
	if err != nil {
		return fmt.Errorf("read secret/%s failed", e.Paths[0])
	}
	value := data[e.Settings["key"]]
	// false: nobody set it yet (docs/how-to-guides/woodpecker.md), so nothing reads the old value there
	if _, err := m.API.SetOrgSecret(ctx, e.Settings["woodpecker-org"], e.Settings["org-secret"], value); err != nil {
		return hide(err, value)
	}
	return nil
}
