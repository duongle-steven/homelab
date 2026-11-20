package offline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/duongle-steven/homelab/test/internal/scopes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every sandbox spec that names a group's namespace (a string literal such as "excalidraw" or "forgejo") carries a
// label whose group deploys it: just dev deploys only the groups the selected specs' labels need, so a spec reading
// another group's component would wait for it and fail (the sandbox-groups review found eleven)
func TestSpecsNamespacesDeployed(t *testing.T) {
	m, err := scopes.Load()
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(repo.Root(), "test", "e2e", "*_test.go"))
	require.NoError(t, err)
	// Names a spec uses without reaching the cluster
	notDeployed := map[string][]string{
		// optOutAllowed is a pure function of a namespace and a pod name
		"MeshOptOutAllowed": {"forgejo", "excalidraw"},
	}
	fset := token.NewFileSet()
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				for _, value := range spec.(*ast.ValueSpec).Values {
					call, ok := value.(*ast.CallExpr)
					if !ok || !isIdent(call.Fun, "Describe") || len(call.Args) == 0 {
						continue
					}
					name, _ := strconv.Unquote(call.Args[0].(*ast.BasicLit).Value)
					var labels []string
					var literals []string
					// A namespace the spec checks with appDeployed or skipUnlessDeployed is optional to it
					guarded := map[string]bool{}
					for _, ns := range notDeployed[name] {
						guarded[ns] = true
					}
					ast.Inspect(call, func(n ast.Node) bool {
						if c, ok := n.(*ast.CallExpr); ok && (isIdent(c.Fun, "appDeployed") || isIdent(c.Fun, "skipUnlessDeployed")) && len(c.Args) == 2 {
							if lit, ok := c.Args[1].(*ast.BasicLit); ok {
								v, _ := strconv.Unquote(lit.Value)
								guarded[v] = true
							}
						}
						return true
					})
					ast.Inspect(call, func(n ast.Node) bool {
						if c, ok := n.(*ast.CallExpr); ok && isIdent(c.Fun, "Label") {
							for _, a := range c.Args {
								if lit, ok := a.(*ast.BasicLit); ok {
									l, _ := strconv.Unquote(lit.Value)
									labels = append(labels, l)
								}
							}
							return false
						}
						if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if v, err := strconv.Unquote(lit.Value); err == nil {
								literals = append(literals, v)
							}
						}
						return true
					})
					if !slices.Contains(labels, "sandbox") {
						continue
					}
					deployed := m.WithNeeds(m.GroupsOfLabels(labels))
					for _, lit := range literals {
						if g := m.GroupOfNamespace(lit); g != "" && !guarded[lit] && !slices.Contains(deployed, g) {
							assert.Fail(t, "spec needs a group its labels do not deploy",
								"%s (%s, labels %v) names namespace %q of group %s: add a label of that group, or the label to the group",
								name, filepath.Base(file), labels, lit, g)
						}
					}
				}
			}
		}
	}
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}
