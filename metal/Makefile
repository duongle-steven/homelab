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
	@subnet="$$(docker network inspect kind -f '{{(index .IPAM.Config 0).Subnet}}')"; \
	if [ "$$subnet" != "${kind_subnet}" ]; then \
		echo "docker network 'kind' has subnet $$subnet, expected ${kind_subnet}; run 'make dev-clean' first"; exit 1; \
	fi
	kind get clusters | grep -qx ${kind_cluster} \
		|| kind create cluster --config kind-dev.yaml
	kind get kubeconfig --name ${kind_cluster} > kubeconfig.yaml
	@# WSL mirrored networking routes 127.0.0.1 through loopback0, where Docker
	@# drops traffic to ports published on 127.0.0.1, so use the node IP instead
	@if ip link show loopback0 >/dev/null 2>&1; then \
		ip="$$(docker inspect -f '{{.NetworkSettings.Networks.kind.IPAddress}}' ${kind_cluster}-control-plane)"; \
		sed -i "s#server: https://127.0.0.1:[0-9]*#server: https://$$ip:6443#" kubeconfig.yaml; \
	fi

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
