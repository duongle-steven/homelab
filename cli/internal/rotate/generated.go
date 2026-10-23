package rotate

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/duongle-steven/homelab/cli/internal/workload"
)

// KV reads and writes OpenBao's KV store (secrets.Store).
type KV interface {
	Read(ctx context.Context, path string) (map[string]string, error)
	Write(ctx context.Context, path string, data map[string]string) error
}

// Readers is what a method does to the readers of a secret (workload.Client).
type Readers interface {
	RefreshExternalSecrets(ctx context.Context, namespace string, names []string) error
	Restart(ctx context.Context, namespace string, workloads []workload.Ref) error
	WaitReady(ctx context.Context, namespace string, workloads []workload.Ref) error
}

// pendingKey holds the new value of a rotation under way, in the data of the entry's record path
// (secret/rotation/<name>): Prepare writes it there before anything else, so a rerun after an interruption finds it
// and never makes a third value; Revoke removes it. The record itself is that path's custom metadata.
const pendingKey = "pending"

// Generated replaces a random value: Prepare writes one new value under Settings[key] to every path (the generated
// secret's destinations), Switch refreshes the readers' ExternalSecrets and restarts their workloads, Verify waits
// until they are ready, Revoke only drops the pending value (nothing else holds the old one).
type Generated struct {
	Store     KV
	Workloads Readers
	Random    func(Entry) (string, error) // Password when nil
}

func (g Generated) Prepare(ctx context.Context, e Entry) error {
	value, err := pending(ctx, g.Store, g.Random, e)
	if err != nil {
		return hide(err, value)
	}
	return hide(writeValue(ctx, g.Store, e, value), value)
}

func (g Generated) Switch(ctx context.Context, e Entry) error {
	if err := refresh(ctx, g.Workloads, e); err != nil {
		return err
	}
	for _, r := range namespaced(e) {
		if len(r.Workloads) > 0 {
			if err := g.Workloads.Restart(ctx, r.Namespace, r.Workloads); err != nil {
				return fmt.Errorf("restart %s's workloads: %w", r.Namespace, err)
			}
		}
	}
	return nil
}

