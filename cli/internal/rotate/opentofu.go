package rotate

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// OpenTofu replaces a secret OpenTofu makes (Settings unit and resource): -replace in the secret's unit, then the
// secrets unit's apply writes it into namespace infra-secrets, from where infra-secrets pushes it to OpenBao. One
// apply ends the old value (class 1 or 2, no overlap). It runs from the workstation only: the cluster holds no
// Cloudflare credentials.
type OpenTofu struct {
	Root      string                                                      // the repository's checkout
	Run       func(ctx context.Context, dir string, args ...string) error // terragrunt in dir; Terragrunt when nil
	Store     KV
	Workloads Readers
	Waits     Waiter
	// Nudge asks infra-secrets to push again now rather than at its next refresh (nil: wait for it)
	Nudge func(ctx context.Context) error
}

// Terragrunt runs terragrunt in dir with the terminal attached (it prints the plan it applies).
func Terragrunt(out io.Writer) func(ctx context.Context, dir string, args ...string) error {
	return func(ctx context.Context, dir string, args ...string) error {
		cmd := exec.CommandContext(ctx, "terragrunt", args...)
		cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.Stdin = dir, out, out, os.Stdin
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("terragrunt %s in %s: %w", args[0], dir, err)
		}
		return nil
	}
}

// valueHash identifies a secret's value without keeping it
func valueHash(data map[string]string) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\x00", k, data[k])
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (m OpenTofu) run(ctx context.Context, dir string, args ...string) error {
	if m.Run == nil {
		return Terragrunt(os.Stdout)(ctx, dir, args...)
	}
	return m.Run(ctx, dir, args...)
}

func (m OpenTofu) Prepare(ctx context.Context, e Entry) error {
	unit, resource := e.Settings["unit"], e.Settings["resource"]
	if unit == "" || resource == "" || len(e.Paths) == 0 {
		return fmt.Errorf("%s has no settings unit or resource, or no paths", e.Name)
	}
	state, err := readState(ctx, m.Store, e)
	if err != nil {
		return err
	}
	if state["value-hash"] == "" {
		current, err := m.Store.Read(ctx, e.Paths[0])
		if err != nil {
			return fmt.Errorf("read secret/%s failed", e.Paths[0])
		}
		state = map[string]string{"value-hash": valueHash(current)}
		if err := writeState(ctx, m.Store, e, state); err != nil {
			return err
		}
	}
	apply := []string{"apply", "--non-interactive", "--", "-auto-approve"}
	if state["replaced"] == "" {
		if err := m.run(ctx, filepath.Join(m.Root, unit), append(slices.Clone(apply), "-replace="+resource)...); err != nil {
			return err
		}
		state["replaced"] = "yes"
		if err := writeState(ctx, m.Store, e, state); err != nil {
			return err
		}
	}
	// The secrets unit is a sibling of the secret's unit (infra/<env>/secrets) and applies the same way again
	return m.run(ctx, filepath.Join(m.Root, filepath.Dir(unit), "secrets"), apply...)
}

func (m OpenTofu) Switch(ctx context.Context, e Entry) error {
	state, err := readState(ctx, m.Store, e)
	if err != nil {
		return err
	}
	before := state["value-hash"]
	if err := m.Waits.Until(ctx, "OpenBao's new value at secret/"+e.Paths[0], func() (bool, error) {
		if m.Nudge != nil {
			_ = m.Nudge(ctx)
		}
		data, err := m.Store.Read(ctx, e.Paths[0])
		if err != nil {
			return false, nil
		}
		return data != nil && valueHash(data) != before, nil
	}); err != nil {
		return fmt.Errorf("infra-secrets did not push the new value to secret/%s: %w", e.Paths[0], err)
	}
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

func (m OpenTofu) Verify(ctx context.Context, e Entry) error { return waitAll(ctx, m.Workloads, e) }

func (m OpenTofu) Revoke(ctx context.Context, e Entry) error { return writeState(ctx, m.Store, e, nil) }

// Manual is a rotation a person does (Steps): lab prints the steps, waits for the person to say they are done, then
// moves the readers. Without a person on a terminal it stops before changing anything.
type Manual struct {
	Out       io.Writer
	Workloads Readers
	Confirm   func(ctx context.Context, question string) error
}

func (m Manual) Prepare(ctx context.Context, e Entry) error {
	if m.Confirm == nil {
		return fmt.Errorf("%s needs a person: run lab secrets rotate %s on a terminal", e.Name, e.Name)
	}
	fmt.Fprintf(m.Out, "%s: %s\n", e.Name, strings.TrimSpace(e.Steps))
	if err := m.Confirm(ctx, "Done?"); err != nil {
		return errors.New("stopped before " + e.Name + "'s steps were done: nothing changed")
	}
	return nil
}

func (m Manual) Switch(ctx context.Context, e Entry) error {
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

func (m Manual) Verify(ctx context.Context, e Entry) error { return waitAll(ctx, m.Workloads, e) }

func (m Manual) Revoke(context.Context, Entry) error { return nil }

// waitAll waits until every reader's workloads are ready
func waitAll(ctx context.Context, w Readers, e Entry) error {
	for _, r := range namespaced(e) {
		if len(r.Workloads) > 0 {
			if err := w.WaitReady(ctx, r.Namespace, r.Workloads); err != nil {
				return err
			}
		}
	}
	return nil
}
