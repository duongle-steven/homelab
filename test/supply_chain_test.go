package test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	labImage         = "ghcr.io/duongle-steven/homelab-lab*"
	labImageIssuer   = "https://token.actions.githubusercontent.com"
	labImageWorkflow = "https://github.com/duongle-steven/homelab/.github/workflows/lab-image.yaml@refs/heads/master"
)

// renderKyverno renders system/kyverno for env ("" or dev) and runs a yq expression on it
func renderKyverno(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/kyverno $1 | yq -N "$0"`, expr, env)
	cmd.Dir = ".."
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return strings.TrimSpace(string(out))
}

// lab's image is signed by lab-image.yaml on master, keyless, with its SBOM attested; production's Kyverno checks
// both (Audit until 15b-2); the sandbox runs lab's image built from the checkout, unsigned, so it has no such policy
func TestLabImagePolicy(t *testing.T) {
	policy := `select(.kind == "ImageValidatingPolicy" and .metadata.name == "verify-lab-image")`
	assert.Equal(t, "verify-lab-image", renderKyverno(t, "", `[select(.kind == "ImageValidatingPolicy")] | map(.metadata.name) | join(",")`))
	assert.Equal(t, labImage, renderKyverno(t, "", policy+` | .spec.matchImageReferences[].glob`))
	assert.Equal(t, "Audit", renderKyverno(t, "", policy+` | .spec.validationActions | join(",")`))
	identity := policy + ` | .spec.attestors[] | select(.name == "github") | .cosign.keyless.identities[0]`
	// Exactly master's run of the workflow: a regex would let a branch's run pass
	assert.Equal(t, labImageIssuer, renderKyverno(t, "", identity+` | .issuer`))
	assert.Equal(t, labImageWorkflow, renderKyverno(t, "", identity+` | .subject`))
	assert.Equal(t, "null", renderKyverno(t, "", identity+` | .subjectRegExp`))
	assert.Equal(t, "https://spdx.dev/Document", renderKyverno(t, "", policy+` | .spec.attestations[] | select(.name == "sbom") | .intoto.type`))
	validations := renderKyverno(t, "", policy+` | [.spec.validations[].expression] | join(" ")`)
	assert.Contains(t, validations, "verifyImageSignatures")
	assert.Contains(t, validations, "verifyAttestationSignatures")

	assert.Empty(t, renderKyverno(t, "dev", `select(.kind == "ImageValidatingPolicy") | .metadata.name`))
}

// cosign runs the cosign CLI with the given environment; secrets go on stdin, never in arguments
func cosign(t *testing.T, env []string, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("cosign", args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "cosign %s:\n%s", args[0], out)
	return string(out)
}

// In the sandbox: Kyverno's image verification passes a pod whose image is signed by the policy's key and fails one
// whose image is not (the production policy's keyless identity cannot sign here: TestLabImagePolicy checks it)
func TestSupplyChain(t *testing.T) {
	secretsSandboxOnly(t)
	dir := t.TempDir()
	password := generatedValue(t, "zot/generated/ci", "password")
	env := []string{"DOCKER_CONFIG=" + dir, "COSIGN_PASSWORD=" + fmt.Sprint(time.Now().UnixNano())}
	// The sandbox CA signs the registry's certificate: --allow-insecure-registry skips verifying it
	cosign(t, env, password, "login", registryHost, "-u", "ci", "--password-stdin")
	repo := registryHost + "/supply-chain-test"
	signed, unsigned := repo+":signed", repo+":unsigned"
	cosign(t, env, "", "copy", "--force", "--allow-insecure-registry", "docker.io/library/busybox:1.37.0", signed)
	cosign(t, env, "", "copy", "--force", "--allow-insecure-registry", "docker.io/library/busybox:1.36.1", unsigned)
	cosign(t, env, "", "generate-key-pair", "--output-key-prefix", filepath.Join(dir, "cosign"))
	cosign(t, env, "", "sign", "--yes", "--allow-insecure-registry", "--use-signing-config=false", "--key", filepath.Join(dir, "cosign.key"), signed)
	public, err := os.ReadFile(filepath.Join(dir, "cosign.pub"))
	require.NoError(t, err)

	namespace := fmt.Sprint("supply-chain-test-", time.Now().Unix())
	opts := k8s.NewKubectlOptions("", "", namespace)
	k8s.CreateNamespace(t, opts, namespace)
	defer k8s.DeleteNamespace(t, opts, namespace)

	// Kyverno reads zot with the ci account (a pull Secret in its own namespace)
	auth := base64.StdEncoding.EncodeToString([]byte("ci:" + password))
	pull := map[string]any{"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/dockerconfigjson",
		"metadata":   map[string]any{"name": namespace, "namespace": "kyverno"},
		"stringData": map[string]string{".dockerconfigjson": fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, registryHost, auth)}}
	policy := map[string]any{"apiVersion": "policies.kyverno.io/v1", "kind": "ImageValidatingPolicy",
		"metadata": map[string]any{"name": namespace},
		"spec": map[string]any{
			"validationActions": []string{"Audit"},
			"matchConstraints": map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": namespace}},
				"resourceRules": []map[string]any{{"apiGroups": []string{""}, "apiVersions": []string{"v1"},
					"operations": []string{"CREATE", "UPDATE"}, "resources": []string{"pods"}}}},
			"matchImageReferences":     []map[string]string{{"glob": repo + "*"}},
			"credentials":              map[string]any{"allowInsecureRegistry": true, "secrets": []string{namespace}},
			"validationConfigurations": map[string]bool{"mutateDigest": false, "verifyDigest": false, "required": true},
			"attestors": []map[string]any{{"name": "test", "cosign": map[string]any{
				"key":   map[string]string{"data": string(public)},
				"ctlog": map[string]bool{"insecureIgnoreTlog": true, "insecureIgnoreSCT": true}}}},
			"validations": []map[string]string{{
				"expression": "images.containers.map(image, verifyImageSignatures(image, [attestors.test])).all(e, e > 0)",
				"message":    "not signed by the test key"}},
		}}
	for _, doc := range []map[string]any{pull, policy} {
		manifest, err := json.Marshal(doc)
		require.NoError(t, err)
		k8s.KubectlApplyFromString(t, k8s.NewKubectlOptions("", "", "kyverno"), string(manifest))
	}
	defer k8s.RunKubectl(t, k8s.NewKubectlOptions("", "", "kyverno"), "delete", "secret", namespace, "--ignore-not-found")
	defer k8s.RunKubectl(t, opts, "delete", "imagevalidatingpolicies.policies.kyverno.io", namespace, "--ignore-not-found")
	require.Eventually(t, func() bool {
		ready, err := k8s.RunKubectlAndGetOutputE(t, opts, "get", "imagevalidatingpolicies.policies.kyverno.io", namespace,
			"-o", `jsonpath={.status.conditionStatus.ready}`)
		return err == nil && ready == "true"
	}, 2*time.Minute, 5*time.Second, "the test policy is not ready")

	// The pods need not run (the node has no pull credentials): Kyverno judges them at admission
	k8s.RunKubectl(t, opts, "run", "signed", "--image="+signed, "--restart=Never")
	k8s.RunKubectl(t, opts, "run", "unsigned", "--image="+unsigned, "--restart=Never")
	result := func(pod string) string {
		out, err := k8s.RunKubectlAndGetOutputE(t, opts, "get", "policyreports.wgpolicyk8s.io",
			"-o", `jsonpath={range .items[?(@.scope.name=="`+pod+`")]}{range .results[?(@.policy=="`+namespace+`")]}{.result}{end}{end}`)
		if err != nil {
			return ""
		}
		return out
	}
	require.Eventually(t, func() bool { return result("signed") != "" && result("unsigned") != "" }, 5*time.Minute, 10*time.Second,
		"no report of the test policy for both pods")
	assert.Equal(t, "pass", result("signed"))
	assert.Equal(t, "fail", result("unsigned"))
}
