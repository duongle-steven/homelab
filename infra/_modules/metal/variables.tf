variable "hosts" {
  type        = any
  description = "The machines, by host name (infra/<env>/metal/hosts.json): mac_address, ip, disk"
}

variable "cluster" {
  type        = any
  description = "The environment's infra/<env>/cluster/config.json: the nodes read init_host, vip and network"
}
