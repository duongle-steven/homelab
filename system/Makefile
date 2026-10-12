.POSIX:

env ?= prod
export KUBECONFIG = $(shell pwd)/../metal/kubeconfig.yaml

.PHONY: bootstrap
bootstrap:
	ansible-playbook \
		--extra-vars homelab_env=${env} \
		bootstrap.yml
