package test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderedRoute is what the public allow-list checks of a rendered HTTPRoute
type renderedRoute struct {
	Namespace, Name, Gateway string
	Hostnames                []string
	Proxied                  bool
}

// publicMap reads system/istio-system/values.yaml's public: namespace -> its public hosts' first labels
func publicMap(t *testing.T) map[string][]string {
	t.Helper()
	out, err := exec.Command("yq", "-o=json", ".public", "../system/istio-system/values.yaml").Output()
	require.NoError(t, err)
	var public map[string][]string
	require.NoError(t, json.Unmarshal(out, &public))
	require.NotEmpty(t, public, "system/istio-system/values.yaml has no public map")
	return public
}

// publicRouteProblems names each route on Gateway public that the map does not allow: from a namespace off the map,
// with a host the map does not list for its namespace, or not proxied by Cloudflare (a DNS-only Tunnel host does not
// work, and skips the WAF and the rate limit). Routes on other Gateways are not its business.
func publicRouteProblems(routes []renderedRoute, public map[string][]string, domain string) []string {
	var problems []string
	for _, r := range routes {
		if r.Gateway != "public" {
			continue
		}
		id := r.Namespace + "/HTTPRoute/" + r.Name
		labels, ok := public[r.Namespace]
		if !ok {
			problems = append(problems, id+": its namespace is not in system/istio-system's public map")
			continue
		}
		// A route without hostnames takes the listener's *.<domain>: every host under the domain
		if len(r.Hostnames) == 0 {
			problems = append(problems, id+": no hostnames, so it serves every host of the listener")
		}
		for _, host := range r.Hostnames {
			allowed := false
			for _, label := range labels {
				allowed = allowed || host == label+"."+domain
			}
			if !allowed {
				problems = append(problems, id+": host "+host+" is not in the public map for "+r.Namespace)
			}
		}
		if !r.Proxied {
			problems = append(problems, id+": no external-dns.kubernetes.io/cloudflare-proxied: \"true\"")
		}
	}
	return problems
}

func TestPublicRoutes(t *testing.T) {
	public := map[string][]string{"blog": {"www"}, "matrix": {"chat"}}
	cases := map[string]struct {
		routes []renderedRoute
		want   int
	}{
		"all fine":                              {[]renderedRoute{{"blog", "blog", "public", []string{"www.example.org"}, true}}, 0},
		"namespace off the map":                 {[]renderedRoute{{"argocd", "argocd", "public", []string{"argocd.example.org"}, true}}, 1},
		"listed namespace, unlisted host":       {[]renderedRoute{{"matrix", "admin", "public", []string{"admin.example.org"}, true}}, 1},
		"not proxied":                           {[]renderedRoute{{"blog", "blog", "public", []string{"www.example.org"}, false}}, 1},
		"listed namespace on the LAN Gateway":   {[]renderedRoute{{"matrix", "dendrite", "gateway", []string{"matrix.example.org"}, false}}, 0},
		"no hostname on public":                 {[]renderedRoute{{"blog", "any", "public", nil, true}}, 1},
		"unlisted namespace on the LAN Gateway": {[]renderedRoute{{"argocd", "argocd", "gateway", []string{"argocd.example.org"}, false}}, 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			problems := publicRouteProblems(c.routes, public, "example.org")
			assert.Len(t, problems, c.want, "%v", problems)
		})
	}
	off := publicRouteProblems([]renderedRoute{{"argocd", "argocd", "public", []string{"argocd.example.org"}, true}}, public, "example.org")
	require.Len(t, off, 1)
	assert.Contains(t, off[0], "argocd/HTTPRoute/argocd")

	t.Run("the repository", func(t *testing.T) {
		public := publicMap(t)
		raw, err := os.ReadFile("../infra/production/cluster/config.json")
		require.NoError(t, err)
		var config envConfig
		require.NoError(t, json.Unmarshal(raw, &config))
		require.NotEmpty(t, config.Domain)

		dir := t.TempDir()
		cmd := exec.Command("scripts/render-all", "prod", dir)
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
		require.NoError(t, err)
		var routes []renderedRoute
		for _, file := range files {
			out, err := exec.Command("yq", "-o=json", "-I=0", `select(.kind == "HTTPRoute") | {"ns": .metadata.namespace, "name": .metadata.name, "hosts": .spec.hostnames, "proxied": .metadata.annotations["external-dns.kubernetes.io/cloudflare-proxied"], "parents": [.spec.parentRefs[] | select((.namespace // "") == "istio-system") | .name]}`, file).Output()
			require.NoError(t, err, file)
			// Components render without a namespace: Argo CD puts them in the one named after their directory
			fallback := strings.TrimSuffix(filepath.Base(file), ".yaml")
			fallback = fallback[strings.LastIndex(fallback, "_")+1:]
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if line == "" {
					continue
				}
				var r struct {
					NS, Name, Proxied string
					Hosts, Parents    []string
				}
				require.NoError(t, json.Unmarshal([]byte(line), &r), line)
				if r.NS == "" {
					r.NS = fallback
				}
				for _, parent := range r.Parents {
					routes = append(routes, renderedRoute{r.NS, r.Name, parent, r.Hosts, r.Proxied == "true"})
				}
			}
		}
		assert.NotEmpty(t, publicRouteProblems([]renderedRoute{{"argocd", "x", "public", nil, false}}, public, config.Domain), "the check sees an unlisted route")
		assert.Empty(t, publicRouteProblems(routes, public, config.Domain))

		var listed int
		for _, r := range routes {
			if r.Gateway == "public" {
				listed++
			}
		}
		assert.GreaterOrEqual(t, listed, len(public), "every namespace of the map has a route on public")

		keys := make([]string, 0, len(public))
		for ns := range public {
			keys = append(keys, ns)
		}
		sort.Strings(keys)
		selector := renderYq(t, "system/istio-system", "", `select(.kind == "Gateway" and .metadata.name == "public") | .spec.listeners[] | select(.name == "https") | .allowedRoutes.namespaces | tojson`)
		var namespaces struct {
			From     string
			Selector struct {
				MatchExpressions []struct {
					Key, Operator string
					Values        []string
				} `json:"matchExpressions"`
			}
		}
		require.NoError(t, json.Unmarshal([]byte(selector), &namespaces), selector)
		assert.Equal(t, "Selector", namespaces.From)
		require.Len(t, namespaces.Selector.MatchExpressions, 1)
		expr := namespaces.Selector.MatchExpressions[0]
		assert.Equal(t, "kubernetes.io/metadata.name", expr.Key)
		assert.Equal(t, "In", expr.Operator)
		assert.Equal(t, keys, expr.Values)
	})
}