func (g Generated) Verify(ctx context.Context, e Entry) error {
	for _, r := range namespaced(e) {
		if len(r.Workloads) > 0 {
			if err := g.Workloads.WaitReady(ctx, r.Namespace, r.Workloads); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g Generated) Revoke(ctx context.Context, e Entry) error { return dropPending(ctx, g.Store, e) }

// pending returns the new value a previous run of this rotation wrote, or makes one and writes it first
func pending(ctx context.Context, store KV, random func(Entry) (string, error), e Entry) (string, error) {
	path := RecordPath(e)
	data, err := store.Read(ctx, path)
	if err != nil {
		return "", errors.New("read the pending value at secret/" + path + " failed")
	}
	if v := data[pendingKey]; v != "" {
		return v, nil
	}
	if random == nil {
		random = Password
	}
	v, err := random(e)
	if err != nil {
		return "", errors.New("make a new value failed")
	}
	if err := store.Write(ctx, path, map[string]string{pendingKey: v}); err != nil {
		return v, fmt.Errorf("keep the new value at secret/%s: %w", path, hide(err, v))
	}
	return v, nil
}

func dropPending(ctx context.Context, store KV, e Entry) error {
	path := RecordPath(e)
	data, err := store.Read(ctx, path)
	if err != nil {
		return errors.New("read the pending value at secret/" + path + " failed")
	}
	if data[pendingKey] == "" {
		return nil
	}
	rest := maps.Clone(data)
	delete(rest, pendingKey)
	if err := store.Write(ctx, path, rest); err != nil {
		return fmt.Errorf("drop the pending value at secret/%s failed", path)
	}
	return nil
}

// writeValue sets Settings[key] to value at every path, keeping the paths' other keys
func writeValue(ctx context.Context, store KV, e Entry, value string) error {
	key := e.Settings["key"]
	if key == "" || len(e.Paths) == 0 {
		return fmt.Errorf("%s has no settings key or no paths", e.Name)
	}
	for _, path := range e.Paths {
		data, err := store.Read(ctx, path)
		if err != nil {
			return fmt.Errorf("read secret/%s failed", path)
		}
		if data[key] == value {
			continue
		}
		next := maps.Clone(data)
		if next == nil {
			next = map[string]string{}
		}
		next[key] = value
		if err := store.Write(ctx, path, next); err != nil {
			return fmt.Errorf("write secret/%s failed", path)
		}
	}
	return nil
}

// namespaced is the entry's readers in the cluster (not people)
func namespaced(e Entry) []Reader {
	var out []Reader
	for _, r := range e.Readers {
		if !r.People && r.Namespace != "" {
			out = append(out, r)
		}
	}
	return out
}

// refresh makes every reader's ExternalSecrets read OpenBao again
func refresh(ctx context.Context, w Readers, e Entry) error {
	for _, r := range namespaced(e) {
		if len(r.ExternalSecrets) > 0 {
			if err := w.RefreshExternalSecrets(ctx, r.Namespace, r.ExternalSecrets); err != nil {
				return fmt.Errorf("refresh %s's ExternalSecrets: %w", r.Namespace, err)
			}
		}
	}
	return nil
}

// hide replaces the values in err's message, so no value reaches a terminal or a log however a dependency words its
// error. The result does not wrap err (unwrapping would reach the unredacted message).
func hide(err error, values ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, v := range values {
		if v != "" {
			msg = strings.ReplaceAll(msg, v, "<redacted>")
		}
	}
	return errors.New(msg)
}

// The character set of External Secrets Operator's Password generator (platform/generated-secrets/templates)
const (
	letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digits  = "0123456789"
	symbols = "~!@#$%^&*()_+`-={}|[]\\:\"<>?,./"
)

// Password makes a value the way the entry's Password generator does: Settings length characters (32 by default),
// six of them digits, six symbols when Settings special is true and none otherwise, the rest letters, in random order.
func Password(e Entry) (string, error) {
	length := 32
	if s := e.Settings["length"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 12 {
			return "", fmt.Errorf("%s: settings length %q is not a number of at least 12", e.Name, s)
		}
		length = n
	}
	nSymbols := 0
	if e.Settings["special"] == "true" {
		nSymbols = 6
	}
	var out []byte
	for _, part := range []struct {
		set string
		n   int
	}{{digits, 6}, {symbols, nSymbols}, {letters, length - 6 - nSymbols}} {
		for range part.n {
			c, err := pick(len(part.set))
			if err != nil {
				return "", err
			}
			out = append(out, part.set[c])
		}
	}
	for i := len(out) - 1; i > 0; i-- {
		j, err := pick(i + 1)
		if err != nil {
			return "", err
		}
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}

func pick(n int) (int, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
}

// checkGeneratorSettings keeps the catalog in step with platform/generated-secrets: each generator's catalog entry
// has its key, length (32 by default), special and destinations; each generated or app-password entry has a
// generator. Entries that never rotate, and woodpecker.agent (per-agent tokens, the plan's Task 8), are left out.
func checkGeneratorSettings(root string, entries []Entry) []string {
	files, err := valuesFiles(root, "platform/generated-secrets")
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	seen := map[string]bool{}
	for _, v := range files {
		for _, list := range []string{"secrets", "extraSecrets"} {
			for _, s := range v.list(list) {
				name, _ := s["name"].(string)
				seen[name] = true
				e, err := Find(entries, name)
				if err != nil || e.Kind == KindNever || e.Kind == KindWoodpeckerAgent {
					continue // Check reports a missing entry
				}
				key, _ := s["key"].(string)
				length := "32"
				if l, ok := s["length"].(int); ok {
					length = strconv.Itoa(l)
				}
				special := "false"
				if b, _ := s["special"].(bool); b {
					special = "true"
				}
				var destinations []string
				dests, _ := s["destinations"].([]any)
				for _, d := range dests {
					if p, ok := d.(string); ok {
						destinations = append(destinations, p)
					}
				}
				for _, c := range []struct{ setting, want string }{{"key", key}, {"length", length}, {"special", special}} {
					if got := e.Settings[c.setting]; got != c.want {
						problems = append(problems, fmt.Sprintf("%s: %s is %q in the catalog, %q in platform/generated-secrets %s", name, c.setting, got, c.want, v.file))
					}
				}
				if !slices.Equal(e.Paths, destinations) {
					problems = append(problems, fmt.Sprintf("%s: paths %v in the catalog, destinations %v in platform/generated-secrets %s", name, e.Paths, destinations, v.file))
				}
			}
		}
	}
	for _, e := range entries {
		if (e.Kind == KindGenerated || e.Kind == KindAppPassword) && !seen[e.Name] {
			problems = append(problems, e.Name+": not in platform/generated-secrets, so nothing makes its first value")
		}
	}
	return problems
}
