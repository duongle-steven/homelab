.POSIX:
.PHONY: *
.EXPORT_ALL_VARIABLES:

env ?= production
KUBECONFIG = $(shell pwd)/infra/$(env)/kubeconfig.yaml
KUBE_CONFIG_PATH = $(KUBECONFIG)
smoke_filter ?= Smoke
terragrunt_apply = terragrunt run --all apply --non-interactive -- -auto-approve

default: metal infra smoke-test clean

# The sandbox syncs GitHub branch sandbox (system/argocd/values-dev.yaml): test the commit you are on
dev: sandbox-push
	cd infra/sandbox && $(terragrunt_apply)
	$(MAKE) env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='Smoke|Identity|SSO|Gateway|Forgejo|Mesh|Monitoring|Egress|Secrets|Ntfy|Logs' sandbox-refresh smoke-test
	# Restarts the node, so it runs alone, after the others
	$(MAKE) env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='NodeRestart' smoke-test

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
