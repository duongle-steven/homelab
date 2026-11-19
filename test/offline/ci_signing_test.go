package offline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The golden path signs with OpenBao's transit key ci-signing, which never leaves OpenBao (step 10f): the pipeline's
// role ci-signer may only sign with it and read it; a publisher may only read its public key, which it copies to a
// ConfigMap Kyverno reads to audit every image of registry.<domain>
func TestCISigningRender(t *testing.T) {
	config := renderVelero(t, "platform/openbao", "", `select(.kind == "ConfigMap" and .metadata.name == "openbao-config") | .data."extraconfig-from-values.hcl"`)
	for _, want := range []string{
		`path      = "sys/mounts/transit"`,
		`path      = "transit/keys/ci-signing"`,
		`type = "ecdsa-p256"`,
		`path      = "sys/policies/acl/ci-signer"`,
		`policy = "path \"transit/sign/ci-signing\" { capabilities = [\"update\"] }\npath \"transit/sign/ci-signing/*\" { capabilities = [\"update\"] }\npath \"transit/keys/ci-signing\" { capabilities = [\"read\"] }"`,
		`path      = "sys/policies/acl/ci-signing-publisher"`,
		`policy = "path \"transit/export/public-key/ci-signing/latest\" { capabilities = [\"read\"] }"`,
		`bound_service_account_names      = ["ci-signer"]`,
		`bound_service_account_names      = ["ci-signing-public-key"]`,
		`bound_service_account_namespaces = ["ci"]`,
	} {
		assert.Contains(t, config, want)
	}
	assert.Equal(t, 2, strings.Count(config, `bound_service_account_namespaces = ["ci"]`))
	callers := renderVelero(t, "platform/openbao", "", `select(.kind == "AuthorizationPolicy") | .spec.rules[].from[].source.principals[]?`)
	assert.Contains(t, callers, "cluster.local/ns/ci/sa/ci-signer")
	assert.Contains(t, callers, "cluster.local/ns/ci/sa/ci-signing-public-key")

	for env, domain := range map[string]string{"": "hoangduong.dev", "dev": "127-0-0-1.nip.io"} {
		job := renderVelero(t, "platform/ci", env, `select(.kind == "Job" and .metadata.name == "ci-signing-public-key") | .metadata.annotations."argocd.argoproj.io/hook" + " " + .spec.template.spec.serviceAccountName`)
		assert.Equal(t, "PostSync ci-signing-public-key", job, env)
		assert.Equal(t, `[{"apiGroups":[""],"resourceNames":["ci-signing-public-key"],"resources":["configmaps"],"verbs":["patch"]},{"apiGroups":[""],"resources":["configmaps"],"verbs":["create"]}]`,
			renderVelero(t, "platform/ci", env, `select(.kind == "Role" and .metadata.name == "ci-signing-public-key") | .rules | sort_keys(..) | to_json(0)`), env)

		policy := func(expr string) string {
			return renderVelero(t, "system/kyverno", env, `select(.kind == "ImageValidatingPolicy" and .metadata.name == "verify-ci-images") | `+expr)
		}
		assert.Equal(t, "Audit", policy(`.spec.validationActions[0]`), env)
		assert.Equal(t, "registry."+domain+"/*", policy(`.spec.matchImageReferences[0].glob`), env)
		assert.Contains(t, policy(`.spec.attestors[0].cosign.key.expression`), `"ci", "ci-signing-public-key"`, env)
		assert.Equal(t, "true true", policy(`.spec.attestors[0].cosign.ctlog | (.insecureIgnoreTlog | tostring) + " " + (.insecureIgnoreSCT | tostring)`), env)
		assert.Equal(t, "https://slsa.dev/provenance/v1 https://spdx.dev/Document", policy(`[.spec.attestations[].intoto.type] | join(" ")`), env)
	}
}

// Kargo admits an image to our apps' production only once a stage verify ran this template on it
// (docs/superpowers/specs/2026-11-18-app-delivery-design.md): a Job in the app's namespace fetches ci-signing's
// public key from ConfigMap ci/ci-signing-public-key (only the namespaces in publicKeyReaders may read it) and runs
// cosign verify, with no transparency log, against zot with the namespace's zot-pull
func TestCISignatureAnalysis(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		tmpl := `select(.kind == "ClusterAnalysisTemplate" and .metadata.name == "ci-signature")`
		assert.Equal(t, "image", renderVelero(t, "platform/ci", env, tmpl+` | [.spec.args[].name] | join(",")`), env)
		pod := tmpl + ` | .spec.metrics[0].provider.job.spec.template.spec`
		assert.Equal(t, "ci-signature", renderVelero(t, "platform/ci", env, pod+` | .serviceAccountName`), env)
		script := renderVelero(t, "platform/ci", env, pod+` | [.initContainers[].args[], .containers[].args[]] | join("\n")`)
		for _, want := range []string{"--cacert", "/api/v1/namespaces/ci/configmaps/ci-signing-public-key", "cosign verify", "--insecure-ignore-tlog=true", "{{args.image}}"} {
			assert.Contains(t, script, want, env)
		}
		assert.NotContains(t, script, "--allow-insecure-registry", env)
		assert.Equal(t, "zot-pull", renderVelero(t, "platform/ci", env, pod+` | .volumes[] | select(.name == "docker-config") | .secret.secretName`), env)

		readers := renderVelero(t, "platform/ci", env, `select(.kind == "RoleBinding" and .metadata.name == "ci-signing-public-key-readers") | [.subjects[] | .namespace + "/" + .name] | join(",")`)
		assert.Contains(t, readers, "delivery-test/ci-signature", env)
		assert.Equal(t, `[{"apiGroups":[""],"resourceNames":["ci-signing-public-key"],"resources":["configmaps"],"verbs":["get"]}]`,
			renderVelero(t, "platform/ci", env, `select(.kind == "Role" and .metadata.name == "ci-signing-public-key-readers") | .rules | sort_keys(..) | to_json(0)`), env)
	}
}
