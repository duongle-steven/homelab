package offline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docs/superpowers/specs/2026-11-20-sandbox-groups-design.md: every CRD comes from system/crds, generated from each
// component's pinned chart; no component installs its own (Argo CD's stay with the bootstrap's system/argocd)
func TestNoComponentShipsCRDs(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join(repo.Root(), "system", "*", "Chart.yaml"))
	require.NoError(t, err)
	more, err := filepath.Glob(filepath.Join(repo.Root(), "platform", "*", "Chart.yaml"))
	require.NoError(t, err)
	for _, chart := range append(dirs, more...) {
		dir, _ := filepath.Rel(repo.Root(), filepath.Dir(chart))
		if dir == "system/argocd" || dir == "system/crds" {
			continue
		}
		// rook-ceph's ceph-csi-operator templates its csi.ceph.io CRDs with no switch (rook-ceph never runs in the sandbox)
		skip := map[string]string{"system/rook-ceph": ` and .spec.group != "csi.ceph.io"`}[dir]
		for _, env := range []string{"", "dev"} {
			assert.Empty(t, renderVelero(t, dir, env, `select(.kind == "CustomResourceDefinition"`+skip+`) | .metadata.name`), "%s (%s) installs CRDs", dir, env)
		}
	}
}

func TestCRDsInOnePlace(t *testing.T) {
	names := strings.Fields(renderVelero(t, "system/crds", "", `select(.kind == "CustomResourceDefinition") | .metadata.name`))
	for _, want := range []string{"certificates.cert-manager.io", "externalsecrets.external-secrets.io", "policyexceptions.kyverno.io",
		"servicemonitors.monitoring.coreos.com", "authorizationpolicies.security.istio.io", "kanidmoauth2clients.kaniop.rs",
		"clusters.postgresql.cnpg.io", "workflows.argoproj.io", "backups.velero.io", "volumesnapshots.snapshot.storage.k8s.io"} {
		assert.Contains(t, names, want)
	}
	seen := map[string]bool{}
	for _, n := range names {
		assert.False(t, seen[n], "%s twice", n)
		seen[n] = true
	}
	// Never pruned: deleting a CRD deletes every object of its kind in the cluster
	options := strings.Fields(renderVelero(t, "system/crds", "", `select(.kind == "CustomResourceDefinition") | .metadata.annotations["argocd.argoproj.io/sync-options"]`))
	assert.Len(t, options, len(names))
	for _, o := range options {
		assert.Contains(t, o, "Prune=false")
	}
}

// A chart bumped without its CRDs regenerated fails the basic check, naming the command
func TestVendorCRDsCheck(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "worktree", "add", "--detach", dir)
	cmd.Dir = repo.Root()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() {
		c := exec.Command("git", "worktree", "remove", "--force", dir)
		c.Dir = repo.Root()
		_ = c.Run()
	})
	// The working tree's state, not HEAD's
	for _, p := range []string{"scripts/vendor-crds", "system/crds"} {
		require.NoError(t, exec.Command("cp", "-r", filepath.Join(repo.Root(), p), filepath.Dir(filepath.Join(dir, p))).Run())
	}
	file := filepath.Join(dir, "system", "crds", "files", "kyverno", "policyexceptions.kyverno.io.yaml")
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte(strings.Replace(string(raw), "kind: CustomResourceDefinition", "kind: CustomResourceDefinitionX", 1)), 0o644))
	// One component: checking all of them renders every chart again
	check := exec.Command("scripts/vendor-crds", "--check", "kyverno")
	check.Dir = dir
	out, err = check.CombinedOutput()
	assert.Error(t, err)
	assert.Contains(t, string(out), "scripts/vendor-crds kyverno")
}

