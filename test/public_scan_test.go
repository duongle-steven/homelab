package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// publicScanValues reads system/public-scan's values (values-<env>.yaml over values.yaml, as Helm merges them)
func publicScanValues(t *testing.T, env string) (targets []string, accepted []map[string]string, rate float64, image string) {
	t.Helper()
	files := []string{"../system/public-scan/values.yaml"}
	if env != "" {
		files = append(files, "../system/public-scan/values-"+env+".yaml")
	}
	out, err := exec.Command("yq", append([]string{"-o=json", "eval-all", ". as $item ireduce ({}; . * $item)"}, files...)...).Output()
	require.NoError(t, err)
	var v struct {
		Targets   []string            `json:"targets"`
		Accepted  []map[string]string `json:"accepted"`
		RateLimit float64             `json:"rateLimit"`
		Image     string              `json:"image"`
	}
	require.NoError(t, json.Unmarshal(out, &v))
	return v.Targets, v.Accepted, v.RateLimit, v.Image
}

// The scan's targets are the public map's hosts (system/istio-system/values.yaml public): production all of them, the
// sandbox those it serves
func TestPublicScanTargets(t *testing.T) {
	var labels []string
	for _, hosts := range publicMap(t) {
		labels = append(labels, hosts...)
	}
	sort.Strings(labels)
	targets, _, _, _ := publicScanValues(t, "")
	sort.Strings(targets)
	assert.Equal(t, labels, targets, "system/public-scan's targets differ from the public map")
	dev, _, _, _ := publicScanValues(t, "dev")
	require.NotEmpty(t, dev)
	assert.Subset(t, labels, dev, "the sandbox scans only hosts of the public map")
}

func TestPublicScanAccepted(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		targets, accepted, _, _ := publicScanValues(t, env)
		for _, a := range accepted {
			assert.NotEmpty(t, a["template"], "env=%q: an accepted finding names its template: %v", env, a)
			assert.Contains(t, targets, a["host"], "env=%q: an accepted finding names a scanned host: %v", env, a)
			assert.NotEmpty(t, strings.TrimSpace(a["reason"]), "env=%q: an accepted finding says why: %v", env, a)
		}
	}
}

// publicScanTargetsFile renders the chart with the given accepted list and returns its targets file
func publicScanTargetsFile(t *testing.T, accepted string) string {
	t.Helper()
	cmd := exec.Command("scripts/render-chart", "system/public-scan", "prod", "--set-json", "accepted="+accepted)
	cmd.Dir = ".."
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	yq := exec.Command("yq", "-N", `select(.kind == "ConfigMap" and .metadata.name == "public-scan") | .data.targets`)
	yq.Stdin = strings.NewReader(string(out))
	file, err := yq.Output()
	require.NoError(t, err)
	return string(file)
}

// An accepted finding hides its template on its own host only
func TestPublicScanAcceptedPerHost(t *testing.T) {
	file := publicScanTargetsFile(t, `[{"template":"x-1","host":"www","reason":"r"},{"template":"x-2","host":"www","reason":"r"}]`)
	lines := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(file), "\n") {
		fields := strings.Fields(line)
		require.Len(t, fields, 2, line)
		lines[fields[0]] = fields[1]
	}
	assert.Equal(t, "x-1,x-2", lines["www"])
	assert.Equal(t, "-", lines["draw"], "draw has nothing accepted")
	assert.Len(t, lines, 4, "one line per target")
}

// nuclei stays under half of Cloudflare's rate limit (infra/_modules/cloudflare): the household's own requests never
// share a block with the scan
func TestPublicScanRateBelowLimit(t *testing.T) {
	raw, err := os.ReadFile("../infra/_modules/cloudflare/variables.tf")
	require.NoError(t, err)
	value := func(name string) float64 {
		m := regexp.MustCompile(`(?s)variable "` + name + `" \{.*?default\s*=\s*(\d+)`).FindSubmatch(raw)
		require.NotNil(t, m, name)
		n, err := strconv.ParseFloat(string(m[1]), 64)
		require.NoError(t, err)
		return n
	}
	_, _, rate, _ := publicScanValues(t, "")
	require.Positive(t, rate)
	assert.LessOrEqual(t, rate*value("rate_limit_period")*2, value("rate_limit_requests"))
}

