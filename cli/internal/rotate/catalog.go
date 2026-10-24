// Package rotate is the catalog of every secret the homelab holds and how each is replaced
// (docs/superpowers/specs/2026-10-24-secrets-rotation-design.md).
package rotate

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/workload"
	"go.yaml.in/yaml/v3"
)

// Kind says how a secret is replaced.
type Kind string

const (
	KindGenerated       Kind = "generated"        // random value in OpenBao, refresh the readers, restart
	KindAppPassword     Kind = "app-password"     // change it in the app, then in OpenBao
	KindNtfyOverlap     Kind = "ntfy-overlap"     // ntfy lists two tokens for a while
	KindForgejoToken    Kind = "forgejo-token"    // a new Forgejo access token, then the old one deleted
	KindForgejoOAuth    Kind = "forgejo-oauth"    // Forgejo regenerates an OAuth2 application's secret
	KindKaniopClient    Kind = "kaniop-client"    // kaniop.rs/force-secret-rotation, then restart the readers
	KindKanidmAdmin     Kind = "kanidm-admin"     // kaniop writes new admin passwords when their Secret is deleted
	KindOpenTofu        Kind = "opentofu"         // -replace, apply, push
	KindWoodpeckerAgent Kind = "woodpecker-agent" // a new shared agent secret, every agent row deleted, the agents registered again
	KindWoodpeckerToken Kind = "woodpecker-token" // Woodpecker's token reset for the admin user
	KindCNPG            Kind = "cnpg-password"    // CloudNativePG's password in its own Secret, with the derived keys
	KindManual          Kind = "manual"           // a person acts; lab gives the steps
	KindScript          Kind = "script"           // an existing script does it; lab points to it
	KindNever           Kind = "never"            // not rotated, with the reason
	KindAutomatic       Kind = "automatic"        // already short-lived
)

var kinds = []Kind{
	KindGenerated, KindAppPassword, KindNtfyOverlap, KindForgejoToken, KindForgejoOAuth, KindKaniopClient,
	KindKanidmAdmin, KindOpenTofu, KindWoodpeckerAgent, KindWoodpeckerToken, KindCNPG, KindManual, KindScript,
	KindNever, KindAutomatic,
}

// Reader is whoever reads a secret: a namespace's ExternalSecrets and workloads, or people.
type Reader struct {
	Namespace       string         `yaml:"namespace"`
	ExternalSecrets []string       `yaml:"externalSecrets"`
	Workloads       []workload.Ref `yaml:"workloads"`
	// People re-enter the value themselves (the ntfy phone entries), so it never rotates on a schedule
	People bool `yaml:"people"`
}

// Entry is one secret.
type Entry struct {
	Name     string            `yaml:"name"`
	Kind     Kind              `yaml:"kind"`
	Class    int               `yaml:"class"`  // 0 = official overlap, 1, 2 or 3
	Reason   string            `yaml:"reason"` // why this class, or why never
	Period   time.Duration     `yaml:"period"` // 0 = command only
	Paths    []string          `yaml:"paths"`  // OpenBao paths under secret/ (every destination of a generated secret)
	Readers  []Reader          `yaml:"readers"`
	Pending  string            `yaml:"pending"`  // the task that creates what it names; skips the repository consistency check
	Steps    string            `yaml:"steps"`    // what a person does, for manual and script entries
	Idle     string            `yaml:"idle"`     // "woodpecker": wait for no running pipeline
	Settings map[string]string `yaml:"settings"` // method-specific: app, Forgejo account, OpenTofu unit and resource, kaniop client
}

//go:embed catalog.yaml
var embedded []byte

// Load parses and validates the embedded catalog.
func Load() ([]Entry, error) { return parse(embedded) }

func parse(data []byte) ([]Entry, error) {
	var entries []Entry
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&entries); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Name == "" {
			return nil, errors.New("catalog: an entry has no name")
		}
		if seen[e.Name] {
			return nil, fmt.Errorf("catalog: %s: duplicate name", e.Name)
		}
		seen[e.Name] = true
		if err := e.validate(); err != nil {
			return nil, fmt.Errorf("catalog: %s: %w", e.Name, err)
		}
	}
	return entries, nil
}

func (e Entry) validate() error {
	if !slices.Contains(kinds, e.Kind) {
		return fmt.Errorf("unknown kind %q", e.Kind)
	}
	if e.Class < 0 || e.Class > 3 {
		return fmt.Errorf("class %d is not 0, 1, 2 or 3", e.Class)
	}
	if e.Class == 3 && e.Settings["workaround"] == "" {
		return errors.New("class 3 needs Settings[workaround]")
	}
	if e.Kind == KindNever && e.Reason == "" {
		return errors.New("never needs a reason")
	}
	if e.Period < 0 {
		return errors.New("negative period")
	}
	if e.Period > 0 && e.Kind == KindManual {
		return errors.New("a manual entry cannot rotate on a schedule")
	}
	if e.Period > 0 && slices.ContainsFunc(e.Readers, func(r Reader) bool { return r.People }) {
		return errors.New("people must re-enter this value, so it cannot rotate on a schedule")
	}
	return nil
}

// Find returns the entry called name.
func Find(entries []Entry, name string) (Entry, error) {
	for _, e := range entries {
		if e.Name == name {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("no secret %q in the catalog (lab secrets list)", name)
}
