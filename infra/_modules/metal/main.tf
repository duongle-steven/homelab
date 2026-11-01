# The node flake's inputs (flake/hosts.json, flake/cluster.json); the unit's hooks install (nixie), enroll and upgrade the nodes
locals {
  # nixie records the installation identity after installing. Exclude it from
  # the flake input to avoid drift; the node IP is required by K3s.
  inventory = {
    for name, host in var.hosts : name => {
      for key, value in host : key => value
      if key != "machine_id_hash"
    }
  }
}

resource "local_file" "hosts" {
  content         = jsonencode(local.inventory)
  filename        = "${path.module}/flake/hosts.json"
  file_permission = "600"
}

resource "local_file" "cluster" {
  content         = jsonencode(var.cluster)
  filename        = "${path.module}/flake/cluster.json"
  file_permission = "600"
}

# The environment's own keys: a staging key is never root on production
resource "local_file" "authorized_keys" {
  content         = var.authorized_keys
  filename        = "${path.module}/flake/authorized_keys"
  file_permission = "600"
}
