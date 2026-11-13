package offline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The golden path's template (step 10f): one volume per run for its steps, kept 7 days, its pods gone on success; the
// build is the labelled pod in its own user namespace; publishing (push, SBOM, signature) only when the settings step
// says so (a push to the default branch, TestCISettings); every result reported to Forgejo; every image pinned
func TestCIPipelineRender(t *testing.T) {
	for env, domain := range map[string]string{"": "hoangduong.dev", "dev": "127-0-0-1.nip.io"} {
		render := func(expr string) string {
			return renderVelero(t, "platform/ci", env, `select(.kind == "WorkflowTemplate" and .metadata.name == "pipeline") | `+expr)
		}
		assert.Equal(t, "main report ci-pipeline", render(`.spec.entrypoint + " " + .spec.onExit + " " + .spec.serviceAccountName`), env)
		assert.Equal(t, "604800 OnPodSuccess", render(`(.spec.ttlStrategy.secondsAfterCompletion | tostring) + " " + .spec.podGC.strategy`), env)
		assert.Equal(t, "work ReadWriteOnce none", render(`.spec.volumeClaimTemplates[] | .metadata.name + " " + .spec.accessModes[0] + " " + .metadata.labels."homelab/backup"`), env)
		assert.Equal(t, "repo sha ref event default-branch", render(`[.spec.arguments.parameters[].name] | join(" ")`), env)

		steps := render(`.spec.templates[] | select(.name == "main") | .steps[][] | .name + " " + .template + " " + (.when // "always")`)
		assert.Equal(t, strings.Join([]string{
			"pending status always",
			"clone clone always",
			"settings settings always",
			"build build {{steps.settings.outputs.parameters.build}} == true",
			"sbom sbom {{steps.settings.outputs.parameters.publish}} == true && {{steps.settings.outputs.parameters.build}} == true",
			"sign sign {{steps.settings.outputs.parameters.publish}} == true && {{steps.settings.outputs.parameters.build}} == true",
		}, "\n"), steps, env)

		build := `.spec.templates[] | select(.name == "build")`
		assert.Equal(t, "build", render(build+` | .metadata.labels."homelab/ci-step"`), env)
		assert.Equal(t, `{"hostUsers": false}`, render(build+` | .podSpecPatch`), env)
		assert.Equal(t, "null", render(build+` | .securityContext.seccompProfile`), env, "the mutation gives it homelab/buildah.json")
		assert.Equal(t, "", render(`.spec.templates[] | select(.name != "build" and .name != "main" and .name != "report") | select(.securityContext.seccompProfile.type != "RuntimeDefault") | .name`), env)
		assert.Contains(t, render(build+` | .script.source`), `image="registry.$DOMAIN/$IMAGE:$SHA"`, env)
		assert.Equal(t, strings.Repeat(domain+"\n", 5)+domain, render(`.spec.templates[] | (.script // .container) | .env[] | select(.name == "DOMAIN") | .value`), env, "every step but the steps-only templates")
		assert.Equal(t, "ci-signer", render(`.spec.templates[] | select(.name == "sign") | .serviceAccountName`), env)
		assert.Contains(t, render(`.spec.templates[] | select(.name == "sign") | .script.source`), "--key hashivault://ci-signing", env)
		assert.Contains(t, render(`.spec.templates[] | select(.name == "status") | .script.source`), `https://git.$DOMAIN/api/v1/repos/$REPO/statuses/$SHA`, env)
		assert.Equal(t, "{{workflow.status}}", render(`.spec.templates[] | select(.name == "report") | .steps[][] | .arguments.parameters[] | select(.name == "state") | .value`), env)

		// Every step names its command: Argo's executor would otherwise ask the registry from the controller, whose
		// egress forbids it
		assert.Empty(t, render(`.spec.templates[] | select(.container != null or .script != null) | select(((.container // .script).command // []) | length == 0) | .name`), env)
		// Pinned: a tag with a version, never latest
		for _, image := range strings.Split(render(`.. | select(has("image")) | .image`), "\n") {
			assert.Regexp(t, `:v?[0-9]+\.[0-9]+(\.[0-9]+)?(-dev)?$`, image, env)
		}
	}
}
