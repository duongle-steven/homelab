# Commands, not builds (docs/how-to-guides/sandbox-tests.md, docs/installation/testbed.md): `just` lists them. Options
# are exported parameters ($name), so a regex or a reason reaches the shell as typed
set shell := ["bash", "-euo", "pipefail", "-c"]

mod infra
mod cli
mod test

just := just_executable()
lab := env("HOMELAB_LAB", "go -C cli run ./cmd/lab")
terragrunt := env("HOMELAB_TERRAGRUNT", "terragrunt")
terragrunt_apply := terragrunt + " run --all apply --non-interactive -- -auto-approve"
# Apps the sandbox deploys only when asked (just dev --apps paperless,opencloud or all) or when the branch changes
# their file since --base (--apps none: never, even then): docs/how-to-guides/sandbox-tests.md
optional_apps := "paperless opencloud"
testbed_kubeconfig := justfile_directory() / "infra/testbed/kubeconfig.yaml"
# The power specs' labels, on a healthy testbed, one run each since each takes the whole cluster down
# (docs/how-to-guides/power.md): --tests runs others (cut-postgres). They do not count towards the mark
power_tests := "cut-every-node shutdown-startup node-not-returning"

[private]
default:
    @{{ just }} --list

# The sandbox: the specs the branch's changes can affect (test/scopes.yaml), --full for all, --tests '<label filter>'
# for some. The sandbox syncs GitHub branch sandbox (system/argocd/values-dev.yaml): test the commit you are on;
# --revision <sha> syncs that commit instead and pushes nothing (GitHub Actions). Alone specs (label alone: NodeRestart
# restarts the node) run after the others, each in its own Ginkgo run (lab test scope --alone --each). The image caches
# are filled before a build and, after the specs, with what the node pulled without them (scripts/warm-image-cache);
# lab's image for the scheduled rotation is built from the checkout (scripts/build-lab-image)
[arg("apps", long)]
[arg("base", long)]
[arg("full", long, value="1")]
[arg("revision", long)]
[arg("tests", long)]
[doc("The sandbox's tests for this branch (--full, --tests, --apps, --revision, --base)")]
dev $full="" $tests="" $apps="" $revision="" $base="origin/dev": sandbox-checkout (sandbox-clean revision)
    #!/usr/bin/env bash
    set -euo pipefail
    scripts/testbed-guard dev
    # A sandbox stopped for the testbed runs stale when started again hours later (timing-sensitive specs fail on it):
    # rebuild it instead
    if scripts/sandbox-stopped; then
        echo "just dev: the sandbox is stopped (the testbed ran?): rebuilding it from scratch" >&2
        {{ just }} dev-clean
    fi
    export KUBECONFIG="$PWD/infra/sandbox/kubeconfig.yaml" KUBE_CONFIG_PATH="$PWD/infra/sandbox/kubeconfig.yaml"
    if [ -n "$tests" ]; then sel=(--tests "$tests")
    elif [ -n "$full" ]; then sel=(--full)
    else git fetch -q origin dev || echo "just dev: git fetch failed, comparing with the local $base" >&2; sel=(--base "$base"); fi
    main=$({{ lab }} test scope "${sel[@]}")
    alone=$({{ lab }} test scope "${sel[@]}" --alone --each)
    if [ -z "$main$alone" ]; then echo "just dev: nothing to test in the sandbox (no change outside test/scopes.yaml's skip)"; exit 0; fi
    echo "just dev: main run '$main', then alone '$(printf '%s' "$alone" | tr '\n' ' ')'"
    opt=$( { if [ "$apps" = none ]; then :; elif [ "$apps" = all ]; then echo "{{ optional_apps }}"; else echo "$apps" | tr , ' '; fi
        [ "$apps" = none ] || for a in {{ optional_apps }}; do git diff --quiet "$base...HEAD" -- "apps/$a.yaml" 2>/dev/null || echo "$a"; done; } |
        tr ' ' '\n' | sed '/^$/d' | sort -u | tr '\n' ' ')
    for a in $opt; do case " {{ optional_apps }} " in *" $a "*) ;; *) echo "just dev: $a is not an optional app ({{ optional_apps }})" >&2; exit 1 ;; esac; done
    echo "just dev: optional apps '$opt'"
    if [ -z "$revision" ]; then {{ just }} sandbox-push; fi
    scripts/warm-image-cache
    (cd infra/sandbox && HOMELAB_SANDBOX_REVISION="$([ -z "$revision" ] || git rev-parse HEAD)" HOMELAB_SANDBOX_APPS="$opt" {{ terragrunt_apply }})
    for a in {{ optional_apps }}; do case " $opt " in *" $a "*) ;; *) kubectl delete namespace "$a" --ignore-not-found --wait=false >/dev/null ;; esac; done
    scripts/build-lab-image
    export HOMELAB_SANDBOX_APPS="$opt"
    status=0
    if [ -n "$main" ]; then {{ just }} sandbox-refresh && {{ just }} test e2e --env sandbox --labels "$main" || status=$?; fi
    if [ "$status" = 0 ] && [ -n "$alone" ]; then
        refresh=$([ -n "$main" ] || echo 1)
        while IFS= read -r f; do
            [ -n "$f" ] || continue
            if [ -n "$refresh" ]; then {{ just }} sandbox-refresh; refresh=; fi
            {{ just }} test e2e --env sandbox --labels "$f" </dev/null || status=1
        done <<<"$alone"
    fi
    scripts/warm-image-cache --node || true
    exit "$status"

