package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// ciRepo creates repository ci/<name> on Forgejo (as its site admin) with files on its default branch, one commit
// each, and deletes it when the spec ends; it returns the last commit's sha
func ciRepo(ctx context.Context, name string, files map[string]string) string {
	GinkgoHelper()
	Expect(forgejoAs(ctx, "", "POST", "/orgs/ci/repos", map[string]any{"name": name, "auto_init": true, "private": true, "default_branch": "main"}, nil)).
		To(Equal(http.StatusCreated))
	DeferCleanup(func(ctx SpecContext) { forgejoAs(ctx, "", "DELETE", "/repos/ci/"+name, nil, nil) }, NodeTimeout(time.Minute))
	var sha string
	for path, content := range files {
		var out struct {
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		Expect(forgejoAs(ctx, "", "POST", "/repos/ci/"+name+"/contents/"+path, map[string]any{
			"content": base64.StdEncoding.EncodeToString([]byte(content)), "message": "add " + path, "branch": "main"}, &out)).
			To(Equal(http.StatusCreated))
		sha = out.Commit.SHA
	}
	return sha
}

// ciWorkflow waits for the Workflow the Sensor made for repository ci/<name> at sha to end, and returns its phase and
// the build's digest output ("" when nothing was pushed)
func ciWorkflow(ctx context.Context, name, sha string) (phase, digest string) {
	GinkgoHelper()
	Eventually(ctx, func() bool {
		out, err := kube.Kubectl(ctx, "ci", "get", "workflows.argoproj.io", "-o", "json")
		if err != nil {
			return false
		}
		var list struct {
			Items []struct {
				Spec struct {
					Arguments struct {
						Parameters []struct{ Name, Value string } `json:"parameters"`
					} `json:"arguments"`
				} `json:"spec"`
				Status struct {
					Phase string                    `json:"phase"`
					Nodes map[string]map[string]any `json:"nodes"`
				} `json:"status"`
			} `json:"items"`
		}
		if json.Unmarshal([]byte(out), &list) != nil {
			return false
		}
		for _, w := range list.Items {
			args := map[string]string{}
			for _, p := range w.Spec.Arguments.Parameters {
				args[p.Name] = p.Value
			}
			if args["repo"] != "ci/"+name || args["sha"] != sha {
				continue
			}
			switch w.Status.Phase {
			case "Succeeded", "Failed", "Error":
				phase = w.Status.Phase
				for _, node := range w.Status.Nodes {
					if node["templateName"] != "build" {
						continue
					}
					outputs, _ := node["outputs"].(map[string]any)
					params, _ := outputs["parameters"].([]any)
					for _, p := range params {
						if m, _ := p.(map[string]any); m["name"] == "digest" {
							digest, _ = m["value"].(string)
						}
					}
				}
				return true
			}
		}
		return false
	}).WithTimeout(25*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "no finished Workflow for ci/%s@%s", name, sha)
	return phase, digest
}

// ciStatus is Forgejo's combined commit status of ci/<name> at sha
func ciStatus(ctx context.Context, name, sha string) string {
	GinkgoHelper()
	var status struct {
		State string `json:"state"`
	}
	Expect(forgejoAs(ctx, "", "GET", "/repos/ci/"+name+"/commits/"+sha+"/status", nil, &status)).To(Equal(http.StatusOK))
	return status.State
}

// Step 10f: a push to a repository of org ci builds its image with Buildah in a user namespace, pushes it to zot, signs
// it and attests its provenance and SBOM with OpenBao's transit key, and Forgejo shows the result
var _ = Describe("CI", Label("sandbox", "ci"), func() {
	waitForAppBeforeEach("https://" + forgejoHost + "/api/healthz")

	It("builds, pushes, signs and attests a push to the default branch", func(ctx SpecContext) {
		name := fmt.Sprint("ci-test-", time.Now().Unix())
		sha := ciRepo(ctx, name, map[string]string{"Containerfile": "FROM docker.io/library/busybox:1.37.0\nRUN echo built > /built\n"})
		phase, digest := ciWorkflow(ctx, name, sha)
		Expect(phase).To(Equal("Succeeded"))
		Expect(digest).To(HavePrefix("sha256:"))
		Eventually(ctx, func() string { return ciStatus(ctx, name, sha) }).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(Equal("success"))

		// Verified with the public key Kyverno uses, without a transparency log (the signing skips it)
		dir := GinkgoT().TempDir()
		public, err := kube.Kubectl(ctx, "ci", "get", "configmap", "ci-signing-public-key", "-o", "jsonpath={.data.cosign\\.pub}")
		Expect(err).NotTo(HaveOccurred(), public)
		Expect(os.WriteFile(filepath.Join(dir, "cosign.pub"), []byte(public), 0o600)).To(Succeed())
		env := []string{"DOCKER_CONFIG=" + dir}
		cosign(ctx, env, generatedValue(ctx, "ci/generated/registry", "password"), "login", registryHost, "-u", "ci", "--password-stdin")
		image := registryHost + "/" + name + "@" + digest
		verify := []string{"--key", filepath.Join(dir, "cosign.pub"), "--insecure-ignore-tlog=true", "--allow-insecure-registry"}
		cosign(ctx, env, "", append([]string{"verify"}, append(verify, image)...)...)
		cosign(ctx, env, "", append([]string{"verify-attestation", "--type", "slsaprovenance1"}, append(verify, image)...)...)
		cosign(ctx, env, "", append([]string{"verify-attestation", "--type", "spdxjson"}, append(verify, image)...)...)

		// The run's volume goes with it
		claims, err := kube.Kubectl(ctx, "ci", "get", "pvc", "-o", "name")
		Expect(err).NotTo(HaveOccurred(), claims)
		Expect(strings.Count(claims, "persistentvolumeclaim/")).To(BeNumerically("<=", 2), "the runs' volumes pile up: %s", claims)
	}, SpecTimeout(35*time.Minute))

	It("refuses a .ci.yaml key it does not know, with a failure status", func(ctx SpecContext) {
		name := fmt.Sprint("ci-bad-", time.Now().Unix())
		sha := ciRepo(ctx, name, map[string]string{"Containerfile": "FROM docker.io/library/busybox:1.37.0\n", ".ci.yaml": "image: site\nprivileged: true\n"})
		phase, digest := ciWorkflow(ctx, name, sha)
		Expect(phase).To(Equal("Failed"))
		Expect(digest).To(BeEmpty())
		Eventually(ctx, func() string { return ciStatus(ctx, name, sha) }).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(Equal("failure"))
	}, SpecTimeout(30*time.Minute))

	It("refuses a webhook without org ci's secret", func(ctx SpecContext) {
		before, err := kube.Kubectl(ctx, "ci", "get", "workflows.argoproj.io", "-o", "name")
		Expect(err).NotTo(HaveOccurred(), before)
		// From Forgejo's own pod, the only caller the mesh lets in: a push event with no Authorization header
		body := `{"ref":"refs/heads/main","after":"1111111111111111111111111111111111111111","repository":{"full_name":"ci/none","default_branch":"main"}}`
		out, _ := kube.Kubectl(ctx, "forgejo", "exec", "deploy/forgejo", "-c", "forgejo", "--", "sh", "-c",
			"wget -q -S -O- --header 'X-Forgejo-Event: push' --header 'Content-Type: application/json' --post-data '"+body+
				"' http://forgejo-eventsource-svc.ci:12000/forgejo 2>&1 || true")
		Expect(out).NotTo(ContainSubstring("200 OK"), "the EventSource accepted a request without the secret")
		Consistently(ctx, func() string {
			now, _ := kube.Kubectl(ctx, "ci", "get", "workflows.argoproj.io", "-o", "name")
			return now
		}).WithTimeout(30*time.Second).WithPolling(5*time.Second).Should(Equal(before), "a Workflow started")
	}, SpecTimeout(5*time.Minute))
})

// Step 10f: the controller runs only Workflows that reference the golden path's templates
var _ = Describe("CIRestrictions", Label("sandbox", "ci"), func() {
	It("refuses a Workflow with an inline template", func(ctx SpecContext) {
		name := fmt.Sprint("inline-", time.Now().Unix())
		Expect(kube.Apply(ctx, "ci", fmt.Sprintf(`{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "metadata": {"name": %q},
			"spec": {"entrypoint": "main", "templates": [{"name": "main", "container": {"image": "docker.io/library/busybox:1.37.0", "command": ["true"]}}]}}`, name))).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { kube.Kubectl(ctx, "ci", "delete", "workflows.argoproj.io", name, "--wait=false") }, NodeTimeout(time.Minute))
		Eventually(ctx, func() string {
			out, _ := kube.Kubectl(ctx, "ci", "get", "workflows.argoproj.io", name, "-o", "jsonpath={.status.phase} {.status.message}")
			return out
		}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(And(MatchRegexp(`^(Error|Failed) `), ContainSubstring("workflowTemplateRef")))
		pods, err := kube.Kubectl(ctx, "ci", "get", "pods", "-l", "workflows.argoproj.io/workflow="+name, "-o", "name")
		Expect(err).NotTo(HaveOccurred(), pods)
		Expect(strings.TrimSpace(pods)).To(BeEmpty(), "the inline template ran")
	}, SpecTimeout(5*time.Minute))
})
