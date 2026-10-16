# The k3d sandbox: one k3s server (k3d.yaml), the latest Gateway API CRDs in k3s's auto-deploy manifests (k3s's
# packaged ones are disabled), and a localhost proxy: under WSL mirrored networking Windows cannot reach ports
# Docker publishes, so a host-network socat forwards 127.0.0.1:80/443/22 to the node (ServiceLB serves the Gateway)
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

variable "proxy_bind" {
  type    = string
  default = "127.0.0.1"
}

locals {
  config   = abspath("${path.module}/k3d.yaml")
  manifest = abspath("${path.module}/gateway-api-${var.gateway_api_version}.yaml")
  node     = "k3d-${var.name}-server-0"
  proxy    = "${var.name}-proxy"
}

resource "terraform_data" "cluster" {
  triggers_replace = [filesha1(local.config), var.gateway_api_version, var.name]
  input            = { name = var.name, kubeconfig = var.kubeconfig }

  provisioner "local-exec" {
    command = <<-EOT
      set -eu
      [ -s "${local.manifest}" ] || curl -fsSL -o "${local.manifest}" \
        https://github.com/kubernetes-sigs/gateway-api/releases/download/${var.gateway_api_version}/standard-install.yaml
      k3d cluster list ${var.name} >/dev/null 2>&1 || k3d cluster create --config "${local.config}" \
        --volume "${local.manifest}:/var/lib/rancher/k3s/server/manifests/gateway-api.yaml@server:*"
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

output "kubeconfig" {
  value      = var.kubeconfig
  depends_on = [terraform_data.cluster]
}
