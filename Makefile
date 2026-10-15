.POSIX:
.PHONY: *
.EXPORT_ALL_VARIABLES:

KUBECONFIG = $(shell pwd)/metal/kubeconfig.yaml
KUBE_CONFIG_PATH = $(KUBECONFIG)
smoke_filter ?= Smoke

default: metal system external smoke-test clean

# The sandbox syncs GitHub branch sandbox (system/argocd/values-dev.yaml): test the commit you are on
dev: sandbox-push
	$(MAKE) env=dev ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='Smoke|Identity|SSO|Gateway|Forgejo|Mesh|Monitoring' metal system sandbox-refresh smoke-test

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
	$(MAKE) -C metal env=dev clean

configure:
	./scripts/configure
	git status

metal:
	make -C metal

system:
	make -C system

external:
	make -C external

smoke-test:
	make -C test filter="$(smoke_filter)"

# TODO maybe there's a better way to manage backup with GitOps?
backup:
	./scripts/backup --action setup --namespace=actualbudget --pvc=actualbudget-data
	./scripts/backup --action setup --namespace=jellyfin --pvc=jellyfin-data

restore:
	./scripts/backup --action restore --namespace=actualbudget --pvc=actualbudget-data
	./scripts/backup --action restore --namespace=jellyfin --pvc=jellyfin-data

test:
	make -C test

clean:
	docker compose --project-directory ./metal/roles/pxe_server/files down

docs:
	mkdocs serve

git-hooks:
	pre-commit install
