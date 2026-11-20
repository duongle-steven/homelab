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

// GroupState decides about a sandbox deploy group: skip its specs when it was not asked for and is absent, fail when it
// was asked for and is absent (the release sandbox must never go green without it) or when the cluster cannot say
func GroupState(group string, wanted []string, present bool, err error) (skip bool, failure string) {
	switch {
	case err != nil:
		return false, fmt.Sprintf("cannot tell whether the group %s is deployed: %v", group, err)
	case present:
		return false, ""
	case slices.Contains(wanted, group):
		return false, fmt.Sprintf("the group %s was asked for (HOMELAB_SANDBOX_GROUPS) but the cluster Secret does not carry it", group)
	}
	return true, ""
}
