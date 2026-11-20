package offline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// cert-manager's webhook validates its own Issuers and Certificates: they sync a wave after it (with every CRD already
// there, a first sync would otherwise apply them before the webhook has an endpoint and, failing, leave trust-manager
// waiting forever for its certificate); trust-manager's Bundle a wave after trust-manager, whose webhook validates it
func TestCertManagerWaves(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		waves := renderVelero(t, "system/cert-manager", env, `select(.kind != "CustomResourceDefinition") | .kind + "/" + .metadata.name + "=" + (.metadata.annotations["argocd.argoproj.io/sync-wave"] // "0")`)
		wave := map[string]string{}
		for _, w := range strings.Fields(waves) {
			k, v, _ := strings.Cut(w, "=")
			wave[k] = v
		}
		assert.Equal(t, "0", wave["Deployment/cert-manager-webhook"], env)
		assert.Equal(t, "1", wave["ClusterIssuer/letsencrypt-prod"], env)
		if env == "dev" {
			for _, k := range []string{"ClusterIssuer/selfsigned", "Certificate/homelab-dev-ca", "Issuer/trust-manager", "Certificate/trust-manager", "Deployment/trust-manager"} {
				assert.Equal(t, "1", wave[k], k)
			}
			assert.Equal(t, "2", wave["Bundle/homelab-dev-ca"])
		}
	}
}
