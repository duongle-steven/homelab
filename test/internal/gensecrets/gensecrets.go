// Package gensecrets: platform/generated-secrets' entries, as its render and the sandbox's OpenBao hold them
package gensecrets

// Entry is one entry of platform/generated-secrets/values.yaml's secrets
type Entry struct {
	Name         string   `json:"name"`
	Key          string   `json:"key"`
	Length       int      `json:"length"`
	Special      bool     `json:"special"`
	Destinations []string `json:"destinations"`
}
