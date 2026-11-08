// Package smoke lists the apps the smoke tests reach in each environment
package smoke

import (
	"fmt"
	"slices"
)

// App is a workload the smoke tests reach: its name and namespace
type App struct {
	Name      string
	Namespace string
}

// Apps returns the apps whose HTTPRoute is checked; the sandbox only runs a subset.
func Apps(env string) []App {
	if env == "dev" {
		return []App{
			{"argocd-server", "argocd"},
			{"excalidraw", "excalidraw"},
			{"kanidm", "kanidm"},
			{"paperless", "paperless"},
			{"grafana", "grafana"},
			{"zot", "zot"},
			{"actualbudget", "actualbudget"},
			{"homepage", "homepage"},
			{"pairdrop", "pairdrop"},
			{"speedtest", "speedtest"},
			{"forgejo", "forgejo"},
			{"opencloud", "opencloud"},
			{"ntfy", "ntfy"},
		}
	}

	return []App{
		{"argocd-server", "argocd"},
		{"forgejo", "forgejo"},
		{"grafana", "grafana"},
		{"homepage", "homepage"},
		{"kanidm", "kanidm"},
		{"zot", "zot"},
	}
}

// OptionalAppState decides about an optional app: skip it when it was not asked for and is absent, fail when it was
// asked for and is absent (the release sandbox must never go green without it) or when the cluster cannot say
func OptionalAppState(app string, wanted []string, present bool, err error) (skip bool, failure string) {
	switch {
	case err != nil:
		return false, fmt.Sprintf("cannot tell whether %s is deployed: %v", app, err)
	case present:
		return false, ""
	case slices.Contains(wanted, app):
		return false, fmt.Sprintf("%s was asked for (HOMELAB_SANDBOX_APPS) but its Application does not exist", app)
	}
	return true, ""
}
