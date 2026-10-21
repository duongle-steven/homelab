# Generated Secrets Straight to OpenBao Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generated secrets go from ESO's `Password` generators straight to OpenBao through PushSecrets, with no
intermediate Secret, in a component renamed `platform/generated-secrets`; tests and docs read OpenBao.

**Architecture:** Each entry of `platform/generated-secrets/values.yaml` renders a `Password` generator and a
PushSecret whose `selector.generatorRef` names it (`IfNotExists`, one `data` item per destination). OpenBao's role
`writer` is bound to namespace `generated-secrets`. Tests read generated values with the admin role (`baoValue`).

**Tech Stack:** External Secrets 2.12 (PushSecret `generatorRef`), OpenBao, Helm, Go tests (`test/`), Python check
(`scripts/check-secret-stores`).

**Spec:** `docs/superpowers/specs/2026-10-21-generated-secrets-design.md`

## Global Constraints

- Component `platform/generated-secrets`, namespace `generated-secrets`; no ExternalSecret and no Secret in it.
- PushSecret: `selector.generatorRef` (`apiVersion: generators.external-secrets.io/v1alpha1`, `kind: Password`),
  `updatePolicy: IfNotExists`, `deletionPolicy: None`, `refreshInterval: 1h`, SecretStore `openbao-writer`; each
  destination one `data` item with `match.secretKey: password`, `remoteRef.remoteKey: <destination>`,
  `remoteRef.property: <entry key>`.
- `values.yaml`'s format is unchanged (`name`, `key`, `length`, `special`, `destinations`, `extraSecrets`).
- No cross-namespace read in OpenBao's policy; a value two namespaces need is two destinations of one entry.
- Never print a secret: tests compare hashes or values in memory.
- Basic checks: `direnv exec . scripts/ci-check all`; commit, then `make dev` from the main checkout only.

## Review Focus

1. A PushSecret reconciled again (hourly, force-sync, ESO restart) must leave every existing destination unchanged —
   Task 2's sandbox test force-syncs twice and compares hashes.
2. Both destinations of `ntfy.alerts-token` must hold the same value after a fresh install — Task 2 compares them.
3. A destination deleted from OpenBao (a rotation) must be written again at the next push — Task 2 deletes one probe
   entry's destination and force-syncs.
4. The existing local sandbox (role `writer` bound to `global-secrets` in OpenBao's storage) cannot push from the new
   namespace — Task 4 rebuilds it with `make dev-fresh`, and the docs say so.
5. A Password entry with `special: true` keeps its symbols and `length` — Task 1's offline test checks the generator
   spec per entry.

---

### Task 1: the component, its render and its checks

**Files:**
- Move: `platform/global-secrets/` → `platform/generated-secrets/` (`git mv`)
- Delete: `platform/generated-secrets/templates/generated-secrets.yaml` (the ExternalSecrets); create
  `platform/generated-secrets/templates/generators.yaml`
- Modify: `platform/generated-secrets/templates/push-secrets.yaml`, `platform/generated-secrets/Chart.yaml` (name),
  `platform/generated-secrets/values.yaml` (header comment), `platform/openbao/values.yaml:87,97,135`,
  `system/argocd/values-dev.yaml:36`, `scripts/check-secret-stores`, `test/scopes.yaml:84`, `scripts/ci-check:7`
- Delete: `test/global_secrets_test.go`; create `test/generated_secrets_test.go`

**Interfaces:**
- Produces: namespace `generated-secrets`; per entry a `Password` `<name>` and a PushSecret `<name>`.

- [ ] **Step 1: Write the failing offline test** (`test/generated_secrets_test.go`)

```go
package test

import (
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type generatedEntry struct {
	Name         string   `json:"name"`
	Key          string   `json:"key"`
	Length       int      `json:"length"`
	Special      bool     `json:"special"`
	Destinations []string `json:"destinations"`
}

func renderGenerated(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart platform/generated-secrets "$1" | yq -N -o=json -I=0 "$0"`, expr, env)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

