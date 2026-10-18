.POSIX:
.PHONY: *
.EXPORT_ALL_VARIABLES:

env ?= production
KUBECONFIG = $(shell pwd)/infra/$(env)/kubeconfig.yaml
KUBE_CONFIG_PATH = $(KUBECONFIG)
smoke_filter ?= Smoke
terragrunt_apply = terragrunt run --all apply --non-interactive -- -auto-approve
# make dev: the sandbox tests the branch's changes can affect (test/scopes.yaml), or scope=full, or tests=<regex>
scope ?= auto
base ?= origin/dev
tests ?=
lab = cd cli && go run ./cmd/lab test scope

default: metal infra smoke-test clean

# The sandbox syncs GitHub branch sandbox (system/argocd/values-dev.yaml): test the commit you are on. Alone tests
# (TestNodeRestart restarts the node) run after the others
dev:
	@if [ -n "$(tests)" ]; then sel="--tests '$(tests)'"; \
	elif [ "$(scope)" = full ]; then sel=--full; \
	else git fetch -q origin dev || echo "make dev: git fetch failed, comparing with the local $(base)" >&2; sel="--base $(base)"; fi; \
	main=$$(eval "$(lab) $$sel") && alone=$$(eval "$(lab) $$sel --alone") || exit 1; \
	if [ -z "$$main$$alone" ]; then echo "make dev: nothing to test in the sandbox (no change outside test/scopes.yaml's skip)"; exit 0; fi; \
	echo "make dev: main run '$$main', then alone '$$alone'"; \
	$(MAKE) sandbox-push && \
	(cd infra/sandbox && $(terragrunt_apply)) && \
	if [ -n "$$main" ]; then $(MAKE) env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter="$$main" sandbox-refresh smoke-test; fi && \
	if [ -n "$$alone" ]; then $(MAKE) env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter="$$alone" $$([ -n "$$main" ] || echo sandbox-refresh) smoke-test; fi

# From scratch, the whole suite: before a dev → master PR and a release
dev-fresh:
	$(MAKE) dev-clean
	$(MAKE) dev scope=full

sandbox-push:
	@if [ -n "$$(git status --porcelain --untracked-files=no)" ]; then \
		echo "commit your changes first: make dev tests the commit at HEAD"; \
		git status --short --untracked-files=no; exit 1; \
	fi
	git push --force origin HEAD:sandbox

# An existing sandbox picks up the new commit now instead of at the next poll; on a fresh one there is nothing yet
sandbox-refresh:
	-@kubectl -n argocd annotate applicationsets --all argocd.argoproj.io/application-set-refresh=true --overwrite >/dev/null 2>&1
	-@kubectl -n argocd annotate applications --all argocd.argoproj.io/refresh=normal --overwrite >/dev/null 2>&1

dev-clean:
	cd infra/sandbox && terragrunt run --all destroy --non-interactive -- -auto-approve

metal:
	make -C metal

# Every unit of infra/<env>: Argo CD's bootstrap, OpenBao's key, OpenTofu's secrets (the sandbox's run in make dev)
infra:
	cd infra/$(env) && $(terragrunt_apply)

smoke-test:
	make -C test filter="$(smoke_filter)"

test:
	make -C test

clean:
	docker compose --project-directory ./metal/roles/pxe_server/files down

docs:
	mkdocs serve

git-hooks:
	pre-commit install
