package e2e

import (
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The chart repository of the apps ApplicationSet's app-template: every other source is the homelab repository
const appTemplateRepo = "https://bjw-s-labs.github.io/helm-charts"

// ArgoCDClusterSecret: the bootstrap's cluster Secret (infra/_modules/argocd/cluster-secret.sh) carries what the
// ApplicationSets read, and every Application syncs the repository and the revision it names (step 14g)
var _ = Describe("ArgoCDClusterSecret", Label("sandbox", "testbed", "argocd"), func() {
	var annotations map[string]string

	BeforeEach(func(ctx SpecContext) {
		var secret struct {
			Metadata struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		Expect(kube.GetJSON(ctx, "argocd", &secret, "secret", "in-cluster")).To(Succeed())
		Expect(secret.Metadata.Labels).To(HaveKeyWithValue("argocd.argoproj.io/secret-type", "cluster"))
		Expect(secret.Metadata.Labels).To(HaveKeyWithValue("homelab/bootstrap", "true"))
		annotations = secret.Metadata.Annotations
	})

	It("carries what the ApplicationSets read", func(ctx SpecContext) {
		Expect(annotations).To(HaveKey("repository"))
		Expect(annotations).To(HaveKey("revision"))
		Expect(annotations).To(HaveKey("backupS3Url"))
		Expect(annotations["repository"]).NotTo(BeEmpty())
		Expect(annotations["revision"]).NotTo(BeEmpty())
	}, SpecTimeout(5*time.Minute))

	It("is what every Application syncs", func(ctx SpecContext) {
		type source struct {
			RepoURL        string `json:"repoURL"`
			TargetRevision string `json:"targetRevision"`
		}
		var apps struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					Source  *source  `json:"source"`
					Sources []source `json:"sources"`
				} `json:"spec"`
			} `json:"items"`
		}
		Expect(kube.GetJSON(ctx, "argocd", &apps, "applications")).To(Succeed())
		Expect(apps.Items).NotTo(BeEmpty())
		var wrong []string
		for _, app := range apps.Items {
			sources := app.Spec.Sources
			if app.Spec.Source != nil {
				sources = append(sources, *app.Spec.Source)
			}
			for _, s := range sources {
				if s.RepoURL == appTemplateRepo {
					continue
				}
				if s.RepoURL != annotations["repository"] || s.TargetRevision != annotations["revision"] {
					wrong = append(wrong, app.Metadata.Name+": "+s.RepoURL+"@"+s.TargetRevision)
				}
			}
		}
		Expect(wrong).To(BeEmpty(), "sources not at %s@%s", annotations["repository"], annotations["revision"])
	}, SpecTimeout(5*time.Minute))
})
