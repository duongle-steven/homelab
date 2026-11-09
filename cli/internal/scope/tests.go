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
	"strconv"
	"strings"
)

// Spec is one top-level container of test/e2e: its name (Describe's text), its labels and its file.
type Spec struct {
	Name   string
	Labels []string
	File   string
}

// Specs reads, with go/parser, the top-level Describe containers of test/e2e/*_test.go and their Label(...) string
// literals, sorted by name.
func Specs(root string) ([]Spec, error) {
	files, err := filepath.Glob(filepath.Join(root, "test", "e2e", "*_test.go"))
	if err != nil {
		return nil, err
	}
	var specs []Spec
	for _, path := range files {
		parsed, err := parseTestFile(path)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		for _, spec := range parsed.specs {
			spec.File = filepath.ToSlash(rel)
			specs = append(specs, spec)
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil
}

// FileTests gives the label filters of a changed spec file's containers and of the containers of every other spec file
// that uses something it defines (its helpers), each the AND of a container's labels without the environment and alone;
// a deleted spec file selects nothing. A test/internal file, deleted or not, selects through the spec files that import
// its package.
func FileTests(root string) FileFilters {
	return func(file string) ([]string, error) {
		if isInternalFile(file) {
			return internalTests(root, filepath.Dir(file))
		}
		path := filepath.Join(root, file)
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		changed, err := parseTestFile(path)
		if err != nil {
			return nil, err
		}
		filters := changed.filters()
		if len(changed.defined) == 0 {
			return filters, nil
		}
		others, err := filepath.Glob(filepath.Join(root, "test", "e2e", "*_test.go"))
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
					filters = append(filters, parsed.filters()...)
					break
				}
			}
		}
		return filters, nil
	}
}

// testModule is the import path of test/'s Go module: test/internal/<pkg> is imported as testModule + "/internal/<pkg>"
const testModule = "github.com/duongle-steven/homelab/test"

// internalTests selects the containers of every test/e2e/*_test.go that imports the package in dir (test/internal/...),
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
	files, err := filepath.Glob(filepath.Join(root, "test", "e2e", "*_test.go"))
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
		// the importer's own containers, and those of every file using its helpers (a helper file has none of its own)
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

type testFile struct {
	specs   []Spec
	defined map[string]bool // top-level functions, variables, constants and types
	used    map[string]bool // every identifier the file mentions
}

// filters are the file's containers as label filters: the AND of each one's labels but the environment and alone
func (f testFile) filters() []string {
	var filters []string
	for _, spec := range f.specs {
		var labels []string
		for _, label := range spec.Labels {
			if !envLabels[label] && label != AloneLabel {
				labels = append(labels, label)
			}
		}
		if len(labels) > 0 {
			filters = append(filters, strings.Join(labels, " && "))
		}
	}
	return filters
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
			if d.Recv == nil {
				define(d.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, name := range s.Names {
						define(name.Name)
					}
					if spec, ok := container(s); ok {
						f.specs = append(f.specs, spec)
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

// container reads `var _ = Describe("Name", ..., Label("a", "b"), ...)`: its text and its labels
func container(s *ast.ValueSpec) (Spec, bool) {
	if len(s.Names) != 1 || s.Names[0].Name != "_" || len(s.Values) != 1 {
		return Spec{}, false
	}
	call, ok := s.Values[0].(*ast.CallExpr)
	if !ok || callName(call) != "Describe" || len(call.Args) == 0 {
		return Spec{}, false
	}
	name, ok := stringLit(call.Args[0])
	if !ok {
		return Spec{}, false
	}
	spec := Spec{Name: name}
	for _, arg := range call.Args[1:] {
		if label, ok := arg.(*ast.CallExpr); ok && callName(label) == "Label" {
			for _, a := range label.Args {
				if value, ok := stringLit(a); ok {
					spec.Labels = append(spec.Labels, value)
				}
			}
		}
	}
	return spec, true
}

func callName(call *ast.CallExpr) string {
	if id, ok := call.Fun.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func stringLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}
