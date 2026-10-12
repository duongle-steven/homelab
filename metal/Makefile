.POSIX:

env ?= prod
export KUBECONFIG = $(shell pwd)/kubeconfig.yaml

kind_cluster = homelab-dev
kind_subnet = 172.30.0.0/16

default: ${env}

prod: boot cluster

dev: kind cluster proxy

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

proxy_name = homelab-dev-proxy
proxy_bind = 127.0.0.1
ingress_ip = 172.30.255.200

proxy:
	@if docker ps --format '{{.Names}}' | grep -qx ${proxy_name}; then exit 0; fi; \
	docker rm -f ${proxy_name} >/dev/null 2>&1; \
	if ss -ltnH '( sport = :80 or sport = :443 )' | grep -q .; then \
		echo "ports 80/443 are already in use on this host:"; ss -ltnp '( sport = :80 or sport = :443 )'; exit 1; \
	fi; \
	docker run -d --name ${proxy_name} --network host --restart unless-stopped \
		--entrypoint sh alpine/socat:1.8.1.3 -c '\
			socat TCP-LISTEN:80,bind=${proxy_bind},fork,reuseaddr TCP:${ingress_ip}:80 & \
			exec socat TCP-LISTEN:443,bind=${proxy_bind},fork,reuseaddr TCP:${ingress_ip}:443'

clean:
	docker rm -f ${proxy_name} >/dev/null 2>&1 || true
	kind delete cluster --name ${kind_cluster}
	docker network rm kind >/dev/null 2>&1 || true
	rm -f kubeconfig.yaml
