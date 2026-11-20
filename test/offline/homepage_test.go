package offline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// homepage's services are generated from each app's and component's description (lab catalog generate, Backstage-1):
// every link the hand-written list held before stays, and Backstage (Backstage-1) joins them
func TestHomepageServicesKept(t *testing.T) {
	services := renderVelero(t, "apps/homepage.yaml", "", `select(.kind == "ConfigMap") | .data."services.yaml" // ""`)
	for _, host := range []string{"chat", "jellyfin", "jellyseerr", "paperless", "drive", "transmission", "prowlarr", "radarr",
		"sonarr", "auth", "git", "ci", "argocd", "registry", "grafana", "draw", "speedtest", "backstage", "kargo"} {
		assert.Contains(t, services, "href: https://"+host+".", "homepage lost %s", host)
	}
	for _, group := range []string{"Popular", "Management", "Development", "Utilities"} {
		assert.Contains(t, services, "- "+group+":")
	}
	assert.Equal(t, 19, strings.Count(services, "href: "), "homepage's entries:\n%s", services)
}
