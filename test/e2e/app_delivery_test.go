package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The sandbox's test app on the app road (test/fixtures/apps/delivery-test.yaml)
const deliveryApp = "delivery-test"

var deliveryHost = deliveryApp + "." + suiteDomain

// deliveryContainerfile is the test app's image: busybox's httpd answering marker, or a loop answering 500 to everything
func deliveryContainerfile(marker string, broken bool) string {
	cmd := `CMD ["httpd", "-f", "-p", "8080", "-h", "/srv"]`
	if broken {
		cmd = `CMD ["sh", "-c", "while true; do printf 'HTTP/1.1 500 Internal Server Error\\r\\nContent-Length: 0\\r\\nConnection: close\\r\\n\\r\\n' | nc -l -p 8080; done"]`
	}
	return "FROM docker.io/library/busybox:1.37.0\n" +
		"RUN mkdir -p /srv && echo " + marker + " > /srv/index.html\n" +
		"USER 65534\nEXPOSE 8080\n" + cmd + "\n"
}

// deliveryCommit replaces the Containerfile of ci/delivery-test on main and returns the commit
func deliveryCommit(ctx context.Context, content string) string {
	GinkgoHelper()
	var current struct {
		SHA string `json:"sha"`
	}
	Expect(forgejoAs(ctx, "", "GET", "/repos/ci/"+deliveryApp+"/contents/Containerfile?ref=main", nil, &current)).To(Equal(http.StatusOK))
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	Expect(forgejoAs(ctx, "", "PUT", "/repos/ci/"+deliveryApp+"/contents/Containerfile", map[string]any{
		"content": base64.StdEncoding.EncodeToString([]byte(content)), "sha": current.SHA, "branch": "main", "message": "AppDelivery",
	}, &out)).To(Equal(http.StatusOK))
	return out.Commit.SHA
}

// deliveryPage is what the app answers through the LAN Gateway: its status and body
func deliveryPage(ctx context.Context) (int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+deliveryHost+"/", nil)
	if err != nil {
		return 0, ""
	}
	resp, err := insecureClient.Do(req)
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

// deliveryRelease is ops/releases' delivery-test.yaml: the version Kargo promoted
func deliveryRelease(ctx context.Context) string {
	var file struct {
		Content string `json:"content"`
	}
	if forgejoAs(ctx, "", "GET", "/repos/ops/releases/contents/"+deliveryApp+".yaml?ref=main", nil, &file) != http.StatusOK {
		return ""
	}
	raw, _ := base64.StdEncoding.DecodeString(file.Content)
	return string(raw)
}

// rolloutState is the Rollout's phase and whether it was aborted
func rolloutState(ctx context.Context) string {
	out, _ := kube.Kubectl(ctx, deliveryApp, "get", "rollouts.argoproj.io", deliveryApp, "-o", "jsonpath={.status.phase} abort={.status.abort}")
	return strings.TrimSpace(out)
}

