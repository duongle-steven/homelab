package rotate

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/duongle-steven/homelab/cli/internal/workload"
)

// previousKey lists a user's old token beside the new one while readers move (apps/ntfy.yaml's NTFY_AUTH_TOKENS)
const previousKey = "previous_token"

// NtfyOverlap replaces an ntfy user's token (Settings user) with an overlap: ntfy accepts several tokens per user.
// Prepare keeps the current token as previous_token and writes the new one at every path, Switch refreshes the
// readers and restarts ntfy (Alertmanager reads its credentials file at every notification), Verify checks the new
// token with ntfy, Revoke drops previous_token and restarts ntfy, which forgets a provisioned token no longer listed.
// A token people use (a reader with People) is dropped only once a person confirms they entered the new one.
type NtfyOverlap struct {
	Store     KV
	Workloads Readers
	Random    func(Entry) (string, error) // Password when nil
	// Check authenticates as user with token (without the tk_ prefix) and fails when ntfy refuses it
	Check func(ctx context.Context, user, token string) error
	// Confirm asks a person; nil without a terminal
	Confirm func(ctx context.Context, question string) error
}

func (m NtfyOverlap) Prepare(ctx context.Context, e Entry) error {
	value, err := pending(ctx, m.Store, m.Random, e)
	if err != nil {
		return hide(err, value)
	}
	key := e.Settings["key"]
	if key == "" || len(e.Paths) == 0 {
		return fmt.Errorf("%s has no settings key or no paths", e.Name)
	}
	for _, path := range e.Paths {
		data, err := m.Store.Read(ctx, path)
		if err != nil {
			return fmt.Errorf("read secret/%s failed", path)
		}
		if data[key] == value {
			// A previous run got this far: previous_token already holds the old token
			continue
		}
		next := maps.Clone(data)
		if next == nil {
			next = map[string]string{}
		}
		if data[key] != "" {
			next[previousKey] = data[key]
		}
		next[key] = value
		if err := m.Store.Write(ctx, path, next); err != nil {
			return fmt.Errorf("write secret/%s failed", path)
		}
	}
	return nil
}

func (m NtfyOverlap) Switch(ctx context.Context, e Entry) error { return m.restartNtfy(ctx, e) }

func (m NtfyOverlap) Verify(ctx context.Context, e Entry) error {
	if err := m.waitNtfy(ctx, e); err != nil {
		return err
	}
	value, err := m.current(ctx, e)
	if err != nil {
		return err
	}
	if m.Check == nil {
		return errors.New("no way to check an ntfy token")
	}
	if err := m.Check(ctx, e.Settings["user"], value); err != nil {
		return fmt.Errorf("ntfy refused %s's new token: %w", e.Settings["user"], hide(err, value))
	}
	return nil
}

func (m NtfyOverlap) Revoke(ctx context.Context, e Entry) error {
	if people(e) {
		later := fmt.Errorf("%w: both %s tokens work until they entered the new one; then run lab secrets rotate %s on a terminal to drop the old one", ErrWaitingForPeople, e.Settings["user"], e.Name)
		if m.Confirm == nil {
			return later
		}
		if m.Confirm(ctx, fmt.Sprintf("Has everyone entered the new %s token in their phones? The old one stops working.", e.Settings["user"])) != nil {
			return later
		}
	}
	for _, path := range e.Paths {
		data, err := m.Store.Read(ctx, path)
		if err != nil {
			return fmt.Errorf("read secret/%s failed", path)
		}
		if _, ok := data[previousKey]; !ok {
			continue
		}
		next := maps.Clone(data)
		delete(next, previousKey)
		if err := m.Store.Write(ctx, path, next); err != nil {
			return fmt.Errorf("write secret/%s failed", path)
		}
	}
	if err := m.restartNtfy(ctx, e); err != nil {
		return err
	}
	if err := m.waitNtfy(ctx, e); err != nil {
		return err
	}
	return dropPending(ctx, m.Store, e)
}

// current is the new token: the pending value of this rotation
func (m NtfyOverlap) current(ctx context.Context, e Entry) (string, error) {
	data, err := m.Store.Read(ctx, RecordPath(e))
	if err != nil {
		return "", errors.New("read the pending value at secret/" + RecordPath(e) + " failed")
	}
	if data[pendingKey] == "" {
		return "", fmt.Errorf("%s has no rotation under way", e.Name)
	}
	return data[pendingKey], nil
}

// restartNtfy refreshes every reader's ExternalSecrets and restarts the workloads that read them at start (ntfy)
func (m NtfyOverlap) restartNtfy(ctx context.Context, e Entry) error {
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

func (m NtfyOverlap) waitNtfy(ctx context.Context, e Entry) error {
	for _, r := range namespaced(e) {
		if len(r.Workloads) > 0 {
			if err := m.Workloads.WaitReady(ctx, r.Namespace, r.Workloads); err != nil {
				return err
			}
		}
	}
	return nil
}

func people(e Entry) bool {
	for _, r := range e.Readers {
		if r.People {
			return true
		}
	}
	return false
}

// NtfyCheck authenticates with ntfy from inside its pod (ntfy denies callers outside the mesh): GET /v1/account with
// the token, read from stdin, never in a command line. It sends nothing to anyone's phone.
func NtfyCheck(pods Executor) func(ctx context.Context, user, token string) error {
	ref := workload.Ref{Kind: "Deployment", Name: "ntfy"}
	script := `read -r t; wget -q -O /dev/null --header "Authorization: Bearer tk_$t" http://127.0.0.1/v1/account`
	return func(ctx context.Context, user, token string) error {
		var out strings.Builder
		if err := pods.Exec(ctx, "ntfy", ref, "main", []string{"sh", "-c", script}, strings.NewReader(token+"\n"), &out); err != nil {
			return fmt.Errorf("%s's token was refused: %w", user, hide(err, token))
		}
		return nil
	}
}