# From scratch, the whole suite: before a dev → master PR and a release (--tests runs those instead)
[arg("apps", long)]
[arg("tests", long)]
[doc("Rebuild the sandbox from scratch and run the whole suite")]
dev-fresh $apps="" $tests="":
    {{ just }} dev-clean
    {{ just }} dev --full ${apps:+--apps "$apps"} ${tests:+--tests "$tests"}

[doc("Remove the sandbox")]
dev-clean: sandbox-checkout
    cd infra/sandbox && {{ terragrunt }} run --all destroy --non-interactive -- -auto-approve

# kubescape on the running sandbox, against .cluster-scan-accepted.yaml, by hand (docs/how-to-guides/cluster-scan.md)
[doc("kubescape on the running sandbox")]
dev-scan: sandbox-checkout
    scripts/testbed-guard dev
    {{ just }} test e2e --env sandbox --labels scan --verbose

# The sandbox's OpenTofu state is .state/ in this checkout (git-ignored): a git worktree has none, and running the
# sandbox from it would put a new OpenBao key over the running cluster's
[private]
sandbox-checkout:
    #!/usr/bin/env bash
    set -euo pipefail
    common=$(git rev-parse --path-format=absolute --git-common-dir)
    if [ "$(git rev-parse --path-format=absolute --git-dir)" != "$common" ]; then
        echo "just: run the sandbox from the main checkout ($(dirname "$common")), not a git worktree" >&2
        exit 1
    fi

# just dev tests the checkout (the tests and their scope come from it): the commit at HEAD, which --revision must name
[private]
sandbox-clean $revision="":
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
        echo "commit your changes first: just dev tests the commit at HEAD"
        git status --short --untracked-files=no; exit 1
    fi
    if [ -n "$revision" ] && [ "$(git rev-parse --verify -q "$revision^{commit}")" != "$(git rev-parse HEAD)" ]; then
        echo "just dev: --revision $revision is not HEAD: check that commit out (the tests and their scope come from the checkout)"
        exit 1
    fi

[private]
sandbox-push: sandbox-clean
    git push --force origin HEAD:sandbox

# An existing sandbox picks up the new commit now instead of at the next poll; on a fresh one there is nothing yet
[private]
sandbox-refresh:
    -@kubectl --kubeconfig infra/sandbox/kubeconfig.yaml -n argocd annotate applicationsets --all argocd.argoproj.io/application-set-refresh=true --overwrite >/dev/null 2>&1
    -@kubectl --kubeconfig infra/sandbox/kubeconfig.yaml -n argocd annotate applications --all argocd.argoproj.io/refresh=normal --overwrite >/dev/null 2>&1

