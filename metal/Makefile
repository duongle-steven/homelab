.POSIX:

env ?= prod
export KUBECONFIG = $(shell pwd)/kubeconfig.yaml

default: ${env}

prod: boot cluster

dev: k3d proxy

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

k3d_cluster = homelab-dev
k3d_node = k3d-$(k3d_cluster)-server-0
gateway_api_version = v1.6.3
gateway_api_manifest = gateway-api-$(gateway_api_version).yaml

# Gateway API CRDs for k3s's auto-deploy manifests (production: roles/k3s)
$(gateway_api_manifest):
	curl -fsSL -o "$@" https://github.com/kubernetes-sigs/gateway-api/releases/download/$(gateway_api_version)/standard-install.yaml

k3d: $(gateway_api_manifest)
	k3d cluster list $(k3d_cluster) >/dev/null 2>&1 \
		|| k3d cluster create --config k3d-dev.yaml \
			--volume "$(CURDIR)/$(gateway_api_manifest):/var/lib/rancher/k3s/server/manifests/gateway-api.yaml@server:*"
	k3d kubeconfig get $(k3d_cluster) > kubeconfig.yaml

proxy_name = homelab-dev-proxy
proxy_bind = 127.0.0.1

# ServiceLB publishes the Gateway (system/istio-system) on the node; Windows reaches WSL's 127.0.0.1 only
# through a host process, so forward 80, 443 and 22 there. Recreated each time: the node IP can change.
proxy:
	@docker rm -f $(proxy_name) >/dev/null 2>&1; \
	if ss -ltnH '( sport = :80 or sport = :443 or sport = :22 )' | grep -q .; then \
		echo "ports 80/443/22 are already in use on this host:"; ss -ltnp '( sport = :80 or sport = :443 or sport = :22 )'; exit 1; \
	fi; \
	node="$$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' $(k3d_node))"; \
	docker run -d --name $(proxy_name) --network host --restart unless-stopped \
		--entrypoint sh alpine/socat:1.8.1.3 -c "\
			socat TCP-LISTEN:80,bind=$(proxy_bind),fork,reuseaddr TCP:$$node:80 & \
			socat TCP-LISTEN:22,bind=$(proxy_bind),fork,reuseaddr TCP:$$node:22 & \
			exec socat TCP-LISTEN:443,bind=$(proxy_bind),fork,reuseaddr TCP:$$node:443" >/dev/null

clean:
	docker rm -f $(proxy_name) >/dev/null 2>&1 || true
	k3d cluster delete $(k3d_cluster)
	rm -f kubeconfig.yaml
