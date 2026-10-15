.POSIX:
.PHONY: *
.EXPORT_ALL_VARIABLES:

KUBECONFIG = $(shell pwd)/metal/kubeconfig.yaml
KUBE_CONFIG_PATH = $(KUBECONFIG)
smoke_filter ?= Smoke

default: metal system external smoke-test clean

dev:
	$(MAKE) env=dev ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter='Smoke|Identity|SSO|Gateway|Forgejo|Mesh' metal system smoke-test

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
