variable "hosts" {
  type        = any
  description = "The machines, by host name (infra/<env>/metal/hosts.json): mac_address, ip, and optionally disk (otherwise config.json's disk, else /dev/sda; nixie drops it when it records an install)"
}

variable "cluster" {
  type        = any
  description = "The environment's infra/<env>/cluster/config.json: the nodes read init_host, vip and network"
}

variable "authorized_keys" {
  type        = string
  description = "infra/<env>/metal/authorized_keys: the public keys root and admin accept on this environment's nodes"
}
