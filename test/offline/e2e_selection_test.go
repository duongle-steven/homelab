package offline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eSelection is fixtures/e2e-selection.json: which cluster tests each test/scopes.yaml group, full, always, alone
// term and recipe ran before the suite moved to Ginkgo (14f-2), the selection the labels must keep.
type e2eSelection struct {
	// The commit the record was generated from
	From string `json:"from"`
	// The 84 cluster tests, without Test
	Tests []string `json:"tests"`
	// Group (its first path), full, always, alone/<term> or recipe → the tests it ran
	Selection map[string][]string `json:"selection"`
}

func loadE2ESelection(t *testing.T) e2eSelection {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repo.Root(), "test/offline/fixtures/e2e-selection.json"))
	require.NoError(t, err)
	var record e2eSelection
	require.NoError(t, json.Unmarshal(body, &record))
	return record
}

func TestE2ESelectionRecord(t *testing.T) {
	record := loadE2ESelection(t)
	assert.NotEmpty(t, record.From)
	require.Len(t, record.Tests, 84)
	known := map[string]bool{}
	for _, name := range record.Tests {
		known[name] = true
	}
	covered := map[string]bool{}
	for key, names := range record.Selection {
		for _, name := range names {
			assert.True(t, known[name], "%s names %s, not a cluster test", key, name)
		}
		if key == "production" {
			continue
		}
		for _, name := range names {
			covered[name] = true
		}
	}
	for _, name := range record.Tests {
		if name == "ArgoCDCheck" {
			assert.False(t, covered[name], "ArgoCDCheck runs only against production")
			assert.Contains(t, record.Selection["production"], name)
			continue
		}
		assert.True(t, covered[name], "%s: no scope or recipe runs it", name)
	}
}
