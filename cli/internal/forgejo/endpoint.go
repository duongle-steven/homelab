package forgejo

import (
	"net/http"
	"time"
)

// Endpoint is how lab reaches one environment's Forgejo: its URL, an HTTP client for the API and git's extra
// configuration (git -c values).
type Endpoint struct {
	URL  string
	HTTP *http.Client
	Git  []string
}

// ProductionEndpoint is Forgejo on the LAN Gateway, https://git.<domain>.
func ProductionEndpoint(domain string) Endpoint {
	return Endpoint{URL: "https://git." + domain, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// RepositoryURL is ops/homelab's clone URL.
func (ep Endpoint) RepositoryURL() string { return ep.URL + "/" + Org + "/" + Repo + ".git" }