// The log gets one short line per finding (template, host, severity): nuclei's own lines hold the request as a curl
// command and what the template extracted from the response
func TestPublicScanReport(t *testing.T) {
	_, _, _, image := publicScanValues(t, "")
	script, err := filepath.Abs("../system/public-scan/files/scan.sh")
	require.NoError(t, err)
	found := `{"template-id":"git-config","info":{"name":"Git Config","severity":"medium","metadata":{"max-request":1}},"host":"10.0.0.1","matched-at":"https://draw.example.org/.git/config","extracted-results":["secret-value"],"curl-command":"curl -H 'Cookie: secret-value' https://draw.example.org/.git/config","matcher-status":true}`
	cmd := exec.Command("docker", "run", "--rm", "--entrypoint", "sh", "-e", "PUBLIC_SCAN_DOMAIN=example.org",
		"-e", "PUBLIC_SCAN_REPORT=/tmp/found.jsonl", "-v", script+":/scan.sh:ro", image,
		"-c", "printf '%s\\n' '"+found+"' > /tmp/found.jsonl && sh /scan.sh draw")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, `{"public_scan":"finding","template":"git-config","host":"draw.example.org","severity":"medium"}`, strings.TrimSpace(string(out)))
	assert.NotContains(t, string(out), "secret-value")
}

// The scan's preflight, in the scan's own image: a host must answer normally, or the scan did not really run
func TestPublicScanScript(t *testing.T) {
	_, _, _, image := publicScanValues(t, "")
	script, err := filepath.Abs("../system/public-scan/files/scan.sh")
	require.NoError(t, err)
	cases := map[string]struct {
		handler http.HandlerFunc
		ok      bool
	}{
		"answers": {func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "<title>ntfy</title>") }, true},
		"redirects": {func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/app", http.StatusFound)
				return
			}
			fmt.Fprint(w, "app")
		}, true},
		"5xx": {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }, false},
		"challenged": {func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("cf-mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
		}, false},
		"challenge page": {func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, "<html><head><title>Just a moment...</title></head></html>")
		}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(c.handler)
			defer server.Close()
			target := strings.TrimPrefix(server.URL, "http://")
			cmd := exec.Command("docker", "run", "--rm", "--network", "host", "--entrypoint", "sh",
				"-e", "PUBLIC_SCAN_CHECK_ONLY=1", "-e", "PUBLIC_SCAN_SCHEME=http", "-e", "PUBLIC_SCAN_DOMAIN=",
				"-e", "PUBLIC_SCAN_TARGETS=/tmp/targets", "-v", script+":/scan.sh:ro", image,
				"-c", "echo '"+target+" -' > /tmp/targets && sh /scan.sh")
			out, err := cmd.CombinedOutput()
			if c.ok {
				assert.NoError(t, err, string(out))
				return
			}
			assert.Error(t, err, string(out))
			assert.Contains(t, string(out), target, "the failure names the host")
			assert.NotContains(t, string(out), "Just a moment", "no response body in the log")
		})
	}
}

// PublicScanFinding (system/loki/files/public-scan-rules.yaml): Loki's ruler loads it beside the audit rules, and it
// names only the template and the host
func TestPublicScanRuleRender(t *testing.T) {
	rules := renderLoki(t, "", `select(.kind == "ConfigMap" and .metadata.name == "loki-audit-rules") | .data["public-scan-rules.yaml"]`)
	require.NotContains(t, []string{"", "null"}, rules, "loki-audit-rules has no public-scan-rules.yaml")
	file := filepath.Join(t.TempDir(), "public-scan-rules.yaml")
	require.NoError(t, os.WriteFile(file, []byte(rules), 0o600))
	for _, cmd := range [][]string{{"rules", "lint", file}, {"rules", "check", file}} {
		out, err := exec.Command("lokitool", cmd...).CombinedOutput()
		require.NoError(t, err, "lokitool %s: %s", cmd[1], out)
	}
	var parsed struct {
		Groups []struct {
			Rules []struct {
				Alert       string            `yaml:"alert"`
				Expr        string            `yaml:"expr"`
				Labels      map[string]string `yaml:"labels"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(rules), &parsed))
	require.Len(t, parsed.Groups, 1)
	require.Len(t, parsed.Groups[0].Rules, 1)
	r := parsed.Groups[0].Rules[0]
	assert.Equal(t, "PublicScanFinding", r.Alert)
	assert.Contains(t, r.Expr, `namespace="public-scan"`)
	assert.Contains(t, r.Expr, `sum by (template, host)`)
	assert.Contains(t, r.Expr, `=~"medium|high|critical"`)
	assert.Contains(t, r.Expr, `[1h]`)
	assert.Equal(t, "warning", r.Labels["severity"])
	labels := regexp.MustCompile(`\$labels\.(\w+)`).FindAllStringSubmatch(r.Annotations["summary"]+r.Annotations["description"], -1)
	for _, l := range labels {
		assert.Contains(t, []string{"template", "host"}, l[1], "the alert names only the template and the host")
	}
}
