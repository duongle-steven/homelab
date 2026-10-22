.POSIX:
.PHONY: *
.EXPORT_ALL_VARIABLES:

env ?= production
KUBECONFIG = $(shell pwd)/infra/$(env)/kubeconfig.yaml
KUBE_CONFIG_PATH = $(KUBECONFIG)
smoke_filter ?= Smoke
terragrunt = terragrunt
terragrunt_apply = $(terragrunt) run --all apply --non-interactive -- -auto-approve
# make dev: the sandbox tests the branch's changes can affect (test/scopes.yaml), or scope=full, or tests=<regex>
scope ?= auto
base ?= origin/dev
tests ?=
revision ?=
lab_cli = cd cli && go run ./cmd/lab
lab = $(lab_cli) test scope

default: metal infra smoke-test clean

# The sandbox's OpenTofu state is .state/ in this checkout (git-ignored): a git worktree has none, and running the
# sandbox from it would put a new OpenBao key over the running cluster's
sandbox-checkout:
	@if [ "$$(git rev-parse --path-format=absolute --git-dir)" != "$$(git rev-parse --path-format=absolute --git-common-dir)" ]; then \
		echo "make: run the sandbox from the main checkout ($$(dirname "$$(git rev-parse --path-format=absolute --git-common-dir)")), not a git worktree" >&2; \
		exit 1; \
	fi

# The sandbox syncs GitHub branch sandbox (system/argocd/values-dev.yaml): test the commit you are on; revision=<sha>
# syncs that commit instead and pushes nothing (GitHub Actions). Alone tests (TestNodeRestart restarts the node) run
# after the others
dev: sandbox-checkout sandbox-clean
	@if [ -n "$(tests)" ]; then sel="--tests '$(tests)'"; \
	elif [ "$(scope)" = full ]; then sel=--full; \
	else git fetch -q origin dev || echo "make dev: git fetch failed, comparing with the local $(base)" >&2; sel="--base $(base)"; fi; \
	main=$$(eval "$(lab) $$sel") && alone=$$(eval "$(lab) $$sel --alone") || exit 1; \
	if [ -z "$$main$$alone" ]; then echo "make dev: nothing to test in the sandbox (no change outside test/scopes.yaml's skip)"; exit 0; fi; \
	echo "make dev: main run '$$main', then alone '$$alone'"; \
	if [ -z "$(revision)" ]; then $(MAKE) sandbox-push; fi && \
	(cd infra/sandbox && HOMELAB_SANDBOX_REVISION="$$([ -z "$(revision)" ] || git rev-parse HEAD)" $(terragrunt_apply)) && \
	if [ -n "$$main" ]; then $(MAKE) env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter="$$main" sandbox-refresh smoke-test; fi && \
	if [ -n "$$alone" ]; then $(MAKE) env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter="$$alone" $$([ -n "$$main" ] || echo sandbox-refresh) smoke-test; fi

# From scratch, the whole suite: before a dev → master PR and a release
dev-fresh:
	$(MAKE) dev-clean
	$(MAKE) dev scope=full

# make dev tests the checkout (the tests and their scope come from it): the commit at HEAD, which revision= must name
sandbox-clean:
	@if [ -n "$$(git status --porcelain --untracked-files=no)" ]; then \
		echo "commit your changes first: make dev tests the commit at HEAD"; \
		git status --short --untracked-files=no; exit 1; \
	fi
	@if [ -n "$(revision)" ] && [ "$$(git rev-parse --verify -q '$(revision)^{commit}')" != "$$(git rev-parse HEAD)" ]; then \
		echo "make dev: revision=$(revision) is not HEAD: check that commit out (the tests and their scope come from the checkout)"; \
		exit 1; \
	fi

sandbox-push: sandbox-clean
	git push --force origin HEAD:sandbox

# An existing sandbox picks up the new commit now instead of at the next poll; on a fresh one there is nothing yet
sandbox-refresh:
	-@kubectl -n argocd annotate applicationsets --all argocd.argoproj.io/application-set-refresh=true --overwrite >/dev/null 2>&1
	-@kubectl -n argocd annotate applications --all argocd.argoproj.io/refresh=normal --overwrite >/dev/null 2>&1

dev-clean: sandbox-checkout
	cd infra/sandbox && terragrunt run --all destroy --non-interactive -- -auto-approve

metal:
	make -C metal

# Every unit of infra/<env>: Argo CD's bootstrap, OpenBao's key, OpenTofu's secrets (the sandbox's run in make dev).
# Production deploys in the same command: the guards (origin/master, checks, the merged pull request's sandbox), the
# infrastructure, Forgejo's org ops, the push of this commit to ops/homelab, then the bootstrap again (the switch to
# Forgejo on a first install). force=1 applies the infrastructure only and deploys nothing (a state migration).
# break_glass="<reason>" deploys while GitHub is down: verify checks a clean checkout that fast-forwards Forgejo instead
# (docs/how-to-guides/deploy-to-forgejo.md); never with force=1.
# Building lab first keeps a cold Go build out of credential_process, which the AWS SDK times out after a minute.
# The commit make infra started from: verify and push refuse another (a git pull during the apply)
infra: deploy_commit := $(shell git rev-parse HEAD)
# Renovate's GitHub App key (docs/how-to-guides/renovate.md), from the environment or the command line: a relative path
# names a file where make runs, not in the unit's directory (infra/production/secrets); a quoted ~/ is the home
# directory. Whole-string tests, so a path may hold spaces.
app_key_path = $(if $(filter /%,$(firstword $(1))),$(1),$(if $(filter ~/%,$(firstword $(1))),$(HOME)/$(patsubst ~/%,%,$(1)),$(CURDIR)/$(1)))
infra: override HOMELAB_GITHUB_APP_KEY := $(if $(HOMELAB_GITHUB_APP_KEY),$(call app_key_path,$(HOMELAB_GITHUB_APP_KEY)))
# The reason reaches lab through the environment, so no quote in it reaches the shell; $(value) keeps make from
# expanding a $ in it
infra: export HOMELAB_BREAK_GLASS := $(value break_glass)
infra:
	@if [ -n "$$HOMELAB_BREAK_GLASS" ] && { [ "$(force)" = 1 ] || [ "$(env)" != production ]; }; then \
		echo "break_glass deploys production and cannot be combined with force=1" >&2; exit 1; fi
	@if [ "$(env)" = sandbox ]; then $(MAKE) -s sandbox-checkout; fi
	go -C cli build -o /dev/null ./cmd/lab
	@if [ "$(env)" = production ] && [ "$(force)" != 1 ]; then \
		$(lab_cli) infra forgejo verify --commit $(deploy_commit) $${HOMELAB_BREAK_GLASS:+"--break-glass=$$HOMELAB_BREAK_GLASS"}; fi
	cd infra/$(env) && $(terragrunt_apply)
	@if [ "$(env)" = production ] && [ "$(force)" != 1 ]; then \
		$(lab_cli) infra forgejo sync && \
		$(lab_cli) infra forgejo push --commit $(deploy_commit) $${HOMELAB_BREAK_GLASS:+"--break-glass=$$HOMELAB_BREAK_GLASS"} && \
		(cd infra/production/argocd && $(terragrunt) apply --non-interactive -- -auto-approve); \
	fi

smoke-test:
	make -C test filter="$(smoke_filter)"

test:
	make -C test

clean:
	docker compose --project-directory ./metal/roles/pxe_server/files down

docs:
	mkdocs serve