// The bootstrap applies every CRD before Argo CD's ApplicationSets exist, so no Application ever syncs ahead of a
// CRD it uses (an ApplicationSet's sync waves would not order its Applications)
func TestBootstrapAppliesCRDsFirst(t *testing.T) {
	dir, env := fakeBin(t, map[string]string{"kubectl": `case "$*" in
  *"create namespace"*) printf 'apiVersion: v1\nkind: Namespace\nmetadata: {name: argocd}\n' ;;
  *wait*) ;;
  *"get secret"*) ;;
  *apply*) yq -N '.kind + "/" + .metadata.name' >> "$FIXTURES/applied" ;;
  *) exit 1 ;;
esac`})
	root, err := filepath.Abs(repo.Root())
	require.NoError(t, err)
	cmd := exec.Command("sh", root+"/infra/_modules/argocd/bootstrap.sh")
	cmd.Env = append(env, "CHART="+root+"/system/argocd", "VALUES=values-dev.yaml", "ENVIRONMENT=dev",
		"REPOSITORY=https://github.com/example/homelab", "BRANCH=sandbox", "KUBECONFIG=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	raw, err := os.ReadFile(filepath.Join(dir, "applied"))
	require.NoError(t, err)
	applied := strings.Fields(string(raw))
	crd, appset := -1, -1
	for i, o := range applied {
		if o == "CustomResourceDefinition/policyexceptions.kyverno.io" && crd < 0 {
			crd = i
		}
		if strings.HasPrefix(o, "ApplicationSet/") && appset < 0 {
			appset = i
		}
	}
	require.GreaterOrEqual(t, crd, 0, "system/crds never applied: %v", applied)
	bootstrap, err := os.ReadFile(filepath.Join(root, "infra/_modules/argocd/bootstrap.sh"))
	require.NoError(t, err)
	assert.Contains(t, string(bootstrap), `apply-crds.sh" "$CHART/../.." "$environment"`, "the bootstrap passes its environment")
	require.GreaterOrEqual(t, appset, 0)
	assert.Less(t, crd, appset, "an ApplicationSet before system/crds")
}

// Argo CD does not manage system/crds (the bootstrap does, like system/argocd), and nothing waits on a CRD any more
func TestNoCRDOrderWorkarounds(t *testing.T) {
	for _, env := range []string{"", "dev", "testbed"} {
		dirs := renderVelero(t, "system/argocd", env, `select(.kind == "ApplicationSet" and .metadata.name == "root") | .. | select(has("path") and .path == "system/crds") | (.exclude // false)`)
		for _, excluded := range strings.Fields(dirs) {
			assert.Equal(t, "true", excluded, "env=%s: root syncs system/crds", env)
		}
	}
	cmd := exec.Command("git", "grep", "-l", "SkipDryRunOnMissingResource", "--", "system", "platform", "apps")
	cmd.Dir = repo.Root()
	out, _ := cmd.Output()
	assert.Empty(t, strings.TrimSpace(string(out)), "files still waiting on a CRD")
}

// Every kind a chart or app renders in any environment has its CRD in system/crds (or Argo CD's own, or is built in):
// a CRD only one environment's values enable (trust-manager's Bundle, the sandbox's) is vendored too
func TestEveryKindHasItsCRD(t *testing.T) {
	served := map[string]bool{}
	for _, chart := range []string{"system/crds", "system/argocd"} {
		for _, gk := range strings.Fields(renderVelero(t, chart, "", `select(.kind == "CustomResourceDefinition") | .spec.group + "/" + .spec.names.kind`)) {
			served[gk] = true
		}
	}
	dirs, err := filepath.Glob(filepath.Join(repo.Root(), "*", "*", "Chart.yaml"))
	require.NoError(t, err)
	apps, err := filepath.Glob(filepath.Join(repo.Root(), "apps", "*.yaml"))
	require.NoError(t, err)
	var targets []string
	for _, c := range dirs {
		dir, _ := filepath.Rel(repo.Root(), filepath.Dir(c))
		if strings.HasPrefix(dir, "system/") || strings.HasPrefix(dir, "platform/") {
			targets = append(targets, dir)
		}
	}
	for _, a := range apps {
		rel, _ := filepath.Rel(repo.Root(), a)
		targets = append(targets, rel)
	}
	for _, target := range targets {
		envs := []string{"", "dev", "testbed"}
		if strings.HasPrefix(target, "apps/") {
			// The testbed runs no apps
			envs = envs[:2]
		}
		for _, env := range envs {
			for _, gk := range strings.Fields(renderVelero(t, target, env, `select(.apiVersion != null) | (.apiVersion | sub("/[^/]*$", "")) + "/" + .kind`)) {
				group := gk[:strings.LastIndex(gk, "/")]
				// Built in: the core group, the k8s.io groups (the Gateway API's from k3s's manifests), and Argo CD's
				// rook-ceph ships csi.ceph.io's CRDs itself (TestNoComponentShipsCRDs)
				if !strings.Contains(group, ".") || strings.HasSuffix(group, ".k8s.io") || served[gk] ||
					(target == "system/rook-ceph" && group == "csi.ceph.io") {
					continue
				}
				assert.Fail(t, "kind without its CRD", "%s (env %q) uses %s: run scripts/vendor-crds for its component", target, env, gk)
			}
		}
	}
}

// A running cluster gets a commit's CRDs before its Argo CD can see the commit: a component that stops rendering a CRD
// would otherwise have it pruned, with every object of its kind, before the bootstrap ran (the sandbox-groups review)
func TestCRDsBeforePush(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repo.Root(), "justfile"))
	require.NoError(t, err)
	justfile := string(raw)
	recipe := func(name string) string {
		start := strings.Index(justfile, "\n"+name)
		require.GreaterOrEqual(t, start, 0, name)
		end := strings.Index(justfile[start+1:], "\n\n")
		return justfile[start : start+1+end]
	}
	for name, push := range map[string]string{"dev $full": "sandbox-push", "testbed:": `git push --force origin "$proof_commit`} {
		body := recipe(name)
		// each with its own environment, whose list it applies (docs/superpowers/specs/2026-11-25-testbed-capacity-design.md)
		env := map[string]string{"dev $full": "dev", "testbed:": "testbed"}[name]
		assert.Regexp(t, `apply-crds\.sh "\$PWD" `+env+`\b`, body, name)
		crds, pushed := strings.Index(body, "apply-crds.sh"), strings.Index(body, push)
		require.GreaterOrEqual(t, crds, 0, "%s applies no CRDs", name)
		require.GreaterOrEqual(t, pushed, 0, "%s pushes nothing", name)
		assert.Less(t, crds, pushed, "%s pushes before it applies the CRDs", name)
	}
}

