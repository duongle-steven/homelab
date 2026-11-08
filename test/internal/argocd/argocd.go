// Package argocd reads Argo CD's Applications for the e2e suite's start: whether the environment has settled
package argocd

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Unsynced lists the Applications in `kubectl get applications -o json` that are still coming: not Synced, or Synced
// but Progressing or Missing (as "name sync/health", sorted). A Degraded one counts as settled: it fails its own specs.
func Unsynced(raw []byte) ([]string, error) {
	var apps struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Sync struct {
					Status string `json:"status"`
				} `json:"sync"`
				Health struct {
					Status string `json:"status"`
				} `json:"health"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &apps); err != nil {
		return nil, err
	}
	if len(apps.Items) == 0 {
		return []string{"no Application yet"}, nil
	}
	var waiting []string
	for _, a := range apps.Items {
		sync, health := a.Status.Sync.Status, a.Status.Health.Status
		if sync != "Synced" || health == "Progressing" || health == "Missing" || health == "" {
			waiting = append(waiting, fmt.Sprintf("%s %s/%s", a.Metadata.Name, sync, health))
		}
	}
	sort.Strings(waiting)
	return waiting, nil
}
