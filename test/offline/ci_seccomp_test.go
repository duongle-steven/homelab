package offline

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The golden path's Buildah step gets its Localhost seccomp profile from a native MutatingAdmissionPolicy, and a
// ValidatingAdmissionPolicy refuses it, or any other Localhost profile, to every other pod in namespace ci (step 15h,
// moved with step 10f). Argo adds its own containers (init and wait, argoexec) to every pod, so a build is the pod the
// template labels homelab/ci-step=build whose main container is Buildah, whose other containers are argoexec, in its
// own user namespace
func TestCISeccompPolicies(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		kinds := render.Yq(t, "platform/ci", env, `select(.kind == "*AdmissionPolicy*" and (.metadata.name | test("seccomp"))) | .kind + " " + .metadata.name + " " + (.spec.failurePolicy // "-")`)
		assert.ElementsMatch(t, []string{
			"MutatingAdmissionPolicy ci-buildah-seccomp Fail",
			"MutatingAdmissionPolicyBinding ci-buildah-seccomp -",
			"ValidatingAdmissionPolicy ci-localhost-seccomp Fail",
			"ValidatingAdmissionPolicyBinding ci-localhost-seccomp -",
		}, lines(kinds), "env=%q", env)
		assert.Equal(t, "Deny", render.Yq(t, "platform/ci", env, `select(.kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == "ci-localhost-seccomp") | .spec.validationActions[]`))
		for _, kind := range []string{"MutatingAdmissionPolicyBinding", "ValidatingAdmissionPolicyBinding"} {
			assert.Equal(t, "ci", render.Yq(t, "platform/ci", env,
				`select(.kind == "`+kind+`" and (.metadata.name | test("seccomp"))) | .spec.matchResources.namespaceSelector.matchLabels["kubernetes.io/metadata.name"]`), kind)
		}
	}

	work := t.TempDir()
	fixtures := filepath.Join(repo.Root(), "test", "offline", "fixtures", "ci-seccomp", "pods.yaml")
	// The CLI evaluates the policies' CEL, not the bindings' selectors (checked above). Kyverno's CLI 1.19 applies
	// MutatingAdmissionPolicy only as v1alpha1, whose fields these use unchanged; one query per document
	policy := func(kinds ...string) string {
		var docs []string
		for _, kind := range kinds {
			docs = append(docs, render.Yq(t, "platform/ci", "", `select(.kind == "`+kind+`" and (.metadata.name | test("seccomp"))) | del(.spec.matchResources)`))
		}
		return strings.Join(docs, "\n---\n")
	}
	mutating := filepath.Join(work, "mutating.yaml")
	writeFile(t, mutating, strings.ReplaceAll(policy("MutatingAdmissionPolicy", "MutatingAdmissionPolicyBinding"),
		"admissionregistration.k8s.io/v1\n", "admissionregistration.k8s.io/v1alpha1\n"))
	validating := filepath.Join(work, "validating.yaml")
	writeFile(t, validating, policy("ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"))

	// Admission order: the mutation first, then the guard sees what it produced
	mutated := filepath.Join(work, "mutated.yaml")
	kyvernoApply(t, mutating, fixtures, "-o", mutated)
	pods := map[string]map[string]any{}
	for _, file := range []string{fixtures, mutated} {
		for _, doc := range yamlDocs(t, file) {
			if doc["kind"] == "Pod" {
				pods[doc["metadata"].(map[string]any)["name"].(string)] = doc
			}
		}
	}
	var profiled []string
	for name, pod := range pods {
		if profile, _ := yqValue(pod, "spec", "securityContext", "seccompProfile", "localhostProfile").(string); profile == "homelab/buildah.json" {
			profiled = append(profiled, name)
		}
	}
	sort.Strings(profiled)
	// The mutation sets it on builds that name no profile; the ones asking keep what they asked
	assert.Equal(t, []string{"alpine-asks", "build", "build-asks", "build-digest", "build-tag-digest", "host-users-asks",
		"no-label-asks", "sidecar-asks", "stable-evil-asks"}, profiled)

	final := filepath.Join(work, "final.yaml")
	var docs []string
	for _, pod := range pods {
		out, err := yaml.Marshal(pod)
		require.NoError(t, err)
		docs = append(docs, string(out))
	}
	writeFile(t, final, strings.Join(docs, "---\n"))
	out := kyvernoApply(t, validating, final)
	var denied []string
	for _, m := range regexp.MustCompile(`resource ci/Pod/(\S+) failed`).FindAllStringSubmatch(out, -1) {
		denied = append(denied, m[1])
	}
	sort.Strings(denied)
	assert.Equal(t, []string{"alpine-asks", "build-other-profile", "host-users-asks", "no-label-asks", "sidecar-asks",
		"stable-evil-asks"}, denied, out)
	assert.Contains(t, out, "only the golden path's Buildah step may use a Localhost seccomp profile (homelab/buildah.json)")
}

func kyvernoApply(t *testing.T, policy, resources string, args ...string) string {
	t.Helper()
	cmd := exec.Command("kyverno", append([]string{"apply", policy, "--resource", resources}, args...)...)
	out, err := cmd.CombinedOutput()
	// kyverno apply exits 1 when a resource fails a policy
	if err != nil && !strings.Contains(string(out), "fail: ") {
		require.NoError(t, err, string(out))
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content+"\n"), 0o600))
}

func yamlDocs(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var docs []map[string]any
	dec := yaml.NewDecoder(f)
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

func yqValue(doc map[string]any, path ...string) any {
	var cur any = doc
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