// A chart that keeps CRDs in crds/ renders none through scripts/render-chart (--skip-crds, like Argo CD's skipCrds), so
// TestNoComponentShipsCRDs cannot see them: rendered with them, every CRD a component ships is vendored in system/crds
// (else its controller would find none: the sandbox-groups review)
func TestShippedCRDsVendored(t *testing.T) {
	t.Setenv("HOMELAB_RENDER_CRDS", "1")
	dirs, err := filepath.Glob(filepath.Join(repo.Root(), "*", "*", "Chart.yaml"))
	require.NoError(t, err)
	for _, c := range dirs {
		dir, _ := filepath.Rel(repo.Root(), filepath.Dir(c))
		if !strings.HasPrefix(dir, "system/") && !strings.HasPrefix(dir, "platform/") || dir == "system/argocd" || dir == "system/crds" {
			continue
		}
		for _, env := range []string{"", "dev"} {
			for _, name := range strings.Fields(renderVelero(t, dir, env, `select(.kind == "CustomResourceDefinition" and .spec.group != "csi.ceph.io") | .metadata.name`)) {
				_, err := os.Stat(filepath.Join(repo.Root(), "system", "crds", "files", filepath.Base(dir), name+".yaml"))
				assert.NoError(t, err, "%s (%s) ships %s, which system/crds lacks: add the component to scripts/vendor-crds", dir, env, name)
			}
		}
	}
}

// scripts/ci-check runs every check it is given, not only the first (the sandbox-groups ledger: three tasks' checks
// had run one each)
func TestCICheckRunsEveryName(t *testing.T) {
	cmd := exec.Command("scripts/ci-check", "workflows", "no-such-check")
	cmd.Dir = repo.Root()
	out, err := cmd.CombinedOutput()
	assert.Error(t, err)
	assert.Contains(t, string(out), "unknown check no-such-check")
}

// apply-crds.sh applies an environment's list (system/crds/environments/<env>.txt), every CRD for the sandbox, and
// refuses an environment it does not know
func TestApplyCRDsByEnvironment(t *testing.T) {
	dir, env := fakeBin(t, map[string]string{"kubectl": `yq -N 'select(.kind == "CustomResourceDefinition") | .metadata.name' > "$FIXTURES/applied"`})
	root, err := filepath.Abs(repo.Root())
	require.NoError(t, err)
	apply := func(environment string) ([]string, error) {
		_ = os.Remove(filepath.Join(dir, "applied"))
		cmd := exec.Command("sh", root+"/infra/_modules/argocd/apply-crds.sh", root, environment)
		cmd.Env = append(env, "KUBECONFIG=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("%w: %s", err, out)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "applied"))
		return strings.Fields(string(raw)), err
	}
	all, err := apply("dev")
	require.NoError(t, err)
	testbed, err := apply("testbed")
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(root, "system/crds/environments/testbed.txt"))
	require.NoError(t, err)
	listed := 0
	for _, l := range strings.Split(string(raw), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			listed++
		}
	}
	assert.Len(t, testbed, listed, "the testbed applies its list")
	assert.Greater(t, len(all), len(testbed), "the sandbox applies every CRD")
	assert.Contains(t, all, "policyexceptions.kyverno.io")
	assert.NotContains(t, testbed, "policyexceptions.kyverno.io", "the testbed runs no Kyverno")
	assert.Contains(t, testbed, "certificates.cert-manager.io")
	seed, err := apply("seed")
	require.NoError(t, err)
	prod, err := apply("prod")
	require.NoError(t, err)
	assert.Equal(t, prod, seed, "the seed is production's first install: production's list")
	_, err = apply("nowhere")
	assert.Error(t, err, "an environment without a list is refused, never applied whole by mistake")
}
