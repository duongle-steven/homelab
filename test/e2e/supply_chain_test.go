package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// cosign runs the cosign CLI with the given environment; secrets go on stdin, never in arguments
func cosign(ctx context.Context, env []string, stdin string, args ...string) string {
	GinkgoHelper()
	cmd := exec.CommandContext(ctx, "cosign", args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "cosign %s:\n%s", args[0], out)
	return string(out)
}

// In the sandbox: Kyverno's image verification passes a pod whose image is signed by the policy's key and fails one
// whose image is not (the production policy's keyless identity cannot sign here: TestLabImagePolicy checks it)
var _ = Describe("SupplyChain", Label("sandbox", "testbed", "supply-chain"), func() {
	It("passes a signed image and fails an unsigned one", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		password := generatedValue(ctx, "zot/generated/ci", "password")
		env := []string{"DOCKER_CONFIG=" + dir, "COSIGN_PASSWORD=" + fmt.Sprint(time.Now().UnixNano())}
		// The sandbox CA signs the registry's certificate: --allow-insecure-registry skips verifying it
		cosign(ctx, env, password, "login", registryHost, "-u", "ci", "--password-stdin")
		repo := registryHost + "/supply-chain-test"
		signed, unsigned := repo+":signed", repo+":unsigned"
		// One platform: the whole index costs a Docker Hub pull per manifest against its anonymous limit; OCI images, the
		// only kind zot takes by default (ghcr.io's containerd/busybox is Docker's format)
		cosign(ctx, env, "", "copy", "--force", "--allow-insecure-registry", "--platform", "linux/amd64", "docker.io/library/busybox:1.37.0", signed)
		cosign(ctx, env, "", "copy", "--force", "--allow-insecure-registry", "--platform", "linux/amd64", "docker.io/library/busybox:1.36.1", unsigned)
		cosign(ctx, env, "", "generate-key-pair", "--output-key-prefix", filepath.Join(dir, "cosign"))
		cosign(ctx, env, "", "sign", "--yes", "--allow-insecure-registry", "--use-signing-config=false", "--key", filepath.Join(dir, "cosign.key"), signed)
		public, err := os.ReadFile(filepath.Join(dir, "cosign.pub"))
		Expect(err).NotTo(HaveOccurred())

		namespace := fmt.Sprint("supply-chain-test-", time.Now().Unix())
		Expect(kube.CreateNamespace(ctx, namespace)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { Expect(kube.DeleteNamespace(ctx, namespace)).To(Succeed()) }, NodeTimeout(5*time.Minute))

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
			Expect(err).NotTo(HaveOccurred())
			Expect(kube.Apply(ctx, "kyverno", string(manifest))).To(Succeed())
		}
		// DeferCleanup runs last in first out, as the defers did
		DeferCleanup(func(ctx SpecContext) {
			_, err := kube.Kubectl(ctx, "kyverno", "delete", "secret", namespace, "--ignore-not-found")
			Expect(err).NotTo(HaveOccurred())
		}, NodeTimeout(time.Minute))
		DeferCleanup(func(ctx SpecContext) {
			_, err := kube.Kubectl(ctx, namespace, "delete", "imagevalidatingpolicies.policies.kyverno.io", namespace, "--ignore-not-found")
			Expect(err).NotTo(HaveOccurred())
		}, NodeTimeout(time.Minute))
		Eventually(ctx, func() bool {
			ready, err := kube.Kubectl(ctx, namespace, "get", "imagevalidatingpolicies.policies.kyverno.io", namespace,
				"-o", `jsonpath={.status.conditionStatus.ready}`)
			return err == nil && ready == "true"
		}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "the test policy is not ready")

		// The pods need not run (the node has no pull credentials): Kyverno judges them at admission
		for _, pod := range [][2]string{{"signed", signed}, {"unsigned", unsigned}} {
			out, err := kube.Kubectl(ctx, namespace, "run", pod[0], "--image="+pod[1], "--restart=Never")
			Expect(err).NotTo(HaveOccurred(), out)
		}
		result := func(pod string) string {
			out, err := kube.Kubectl(ctx, namespace, "get", "policyreports.wgpolicyk8s.io",
				"-o", `jsonpath={range .items[?(@.scope.name=="`+pod+`")]}{range .results[?(@.policy=="`+namespace+`")]}{.result} {.message}{end}{end}`)
			if err != nil {
				return ""
			}
			return out
		}
		// Kyverno keeps only fail, error and warn (the chart's reporting.allowedResults): the unsigned pod's fail shows the
		// policy ran on both pods, created together, and the signed pod passes by having none. The result comes first,
		// then Kyverno's message
		Eventually(ctx, func() bool { return result("unsigned") != "" }).WithTimeout(5*time.Minute).WithPolling(10*time.Second).
			Should(BeTrue(), "no report of the test policy for the unsigned pod")
		unsignedResult := result("unsigned")
		Expect(strings.HasPrefix(unsignedResult, "fail")).To(BeTrue(), unsignedResult)
		Consistently(ctx, func() string { return result("signed") }).WithTimeout(30*time.Second).WithPolling(5*time.Second).
			Should(BeEmpty(), "the signed pod has a result")
	}, SpecTimeout(40*time.Minute))
})