// Generated secrets go straight to OpenBao: per entry a Password generator and a PushSecret that runs it, writes only
// missing destinations (IfNotExists) and never deletes; no ExternalSecret or Secret holds a copy that could differ
// from OpenBao's value (ESO's CreatedOnce regenerates under a stale cache)
func TestGeneratedSecrets(t *testing.T) {
	values, err := exec.Command("yq", "-o=json", "-I=0", ".secrets + .extraSecrets", "../platform/generated-secrets/values.yaml").Output()
	require.NoError(t, err)
	var entries []generatedEntry
	require.NoError(t, json.Unmarshal(values, &entries))
	require.NotEmpty(t, entries)
	for _, env := range []string{"prod", "dev"} {
		kinds := renderGenerated(t, env, `[.kind] | .[]`)
		assert.NotContains(t, kinds, `"ExternalSecret"`, env)
		assert.NotContains(t, kinds, `"Secret"`, env)
		for _, e := range entries {
			length := e.Length
			if length == 0 {
				length = 32
			}
			symbols := 0
			if e.Special {
				symbols = 6
			}
			var gen struct {
				Length  int `json:"length"`
				Symbols int `json:"symbols"`
			}
			require.NoError(t, json.Unmarshal([]byte(renderGenerated(t, env, `select(.kind == "Password" and .metadata.name == "`+e.Name+`") | .spec`)), &gen), e.Name)
			assert.Equal(t, length, gen.Length, e.Name)
			assert.Equal(t, symbols, gen.Symbols, e.Name)

			var push struct {
				RefreshInterval string `json:"refreshInterval"`
				UpdatePolicy    string `json:"updatePolicy"`
				DeletionPolicy  string `json:"deletionPolicy"`
				SecretStoreRefs []struct {
					Name string `json:"name"`
				} `json:"secretStoreRefs"`
				Selector struct {
					GeneratorRef struct {
						Kind string `json:"kind"`
						Name string `json:"name"`
					} `json:"generatorRef"`
				} `json:"selector"`
				Data []struct {
					Match struct {
						SecretKey string `json:"secretKey"`
						RemoteRef struct {
							RemoteKey string `json:"remoteKey"`
							Property  string `json:"property"`
						} `json:"remoteRef"`
					} `json:"match"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal([]byte(renderGenerated(t, env, `select(.kind == "PushSecret" and .metadata.name == "`+e.Name+`") | .spec`)), &push), e.Name)
			assert.Equal(t, "1h", push.RefreshInterval, e.Name)
			assert.Equal(t, "IfNotExists", push.UpdatePolicy, e.Name)
			assert.Equal(t, "None", push.DeletionPolicy, e.Name)
			require.Len(t, push.SecretStoreRefs, 1, e.Name)
			assert.Equal(t, "openbao-writer", push.SecretStoreRefs[0].Name, e.Name)
			assert.Equal(t, "Password", push.Selector.GeneratorRef.Kind, e.Name)
			assert.Equal(t, e.Name, push.Selector.GeneratorRef.Name, e.Name)
			var remoteKeys []string
			for _, d := range push.Data {
				assert.Equal(t, "password", d.Match.SecretKey, e.Name)
				assert.Equal(t, e.Key, d.Match.RemoteRef.Property, e.Name)
				remoteKeys = append(remoteKeys, d.Match.RemoteRef.RemoteKey)
			}
			assert.Equal(t, e.Destinations, remoteKeys, e.Name)
		}
	}
}

// OpenBao's writer is the generated-secrets namespace's default service account, nothing else
func TestGeneratedSecretsWriterBinding(t *testing.T) {
	out, err := exec.Command("grep", "-n", "bound_service_account_namespaces = \\[\"generated-secrets\"\\]", "../platform/openbao/values.yaml").CombinedOutput()
	require.NoError(t, err, string(out))
}
```

In `scripts/ci-check` line 7 replace `TestGlobalSecretsCreatedOnce` with `TestGeneratedSecrets`; in
`test/scopes.yaml` replace `test/global_secrets_test.go` with `test/generated_secrets_test.go` in `skip`.

- [ ] **Step 2: Run it to see it fail**

Run: `direnv exec . env -C test go test -count=1 -run TestGeneratedSecrets ./...`
Expected: FAIL (`scripts/render-chart platform/generated-secrets`: no such directory; the binding grep finds
nothing).

- [ ] **Step 3: Move and rewrite the component**

`git mv platform/global-secrets platform/generated-secrets`; in its `Chart.yaml` set `name: generated-secrets`;
`git rm platform/generated-secrets/templates/generated-secrets.yaml`; create `templates/generators.yaml`:

```yaml
{{- range concat .Values.secrets .Values.extraSecrets }}
---
apiVersion: generators.external-secrets.io/v1alpha1
kind: Password
metadata:
  name: {{ .name }}
  namespace: {{ $.Release.Namespace }}
spec:
  length: {{ .length | default 32 }}
  digits: 6
  symbols: {{ if .special }}6{{ else }}0{{ end }}
  allowRepeat: true
  noUpper: false
{{- end }}
```

and `templates/push-secrets.yaml`:

```yaml
{{- range concat .Values.secrets .Values.extraSecrets }}
{{- $entry := . }}
{{- if .destinations }}
---
apiVersion: external-secrets.io/v1alpha1
kind: PushSecret
metadata:
  name: {{ .name }}
  namespace: {{ $.Release.Namespace }}
spec:
  # Every reconcile runs the generator once and writes that value to the destinations that do not exist (IfNotExists):
  # an existing value is never replaced, a missing one (an OpenBao that lost its data, a rotation) within the hour.
  # All destinations of a first push hold the same value. No Secret keeps a copy that could differ from OpenBao's.
  refreshInterval: 1h
  updatePolicy: IfNotExists
  deletionPolicy: None
  secretStoreRefs:
    - kind: SecretStore
      name: openbao-writer
  selector:
    generatorRef:
      apiVersion: generators.external-secrets.io/v1alpha1
      kind: Password
      name: {{ .name }}
  data:
    {{- range .destinations }}
    - match:
        secretKey: password
        remoteRef:
          remoteKey: {{ . }}
          property: {{ $entry.key }}
    {{- end }}
{{- end }}
{{- end }}
```

Replace `values.yaml`'s header comment with:

```yaml
# Random secrets generated for this cluster and written straight into OpenBao, once per destination
# (secret/<namespace>/generated/<name>, the only paths the writer reaches): a Password generator and a PushSecret
# each (templates/), no Secret in this namespace. Readers use their namespace's SecretStore openbao. A value two
# namespaces need is two destinations of one entry, written by the same push; a destination added later gets the
# existing value by hand first (docs/concepts/secrets-management.md). Never rotated automatically.
```

In `platform/openbao/values.yaml`: line 135 `bound_service_account_namespaces = ["generated-secrets"]`, and the
comments at lines 87 and 97 name `platform/generated-secrets`. In `system/argocd/values-dev.yaml` line 36
`- path: platform/generated-secrets`. In `test/scopes.yaml` line 84 `- platform/generated-secrets/**`. In
`scripts/check-secret-stores`: every `platform/global-secrets` becomes `platform/generated-secrets` (docstring,
`WRITERS`, the failure message).

- [ ] **Step 4: Run the tests and the checks**

Run: `direnv exec . env -C test go test -count=1 -run TestGeneratedSecrets ./...` — Expected: ok.
Run: `direnv exec . scripts/check-secret-stores | tail -1` — Expected: `check-secret-stores: 0 failures`.
Run: `grep -rn 'global-secrets' --exclude-dir=.git --exclude-dir=superpowers platform system scripts infra test | grep -v terragrunt-cache` —
Expected: only the tests Task 2 changes (`ntfy_test.go`, `gateway_test.go`, `sso_test.go`, `secrets_test.go`).

- [ ] **Step 5: Basic checks, commit** (`git add` first so `lab test scope --check` sees the new paths)

```bash
git add -A platform/global-secrets platform/generated-secrets platform/openbao system/argocd scripts test
direnv exec . scripts/ci-check all
git commit -m "feat(secrets): generated secrets straight to OpenBao (PushSecret generatorRef), platform/generated-secrets"
```

---

### Task 2: the sandbox tests read OpenBao

**Files:**
- Modify: `test/secrets_test.go` (`TestSecretsWriterRole`, `TestSecretsReachConsumers`, a new helper and test),
  `test/ntfy_test.go:44`, `test/gateway_test.go:142`, `test/sso_test.go:177`

**Interfaces:**
- Consumes: `baoValue(t, path, property string) string` (`test/forgejo_test.go`, admin role).
- Produces: `generatedValue(t *testing.T, path, property string) string` (waits up to 20 minutes for the push).

- [ ] **Step 1: The helper and the readers**

In `test/secrets_test.go` add:

```go
// generatedValue is a generated secret as every app reads it, from OpenBao (secret/<path>, admin role), once pushed
func generatedValue(t *testing.T, path, property string) string {
	t.Helper()
	var value string
	require.Eventually(t, func() bool {
		value = baoValueE(path, property)
		return value != ""
	}, 20*time.Minute, 15*time.Second, "secret/%s is not pushed", path)
	return value
}
```

and in `test/forgejo_test.go` split `baoValue` so the poll does not fail the test on the first miss:

```go
func baoValue(t *testing.T, path, property string) string {
	t.Helper()
	value := baoValueE(path, property)
	require.NotEmpty(t, value, "secret/%s#%s", path, property)
	return value
}

// baoValueE reads OpenBao the documented way: an admin login inside OpenBao's pod, the token never printed; empty on
// any error
func baoValueE(path, property string) string {
	script := "kubectl -n openbao create token openbao-admin --audience=openbao --duration=10m | kubectl -n openbao exec -i openbao-0 -c openbao -- " +
		"sh -c 'export BAO_TOKEN=$(bao write -field=token auth/kubernetes/login role=admin jwt=-); bao kv get -field=" + property + " secret/" + path + "; bao token revoke -self >/dev/null'"
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
```

Then:
- `test/ntfy_test.go`: `return "tk_" + generatedValue(t, "ntfy/generated/"+user+"-token", "token")`
- `test/gateway_test.go:142` and `test/sso_test.go:177`: `password := generatedValue(t, "zot/generated/ci", "password")`
- `TestSecretsWriterRole`: `bao(t, "writer", "generated-secrets", "default", ...)` (both calls).
- `TestSecretsReachConsumers`: `generated := generatedValue(t, "grafana/generated/admin", "password")`, and drop the
  now-tautological `assert.Equal(t, generated, grafanaAdmin(t), ...)`.

- [ ] **Step 2: Write the new sandbox test** (in `test/secrets_test.go`)

```go
// Generated secrets live only in OpenBao: no Secret in generated-secrets, a push again leaves every value as it is,
// one entry's destinations hold the same value, and a destination deleted (a rotation) is written again
func TestSecretsGeneratedOnlyInOpenBao(t *testing.T) {
	secretsSandboxOnly(t)
	waitForUnsealed(t)
	options := k8s.NewKubectlOptions("", "", "generated-secrets")
	options.Logger = logger.Discard
	secrets, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "secrets", "-o", "name")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(secrets), "a Secret in generated-secrets")

	hash := func(path, property string) string {
		sum := sha256.Sum256([]byte(generatedValue(t, path, property)))
		return hex.EncodeToString(sum[:])
	}
	// Both destinations of one entry: the same push wrote them
	assert.Equal(t, hash("ntfy/generated/alerts-token", "token"), hash("monitoring-system/generated/ntfy", "token"))

	before := hash("grafana/generated/admin", "password")
	forceSync := func(name string) {
		_, err := k8s.RunKubectlAndGetOutputE(t, options, "annotate", "pushsecret", name, fmt.Sprintf("force-sync=%d", time.Now().UnixNano()), "--overwrite")
		require.NoError(t, err)
	}
	for range 2 {
		forceSync("grafana.admin")
		time.Sleep(20 * time.Second)
		assert.Equal(t, before, hash("grafana/generated/admin", "password"), "a push replaced an existing value")
	}

	// A rotation: the destination deleted, the next push writes a new value
	probe := hash("woodpecker/generated/agent", "secret")
	admin(t, "kv", "metadata", "delete", "secret/woodpecker/generated/agent")
	forceSync("woodpecker.agent")
	assert.NotEqual(t, probe, hash("woodpecker/generated/agent", "secret"), "the deleted destination was not written again")
}
```

(`woodpecker.agent` has no reader in the sandbox, so rotating it disturbs nothing.) Add the imports `crypto/sha256`
and `encoding/hex` if missing.

- [ ] **Step 3: Compile and vet offline**

Run: `direnv exec . env -C test go vet ./...` — Expected: no output.

- [ ] **Step 4: Commit** (the sandbox run is Task 4)

```bash
git add test
git commit -m "test(secrets): read generated values from OpenBao; generated secrets live only there"
```

---

### Task 3: docs and CLAUDE.md

**Files:**
- Modify: `docs/concepts/secrets-management.md` (the generated secrets section, rotation, adding a destination),
  `docs/how-to-guides/alerts.md:10-11`, `docs/installation/post-installation.md:20,25,28`,
  `docs/installation/sandbox.md:39,82`, `CLAUDE.md` (8e done; every `global-secrets`)

- [ ] **Step 1: Rewrite the generated secrets section of `secrets-management.md`** — what the component holds
  (generators, PushSecrets, the writer's SecretStore; no Secret), why (ESO's `CreatedOnce` and a stale cache), how
  to read a value:

```sh
# A generated value, as the apps read it (admin role; the token never reaches your shell)
kubectl -n openbao create token openbao-admin --audience=openbao --duration=10m |
  kubectl -n openbao exec -i openbao-0 -c openbao -- \
    sh -c 'export BAO_TOKEN=$(bao write -field=token auth/kubernetes/login role=admin jwt=-); bao kv get -field=<property> secret/<namespace>/generated/<name>; bao token revoke -self >/dev/null'
```

  Rotation becomes: delete every destination of the entry (`bao kv metadata delete secret/<namespace>/generated/<name>`
  each, admin role), `kubectl -n generated-secrets annotate pushsecret <name> force-sync=$(date +%s) --overwrite`,
  force-sync the readers' ExternalSecrets, restart the apps, change admin passwords inside the app. Adding a
  destination to an existing entry: write the existing value there first with the admin role (`bao kv put`), then add
  it to `values.yaml`.

- [ ] **Step 2: The other docs** — `alerts.md` (phone's password: the command above with
  `secret/ntfy/generated/phone-password`, property `password`), `post-installation.md` and `sandbox.md` (paperless,
  Grafana, zot's `ci`: the same command with their paths).

- [ ] **Step 3: CLAUDE.md** — roadmap 8e gains `(done)`; every `global-secrets` in the Sandbox section and the
  production checklist becomes `generated-secrets` (or OpenBao where it says where to read a value); the Sandbox
  section notes that a sandbox built before 8e needs `make dev-fresh` once.

- [ ] **Step 4: Check and commit**

Run: `direnv exec . mkdocs build --strict -q; grep -rn 'global-secrets' CLAUDE.md docs --include=*.md | grep -v superpowers` —
Expected: exit 0; only the sentences that explain the rename.

```bash
git add docs CLAUDE.md
git commit -m "docs: generated secrets live only in OpenBao (8e)"
```

---

### Task 4: the sandbox proof

- [ ] **Step 1: Rebuild** — `direnv exec . make dev-fresh` (the local sandbox's OpenBao binds `writer` to
  `global-secrets`; a fresh one reads the new `initialize` stanza). Expected: every test passes.
- [ ] **Step 2: Check the namespace** —
  `KUBECONFIG=infra/sandbox/kubeconfig.yaml kubectl -n generated-secrets get secrets,externalsecrets,generatorstates`
  — Expected: no resources (a `GeneratorState` would mean the Password generator keeps state; ledger it if so).
- [ ] **Step 3: Ledger the evidence**, then the final review, the PR into `dev` (no `sandbox` label: the local fresh
  run covers it) with rulings and deferred minors.
