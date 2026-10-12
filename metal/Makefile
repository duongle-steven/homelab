.POSIX:

env ?= prod
export KUBECONFIG = $(shell pwd)/kubeconfig.yaml

kind_cluster = homelab-dev
kind_subnet = 172.30.0.0/16

default: ${env}

prod: boot cluster

dev: kind cluster

~/.ssh/id_ed25519:
	ssh-keygen -t ed25519 -P '' -f "$@"

boot: ~/.ssh/id_ed25519
	ansible-playbook \
		--inventory inventories/${env}.yml \
		boot.yml

cluster:
	ansible-playbook \
		--inventory inventories/${env}.yml \
		cluster.yml

console:
	ansible-console \
		--inventory inventories/${env}.yml

kind:
	docker network inspect kind >/dev/null 2>&1 \
		|| docker network create --subnet ${kind_subnet} kind
	kind get clusters | grep -qx ${kind_cluster} \
		|| kind create cluster --config kind-dev.yaml
	kind get kubeconfig --name ${kind_cluster} > kubeconfig.yaml
