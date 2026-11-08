package offline

import (
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// During a rotation a user's previous token is listed beside the new one (ntfy accepts several tokens per user), so
// readers still holding the old one keep publishing until it is revoked (step 11b)
func TestNtfyAcceptsPreviousToken(t *testing.T) {
	auth := renderVelero(t, "apps/ntfy.yaml", "", `select(.kind == "ExternalSecret" and .metadata.name == "ntfy-auth") | .spec`)
	tokens := renderVelero(t, "apps/ntfy.yaml", "", `select(.kind == "ExternalSecret" and .metadata.name == "ntfy-auth") | .spec.target.template.data.NTFY_AUTH_TOKENS`)
	// Each token path is extracted whole, its keys prefixed with the user: previous_token appears only during a rotation
	for _, user := range []string{"alerts", "phone"} {
		assert.Contains(t, auth, "key: ntfy/generated/"+user+"-token", user)
		assert.Contains(t, auth, "target: "+user+"_$1", user)
	}
	tmpl, err := template.New("tokens").Parse(tokens)
	require.NoError(t, err)
	render := func(data map[string]string) string {
		var b strings.Builder
		require.NoError(t, tmpl.Execute(&b, data))
		return b.String()
	}
	assert.Equal(t, "alerts:tk_new,phone:tk_p", render(map[string]string{"alerts_token": "new", "phone_token": "p"}))
	assert.Equal(t, "alerts:tk_new,alerts:tk_old,phone:tk_p",
		render(map[string]string{"alerts_token": "new", "alerts_previous_token": "old", "phone_token": "p"}))
	assert.Equal(t, "alerts:tk_a,phone:tk_new,phone:tk_old",
		render(map[string]string{"alerts_token": "a", "phone_token": "new", "phone_previous_token": "old"}))
}
