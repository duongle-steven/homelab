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

	"github.com/duongle-steven/homelab/test/internal/testenv"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	testenv.SecretsSandboxOnly(t)
	dir := t.TempDir()
	password := generatedValue(t, "zot/generated/ci", "password")
	env := []string{"DOCKER_CONFIG=" + dir, "COSIGN_PASSWORD=" + fmt.Sprint(time.Now().UnixNano())}
	// The sandbox CA signs the registry's certificate: --allow-insecure-registry skips verifying it
	cosign(t, env, password, "login", registryHost, "-u", "ci", "--password-stdin")
	repo := registryHost + "/supply-chain-test"
	signed, unsigned := repo+":signed", repo+":unsigned"
	// One platform: the whole index costs a Docker Hub pull per manifest against its anonymous limit; OCI images, the
	// only kind zot takes by default (ghcr.io's containerd/busybox is Docker's format)
	cosign(t, env, "", "copy", "--force", "--allow-insecure-registry", "--platform", "linux/amd64", "docker.io/library/busybox:1.37.0", signed)
	cosign(t, env, "", "copy", "--force", "--allow-insecure-registry", "--platform", "linux/amd64", "docker.io/library/busybox:1.36.1", unsigned)
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
			"-o", `jsonpath={range .items[?(@.scope.name=="`+pod+`")]}{range .results[?(@.policy=="`+namespace+`")]}{.result} {.message}{end}{end}`)
		if err != nil {
			return ""
		}
		return out
	}
	// Kyverno keeps only fail, error and warn (the chart's reporting.allowedResults): the unsigned pod's fail shows the
	// policy ran on both pods, created together, and the signed pod passes by having none. The result comes first,
	// then Kyverno's message
	require.Eventually(t, func() bool { return result("unsigned") != "" }, 5*time.Minute, 10*time.Second,
		"no report of the test policy for the unsigned pod")
	unsignedResult := result("unsigned")
	assert.True(t, strings.HasPrefix(unsignedResult, "fail"), unsignedResult)
	assert.Never(t, func() bool { return result("signed") != "" }, 30*time.Second, 5*time.Second,
		"the signed pod has a result: %s", result("signed"))
}
