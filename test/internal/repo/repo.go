// Package repo locates the repository from the suite's packages, which sit at different depths under test/
package repo

import (
	"path/filepath"
	"runtime"
)

// Root is the repository's root: three levels above this file (test/internal/repo)
func Root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}
