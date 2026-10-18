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

// FileTests reads a test file's Test functions with go/parser; a file that no longer exists defines none.
func FileTests(root string) TestNames {
	return func(file string) ([]string, error) {
		path := filepath.Join(root, file)
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				names = append(names, fn.Name.Name)
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
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		names, err := FileTests(root)(filepath.ToSlash(rel))
		if err != nil {
			return nil, err
		}
		all = append(all, names...)
	}
	sort.Strings(all)
	return all, nil
}
