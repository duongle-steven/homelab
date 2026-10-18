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
