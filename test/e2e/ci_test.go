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
		sha = ciCommit(ctx, name, "main", "", path, content)
	}
	return sha
}

// ciCommit adds a file to ci/<name> on branch (creating newBranch from it when newBranch is set) and returns the commit
func ciCommit(ctx context.Context, name, branch, newBranch, path, content string) string {
	GinkgoHelper()
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	body := map[string]any{"content": base64.StdEncoding.EncodeToString([]byte(content)), "message": "add " + path, "branch": branch}
	if newBranch != "" {
		body["new_branch"] = newBranch
	}
	Expect(forgejoAs(ctx, "", "POST", "/repos/ci/"+name+"/contents/"+path, body, &out)).To(Equal(http.StatusCreated))
	return out.Commit.SHA
}

// ciWorkflow waits for the Workflow the Sensor made for repository ci/<name> at sha to end, and returns its phase and
// the build's digest output ("" when nothing was pushed)
func ciWorkflow(ctx context.Context, name, sha string) (phase, digest string) {
	GinkgoHelper()
	return ciWorkflowFor(ctx, name, sha, "")
}

// ciWorkflowFor is ciWorkflow for one event (push, pull_request), or any when event is ""
func ciWorkflowFor(ctx context.Context, name, sha, event string) (phase, digest string) {
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
			if args["repo"] != "ci/"+name || args["sha"] != sha || (event != "" && args["event"] != event) {
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
	}).WithTimeout(25*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "no finished Workflow for ci/%s@%s (%s)", name, sha, event)
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
	// org ci's bot, its token and the webhook come from lab infra forgejo sync, which production's deploy runs and the
	// sandbox only in specs: run it (it changes nothing when all is in place), then wait for the token in namespace ci
	BeforeEach(func(ctx SpecContext) {
		lab(ctx, "infra", "forgejo", "sync", "--env", labEnv)
		_, err := kube.Kubectl(ctx, "ci", "annotate", "externalsecret", "forgejo", fmt.Sprintf("force-sync=%d", time.Now().UnixNano()), "--overwrite")
		Expect(err).NotTo(HaveOccurred())
		Eventually(ctx, func() string {
			out, _ := kube.Kubectl(ctx, "ci", "get", "externalsecret", "forgejo", "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
			return out
		}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(Equal("True"), "Secret ci/forgejo has no token")
	}, NodeTimeout(10*time.Minute))

	It("builds, pushes, signs and attests a push to the default branch", func(ctx SpecContext) {
		name := fmt.Sprint("ci-test-", time.Now().Unix())
		sha := ciRepo(ctx, name, map[string]string{"Containerfile": "FROM docker.io/library/busybox:1.37.0\nRUN echo built > /built\n"})
		phase, digest := ciWorkflow(ctx, name, sha)
		Expect(phase).To(Equal("Succeeded"))
		Expect(digest).To(HavePrefix("sha256:"))
		Eventually(ctx, func() string { return ciStatus(ctx, name, sha) }).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Equal("success"))

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
		Eventually(ctx, func() string { return ciStatus(ctx, name, sha) }).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Equal("failure"))
	}, SpecTimeout(30*time.Minute))

	It("builds a pull request without pushing, with a status on its head", func(ctx SpecContext) {
		name := fmt.Sprint("ci-pr-", time.Now().Unix())
		ciRepo(ctx, name, map[string]string{"Containerfile": "FROM docker.io/library/busybox:1.37.0\n"})
		sha := ciCommit(ctx, name, "main", "feature", "Containerfile", "FROM docker.io/library/busybox:1.37.0\nRUN echo pr > /pr\n")
		Expect(forgejoAs(ctx, "", "POST", "/repos/ci/"+name+"/pulls", map[string]any{"head": "feature", "base": "main", "title": "a change"}, nil)).
			To(Equal(http.StatusCreated))
		phase, digest := ciWorkflowFor(ctx, name, sha, "pull_request")
		Expect(phase).To(Equal("Succeeded"))
		Expect(digest).To(BeEmpty(), "a pull request pushed its image")
		Eventually(ctx, func() string { return ciStatus(ctx, name, sha) }).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Equal("success"))
	}, SpecTimeout(35*time.Minute))

	It("runs two pushes in a row, each with its own status", func(ctx SpecContext) {
		name := fmt.Sprint("ci-twice-", time.Now().Unix())
		first := ciRepo(ctx, name, map[string]string{"Containerfile": "FROM docker.io/library/busybox:1.37.0\n"})
		second := ciCommit(ctx, name, "main", "", "README.md", "two\n")
		for _, sha := range []string{first, second} {
			phase, digest := ciWorkflowFor(ctx, name, sha, "push")
			Expect(phase).To(Equal("Succeeded"), sha)
			Expect(digest).To(HavePrefix("sha256:"), sha)
			Eventually(ctx, func() string { return ciStatus(ctx, name, sha) }).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(Equal("success"), sha)
		}
	}, SpecTimeout(45*time.Minute))

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
	// templates/workflow-policy.yaml: what an editor may submit through Argo Server, which acts as its ServiceAccount
	editor := "system:serviceaccount:argo-workflows:ci-editor"
	submit := func(ctx context.Context, entrypoint, event string) (string, error) {
		spec := map[string]any{"workflowTemplateRef": map[string]any{"name": "pipeline"}, "arguments": map[string]any{"parameters": []map[string]string{
			{"name": "repo", "value": "ci/none"}, {"name": "sha", "value": "1111111111111111111111111111111111111111"},
			{"name": "ref", "value": "refs/heads/main"}, {"name": "event", "value": event}, {"name": "default-branch", "value": "main"}}}}
		if entrypoint != "" {
			spec["entrypoint"] = entrypoint
		}
		doc, _ := json.Marshal(map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "metadata": map[string]any{"generateName": "manual-"}, "spec": spec})
		file := filepath.Join(GinkgoT().TempDir(), "workflow.json")
		Expect(os.WriteFile(file, doc, 0o600)).To(Succeed())
		return kube.Kubectl(ctx, "ci", "create", "--dry-run=server", "--as="+editor, "-f", file)
	}

	It("lets only the Sensor start a run that publishes", func(ctx SpecContext) {
		out, err := submit(ctx, "", "push")
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("only the Sensor starts a run with event=push"))
		out, err = submit(ctx, "sign", "pull_request")
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("the golden path's entrypoint"))
		out, err = submit(ctx, "", "pull_request")
		Expect(err).NotTo(HaveOccurred(), "a manual run that does not publish: %s", out)
	}, SpecTimeout(2*time.Minute))

	It("refuses a Workflow with an inline template", func(ctx SpecContext) {
		name := fmt.Sprint("inline-", time.Now().Unix())
		Expect(kube.Apply(ctx, "ci", fmt.Sprintf(`{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "metadata": {"name": %q},
			"spec": {"entrypoint": "main", "templates": [{"name": "main", "container": {"image": "docker.io/library/busybox:1.37.0", "command": ["true"]}}]}}`, name))).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			kube.Kubectl(ctx, "ci", "delete", "workflows.argoproj.io", name, "--wait=false")
		}, NodeTimeout(time.Minute))
		Eventually(ctx, func() string {
			out, _ := kube.Kubectl(ctx, "ci", "get", "workflows.argoproj.io", name, "-o", "jsonpath={.status.phase} {.status.message}")
			return out
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(And(MatchRegexp(`^(Error|Failed) `), ContainSubstring("workflowTemplateRef")))
		pods, err := kube.Kubectl(ctx, "ci", "get", "pods", "-l", "workflows.argoproj.io/workflow="+name, "-o", "name")
		Expect(err).NotTo(HaveOccurred(), pods)
		Expect(strings.TrimSpace(pods)).To(BeEmpty(), "the inline template ran")

		// Nobody changes a run's arguments afterwards (editors may patch Workflows to stop or retry them)
		out, err := kube.Kubectl(ctx, "ci", "patch", "workflows.argoproj.io", name, "--dry-run=server", "--as="+editor, "--type=merge",
			"-p", `{"spec": {"arguments": {"parameters": [{"name": "event", "value": "push"}]}}}`)
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("do not change after it is created"))
	}, SpecTimeout(5*time.Minute))
})