# Testbed: three NixOS VMs in WSL (infra/testbed, docs/installation/testbed.md), never beside the sandbox: what needs
# several nodes. The commit at HEAD goes to GitHub branch testbed, which its Argo CD syncs; infra/testbed installs the
# VMs nixie has not (PXE), enrolls them, upgrades the others in place and bootstraps Argo CD; then the tests. Main
# checkout only: its state is .state/testbed. The commit it started from is the one it marks: it refuses another (a git
# pull during the run)
[doc("Build or update the testbed's VMs and run its tests (the mark)")]
testbed: sandbox-checkout sandbox-clean
    #!/usr/bin/env bash
    set -euo pipefail
    proof_commit=$(git rev-parse HEAD)
    scripts/testbed-guard testbed
    scripts/ssh-key-loaded testbed
    gh auth status >/dev/null || { echo "just testbed: gh is not logged in, so the mark could not be written" >&2; exit 1; }
    git push --force origin "$proof_commit:refs/heads/testbed"
    (cd infra/testbed && {{ terragrunt_apply }})
    kubectl --kubeconfig {{ testbed_kubeconfig }} -n argocd annotate applicationsets --all argocd.argoproj.io/application-set-refresh=true --overwrite >/dev/null 2>&1 || true
    kubectl --kubeconfig {{ testbed_kubeconfig }} -n argocd annotate applications --all argocd.argoproj.io/refresh=normal --overwrite >/dev/null 2>&1 || true
    {{ just }} test e2e --env testbed --labels proof
    test "$(git rev-parse HEAD)" = "$proof_commit" || { echo "just testbed: HEAD moved from $proof_commit during the run: no mark" >&2; exit 1; }
    scripts/mark-proof testbed "$proof_commit"

# Reinstalls the VMs from empty disks (nixie), then the tests
[doc("Reinstall the testbed's VMs from empty disks, then its tests")]
testbed-fresh: sandbox-checkout sandbox-clean
    scripts/testbed-guard testbed
    {{ just }} testbed-clean
    {{ just }} testbed

# Removes the VMs, their disks and network, nixie's install record and their host keys. argocd holds only what is in
# the cluster, which goes with the disks: its state is dropped, since destroying it asks a cluster that may not answer
[doc("Remove the testbed's VMs, disks and records")]
testbed-clean: sandbox-checkout
    rm -rf .state/testbed/argocd
    cd infra/testbed/metal && {{ terragrunt }} destroy --non-interactive -- -auto-approve
    cd infra/testbed/vms && {{ terragrunt }} destroy --non-interactive -- -auto-approve
    rm -f .state/testbed/metal/nixie-hosts.json
    for ip in $(jq -r '.[].ip' infra/testbed/metal/hosts.json); do ssh-keygen -R "$ip" >/dev/null 2>&1 || true; done

[arg("tests", long)]
[doc("The power specs on the testbed, one run each (--tests: labels, space-separated)")]
testbed-power $tests="": sandbox-checkout
    #!/usr/bin/env bash
    set -euo pipefail
    scripts/testbed-guard testbed
    for t in ${tests:-{{ power_tests }}}; do
        {{ just }} test e2e --env testbed --labels "$t" --verbose
    done

# kube-bench on every node, against .cluster-scan-accepted.yaml, by hand (docs/how-to-guides/cluster-scan.md): not part
# of the mark
[doc("kube-bench on the testbed's nodes")]
testbed-scan: sandbox-checkout
    scripts/testbed-guard testbed
    {{ just }} test e2e --env testbed --labels scan --verbose

# Shuts the VMs down (they keep their disks: just testbed starts them again), to run the sandbox
[doc("Shut the testbed's VMs down")]
testbed-stop:
    for name in $(jq -r 'keys[]' infra/testbed/metal/hosts.json); do virsh -c qemu:///system shutdown "$name" || true; done

[doc("Serve the documentation locally")]
docs:
    mkdocs serve
