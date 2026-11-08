package scope

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileTests reads, with go/parser, the Test functions of a changed test file and of every other test file that uses
// something it defines (its helpers); a file that no longer exists selects nothing.
func FileTests(root string) TestNames {
	return func(file string) ([]string, error) {
		path := filepath.Join(root, file)
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if isInternalFile(file) {
			return internalTests(root, filepath.Dir(file))
		}
		changed, err := parseTestFile(path)
		if err != nil {
			return nil, err
		}
		names := changed.tests
		if len(changed.defined) == 0 {
			return names, nil
		}
		others, err := filepath.Glob(filepath.Join(root, "test", "*_test.go"))
		if err != nil {
			return nil, err
		}
		for _, other := range others {
			if other == path {
				continue
			}
			parsed, err := parseTestFile(other)
			if err != nil {
				return nil, err
			}
			for name := range changed.defined {
				if parsed.used[name] {
					names = append(names, parsed.tests...)
					break
				}
			}
		}
		return names, nil
	}
}

// testModule is the import path of test/'s Go module: test/internal/<pkg> is imported as testModule + "/internal/<pkg>"
const testModule = "github.com/duongle-steven/homelab/test"

// internalTests selects the Test functions of every test/*_test.go that imports the package in dir (test/internal/...),
// directly or through other test/internal packages
func internalTests(root, dir string) ([]string, error) {
	affected := map[string]bool{testModule + strings.TrimPrefix(filepath.ToSlash(dir), "test"): true}
	imports := map[string][]string{} // a test/internal package's import path → what its files import
	err := filepath.WalkDir(filepath.Join(root, "test", "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, err := filepath.Rel(filepath.Join(root, "test"), filepath.Dir(path))
		if err != nil {
			return err
		}
		paths, err := importPaths(path)
		pkg := testModule + "/" + filepath.ToSlash(rel)
		imports[pkg] = append(imports[pkg], paths...)
		return err
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for grew := true; grew; {
		grew = false
		for pkg, paths := range imports {
			if !affected[pkg] && anyOf(paths, affected) {
				affected[pkg], grew = true, true
			}
		}
	}
	files, err := filepath.Glob(filepath.Join(root, "test", "*_test.go"))
	if err != nil {
		return nil, err
	}
	var names []string
	seen := map[string]bool{}
	for _, path := range files {
		paths, err := importPaths(path)
		if err != nil {
			return nil, err
		}
		if !anyOf(paths, affected) {
			continue
		}
		// the importer's own tests, and those of every file using its helpers (a helper file has no tests of its own)
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		selected, err := FileTests(root)(filepath.ToSlash(rel))
		if err != nil {
			return nil, err
		}
		for _, name := range selected {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names, nil
}

func importPaths(path string) ([]string, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, spec := range parsed.Imports {
		paths = append(paths, strings.Trim(spec.Path.Value, `"`))
	}
	return paths, nil
}

func anyOf(paths []string, set map[string]bool) bool {
	for _, p := range paths {
		if set[p] {
			return true
		}
	}
	return false
}

// AllTests lists every Test function in test/*_test.go, sorted.
func AllTests(root string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(root, "test", "*_test.go"))
	if err != nil {
		return nil, err
	}
	var all []string
	for _, path := range files {
		parsed, err := parseTestFile(path)
		if err != nil {
			return nil, err
		}
		all = append(all, parsed.tests...)
	}
	sort.Strings(all)
	return all, nil
}

type testFile struct {
	tests   []string
	defined map[string]bool // top-level functions, variables, constants and types other than Test functions
	used    map[string]bool // every identifier the file mentions
}

func parseTestFile(path string) (testFile, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return testFile{}, err
	}
	f := testFile{defined: map[string]bool{}, used: map[string]bool{}}
	define := func(name string) {
		if name != "_" && name != "init" {
			f.defined[name] = true
		}
	}
	for _, decl := range parsed.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && strings.HasPrefix(d.Name.Name, "Test") {
				f.tests = append(f.tests, d.Name.Name)
			} else if d.Recv == nil {
				define(d.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, name := range s.Names {
						define(name.Name)
					}
				case *ast.TypeSpec:
					define(s.Name.Name)
				}
			}
		}
	}
	ast.Inspect(parsed, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			f.used[id.Name] = true
		}
		return true
	})
	return f, nil
}
