# The k3d sandbox: one k3s server (k3d.yaml), the latest Gateway API CRDs in k3s's auto-deploy manifests (k3s's
# packaged ones are disabled), and a localhost proxy: under WSL mirrored networking Windows cannot reach ports
# Docker publishes, so a host-network socat forwards 127.0.0.1:80/443/22 to the node (ServiceLB serves the Gateway).
# The node pulls through pull-through caches, one per registry (k3d registries outside the cluster, with their own
# volumes): deleting the cluster keeps them, so a rebuilt sandbox downloads nothing it pulled before. An image from
# another registry, or a cache that fails, is pulled from its registry directly.
terraform {
  required_version = ">= 1.13"
  required_providers {
    external = {
      source  = "hashicorp/external"
      version = "2.4.2"
    }
  }
}

variable "kubeconfig" {
  type = string
}

variable "name" {
  type    = string
  default = "homelab-dev"
}

variable "gateway_api_version" {
  type    = string
  default = "v1.6.3"
}

variable "manifest_dir" {
  type        = string
  description = "Where the Gateway API manifest is kept: k3d mounts it into the node, so it must outlive Terragrunt's cache"
}

variable "proxy_bind" {
  type    = string
  default = "127.0.0.1"
}

variable "proxy" {
  type        = bool
  default     = true
  description = "Forward 127.0.0.1:80/443/22 to the node (the browser under WSL); GitHub Actions turns it off, the tests dial the node"
}

variable "caches" {
  type = map(object({ remote = string, port = number }))
  default = {
    "docker.io"        = { remote = "https://registry-1.docker.io", port = 5101 }
    "ghcr.io"          = { remote = "https://ghcr.io", port = 5102 }
    "quay.io"          = { remote = "https://quay.io", port = 5103 }
    "registry.k8s.io"  = { remote = "https://registry.k8s.io", port = 5104 }
    "code.forgejo.org" = { remote = "https://code.forgejo.org", port = 5105 }
  }
  description = "Pull-through caches by registry; the host port is only k3d's (WSL cannot reach published ports), the node uses port 5000"
}

locals {
  # k3d names the containers k3d-<name>
  cache_names = { for host, _ in var.caches : host => "homelab-cache-${replace(host, ".", "-")}" }
  registries = yamlencode({
    mirrors = { for host, name in local.cache_names : host => { endpoint = ["http://k3d-${name}:5000"] } }
  })
  registries_file = "${var.manifest_dir}/registries.yaml"
  cache_image     = "docker.io/library/registry:3.1.2"
  config          = abspath("${path.module}/k3d.yaml")
  manifest        = "${var.manifest_dir}/gateway-api-${var.gateway_api_version}.yaml"
  node            = "k3d-${var.name}-server-0"
  proxy           = "${var.name}-proxy"
}

resource "terraform_data" "cluster" {
  triggers_replace = [filesha1(local.config), local.manifest, var.name, local.registries]
  input            = { name = var.name, kubeconfig = var.kubeconfig }

  provisioner "local-exec" {
    command = <<-EOT
      set -eu
      [ -s "${local.manifest}" ] || curl -fsSL -o "${local.manifest}" \
        https://github.com/kubernetes-sigs/gateway-api/releases/download/${var.gateway_api_version}/standard-install.yaml
      # The caches outlive the cluster: created once, never deleted here (k3d registry delete <name> empties one)
%{for host, name in local.cache_names~}
      k3d registry list ${name} >/dev/null 2>&1 || k3d registry create ${name} --image ${local.cache_image} \
        --proxy-remote-url ${var.caches[host].remote} --volume ${name}:/var/lib/registry --port 127.0.0.1:${var.caches[host].port} --no-help
%{endfor~}
      echo '${base64encode(local.registries)}' | base64 -d > "${local.registries_file}"
      k3d cluster list ${var.name} >/dev/null 2>&1 || k3d cluster create --config "${local.config}" \
%{for host, name in local.cache_names~}
        --registry-use k3d-${name}:${var.caches[host].port} \
%{endfor~}
        --volume "${local.manifest}:/var/lib/rancher/k3s/server/manifests/gateway-api.yaml@server:*" \
        --registry-config "${local.registries_file}"
      k3d kubeconfig get ${var.name} > "${var.kubeconfig}"
    EOT
  }

  provisioner "local-exec" {
    when    = destroy
    command = "k3d cluster delete ${self.input.name}; rm -f '${self.input.kubeconfig}'"
  }
}

# The node's address (it can change when the cluster is recreated)
data "external" "node" {
  depends_on = [terraform_data.cluster]
  program    = ["sh", "-c", "printf '{\"ip\":\"%s\"}' \"$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' ${local.node} 2>/dev/null)\""]
}

resource "terraform_data" "proxy" {
  count            = var.proxy ? 1 : 0
  triggers_replace = [data.external.node.result.ip, var.proxy_bind]
  input            = { name = local.proxy }

  provisioner "local-exec" {
    command = <<-EOT
      set -eu
      node="${data.external.node.result.ip}"
      [ -n "$node" ] || { echo "the k3d node ${local.node} is not running"; exit 1; }
      docker rm -f ${local.proxy} >/dev/null 2>&1 || true
      if ss -ltnH '( sport = :80 or sport = :443 or sport = :22 )' | grep -q .; then
        echo "ports 80/443/22 are already in use on this host:"; ss -ltnp '( sport = :80 or sport = :443 or sport = :22 )'; exit 1
      fi
      docker run -d --name ${local.proxy} --network host --restart unless-stopped \
        --entrypoint sh alpine/socat:1.8.1.3 -c "\
          socat TCP-LISTEN:80,bind=${var.proxy_bind},fork,reuseaddr TCP:$node:80 & \
          socat TCP-LISTEN:22,bind=${var.proxy_bind},fork,reuseaddr TCP:$node:22 & \
          exec socat TCP-LISTEN:443,bind=${var.proxy_bind},fork,reuseaddr TCP:$node:443" >/dev/null
    EOT
  }

  provisioner "local-exec" {
    when    = destroy
    command = "docker rm -f ${self.input.name} >/dev/null 2>&1 || true"
  }
}

moved {
  from = terraform_data.proxy
  to   = terraform_data.proxy[0]
}

# Changes whenever the cluster is recreated: the argocd unit bootstraps again
output "cluster_id" {
  value = terraform_data.cluster.id
}

output "kubeconfig" {
  value      = var.kubeconfig
  depends_on = [terraform_data.cluster]
}
