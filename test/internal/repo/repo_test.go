package repo

import (
	"os"
	"path/filepath"
	"testing"
)

// The root is the repository's, whatever the package's directory: the suite's packages sit at different depths
func TestRootIsTheRepository(t *testing.T) {
	if _, err := os.Stat(filepath.Join(Root(), "test", "scopes.yaml")); err != nil {
		t.Fatalf("Root() = %q: %v", Root(), err)
	}
}