// The app road end to end (docs/superpowers/specs/2026-11-18-app-delivery-design.md): a repository from Backstage's
// template, built and signed by the CI, verified and promoted by Kargo into ops/releases, released by Argo Rollouts'
// canary; an unsigned image never reaches production, a version that fails is stopped with users back on the last
// good one, and promoting the previous Freight undoes it
var _ = Describe("AppDelivery", Label("sandbox", "delivery"), Ordered, ContinueOnFailure, func() {
	// Optional in the sandbox (just dev deploys it with the specs that need it): skip before waiting for anything
	BeforeEach(func(ctx SpecContext) { skipUnlessDeployed(ctx, deliveryApp) })
	waitForIdentity()
	backstageReady()
	var good, goodMarker string

	It("creates its repository and builds the first version", func(ctx SpecContext) {
		backstageReadsForgejo(ctx)
		// Left by an earlier run: the template refuses a repository that exists, and a location already registered
		forgejoAs(ctx, "", "DELETE", "/repos/ci/"+deliveryApp, nil, nil)
		admin, _, _ := backstageIdentity(ctx, "dev-admin")
		backstageUnregister(ctx, admin, deliveryApp)
		backstageCreateRepository(ctx, deliveryApp, "AppDelivery's app")
		DeferCleanup(func(ctx SpecContext) { forgejoAs(ctx, "", "DELETE", "/repos/ci/"+deliveryApp, nil, nil) }, NodeTimeout(time.Minute))
		goodMarker = fmt.Sprint("ok-", time.Now().Unix())
		good = deliveryCommit(ctx, deliveryContainerfile(goodMarker, false))
		// The image is what Kargo needs: the CI's last step (Forgejo's commit status) is the CI specs' to prove
		_, digest := ciWorkflowFor(ctx, deliveryApp, good, "push")
		Expect(digest).NotTo(BeEmpty(), "the CI pushed no image")
	}, SpecTimeout(45*time.Minute))

	It("verifies and promotes it to production by itself", func(ctx SpecContext) {
		Eventually(ctx, deliveryRelease).WithTimeout(20*time.Minute).WithPolling(20*time.Second).Should(ContainSubstring(good),
			"Kargo did not write the CI's tag to ops/releases")
		Eventually(ctx, func() string {
			_, body := deliveryPage(ctx)
			return body
		}).WithTimeout(15 * time.Minute).WithPolling(15 * time.Second).Should(Equal(goodMarker))
		Eventually(ctx, rolloutState).WithTimeout(10 * time.Minute).WithPolling(15 * time.Second).Should(HavePrefix("Healthy"))
	}, SpecTimeout(40*time.Minute))

	It("never promotes an image the CI did not sign", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		password := generatedValue(ctx, "zot/generated/ci", "password")
		env := []string{"DOCKER_CONFIG=" + dir}
		cosign(ctx, env, password, "login", registryHost, "-u", "ci", "--password-stdin")
		unsigned := registryHost + "/" + deliveryApp + ":unsigned"
		cosign(ctx, env, "", "copy", "--force", "--allow-insecure-registry", "--platform", "linux/amd64", "docker.io/library/busybox:1.37.0", unsigned)
		// Freight for it, as a Warehouse that found it newest would make: verify takes the newest Freight by itself
		digest := strings.TrimSpace(cosign(ctx, env, "", "triangulate", "--allow-insecure-registry", "--type", "digest", unsigned))
		digest = digest[strings.LastIndex(digest, "@")+1:]
		freight := fmt.Sprintf(`{"apiVersion":"kargo.akuity.io/v1alpha1","kind":"Freight","metadata":{"generateName":"unsigned-","namespace":%q},`+
			`"origin":{"kind":"Warehouse","name":%q},"images":[{"repoURL":%q,"tag":"unsigned","digest":%q}]}`,
			deliveryApp, deliveryApp, registryHost+"/"+deliveryApp, digest)
		// Kargo names Freight by its content: one left by an earlier run is this one
		out, err := kube.KubectlStdin(ctx, freight, deliveryApp, "create", "-f", "-", "-o", "name")
		if err != nil && strings.Contains(out, "AlreadyExists") {
			out = out[strings.Index(out, `"`)+1:]
			out = "freight.kargo.akuity.io/" + out[:strings.Index(out, `"`)]
		} else {
			Expect(err).NotTo(HaveOccurred(), out)
		}
		name := strings.TrimPrefix(strings.TrimSpace(out), "freight.kargo.akuity.io/")
		DeferCleanup(func(ctx SpecContext) {
			_, _ = kube.Kubectl(ctx, deliveryApp, "delete", "freight", name, "--ignore-not-found")
		}, NodeTimeout(time.Minute))

		// verify's check of it fails, and production keeps the good version
		Eventually(ctx, func() string {
			out, _ := kube.Kubectl(ctx, deliveryApp, "get", "analysisruns.argoproj.io", "-o",
				`jsonpath={range .items[?(@.spec.args[0].value=="`+registryHost+"/"+deliveryApp+"@"+digest+`")]}{.status.phase}{"\n"}{end}`)
			return out
		}).WithTimeout(10*time.Minute).WithPolling(15*time.Second).Should(ContainSubstring("Failed"), "no failed signature check of the unsigned image")
		Consistently(ctx, deliveryRelease).WithTimeout(3 * time.Minute).WithPolling(20 * time.Second).Should(ContainSubstring(good))
	}, SpecTimeout(20*time.Minute))

	It("aborts a version that serves errors and keeps users on the last good one", func(ctx SpecContext) {
		broken := deliveryCommit(ctx, deliveryContainerfile("broken", true))
		_, digest := ciWorkflowFor(ctx, deliveryApp, broken, "push")
		Expect(digest).NotTo(BeEmpty(), "the CI pushed no image")
		Eventually(ctx, deliveryRelease).WithTimeout(20 * time.Minute).WithPolling(20 * time.Second).Should(ContainSubstring(broken))

		// Traffic through the Gateway while the canary runs: its 5xx are what the analysis reads
		traffic, stop := context.WithCancel(ctx)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for traffic.Err() == nil {
				deliveryPage(traffic)
				time.Sleep(100 * time.Millisecond)
			}
		}()
		DeferCleanup(func() { stop(); wg.Wait() })

		Eventually(ctx, rolloutState).WithTimeout(15*time.Minute).WithPolling(10*time.Second).Should(Equal("Degraded abort=true"),
			"the analysis did not abort the canary")
		Eventually(ctx, func() string {
			_, body := deliveryPage(ctx)
			return body
		}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(Equal(goodMarker), "users are not back on the last good version")
		Eventually(ctx, func() bool {
			for _, a := range activeAlerts(ctx) {
				if a["alertname"] == "RolloutAborted" && a["namespace"] == deliveryApp {
					return true
				}
			}
			return false
		}).WithTimeout(5*time.Minute).WithPolling(20*time.Second).Should(BeTrue(), "RolloutAborted did not fire")
	}, SpecTimeout(50*time.Minute))

	It("is undone by promoting the previous Freight", func(ctx SpecContext) {
		out, err := kube.Kubectl(ctx, deliveryApp, "get", "freight", "-o",
			`jsonpath={range .items[?(@.images[0].tag=="`+good+`")]}{.metadata.name}{end}`)
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(strings.TrimSpace(out)).NotTo(BeEmpty(), "no Freight of the good version")
		// What promoting it in Kargo's UI creates (SSOKargo proves an admin may, through the API)
		promotion := fmt.Sprintf(`{"apiVersion":"kargo.akuity.io/v1alpha1","kind":"Promotion","metadata":{"generateName":"rollback-","namespace":%q},`+
			`"spec":{"stage":"production","freight":%q}}`, deliveryApp, strings.TrimSpace(out))
		created, err := kube.KubectlStdin(ctx, promotion, deliveryApp, "create", "-f", "-")
		Expect(err).NotTo(HaveOccurred(), created)
		Eventually(ctx, deliveryRelease).WithTimeout(10 * time.Minute).WithPolling(15 * time.Second).Should(ContainSubstring(good))
		Eventually(ctx, rolloutState).WithTimeout(10 * time.Minute).WithPolling(15 * time.Second).Should(HavePrefix("Healthy"))
		_, body := deliveryPage(ctx)
		Expect(body).To(Equal(goodMarker))
	}, SpecTimeout(30*time.Minute))
})
