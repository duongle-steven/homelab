package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sandbox-scope decides whether a push to dev runs the sandbox: the whole suite unless every change since the
// previous push is skipped, and never "nothing to test" because the selector failed
func TestCISandboxScope(t *testing.T) {
	const before = "1111111111111111111111111111111111111111"
	for _, c := range []struct {
		name, lab, before, want string
		fails                    bool
	}{
		{"changes to test", `[ "$5" = --alone ] && echo NodeRestart || echo Smoke`, before, "scope=full", false},
		{"every change skipped", `echo`, before, "", false},
		{"only alone tests", `[ "$5" = --alone ] && echo NodeRestart || echo`, before, "scope=full", false},
		{"selector fails", `echo "base does not exist" >&2; exit 1`, before, "", true},
		{"new branch", `echo "must not run" >&2; exit 1`, "0000000000000000000000000000000000000000", "scope=full", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, env := fakeBin(t, map[string]string{"lab": `printf '%s ' "$@" >> "$FIXTURES/calls"; echo >> "$FIXTURES/calls"
` + c.lab})
			out, err := runScript(t, append(env, "SANDBOX_LAB="+filepath.Join(dir, "lab")), "sandbox-scope", c.before)
			if c.fails {
				require.Error(t, err, out)
				return
			}
			require.NoError(t, err, out)
			assert.Equal(t, c.want, lastLine(out))
			if c.before == before {
				calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
				assert.Contains(t, string(calls), "test scope --base "+before)
			}
		})
	}
}

func lastLine(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			return s[i+1:]
		}
	}
	return s
}
