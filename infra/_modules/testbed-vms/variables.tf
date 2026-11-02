variable "hosts" {
  type        = map(object({ mac_address = string, ip = string }))
  description = "The machines (infra/testbed/metal/hosts.json): one VM each, its MAC and its fixed DHCP address"
}

variable "network" {
  type        = object({ prefix_length = number, gateway = string })
  description = "config.json's network: the libvirt network's own address is the nodes' gateway"
}

variable "memory_mib" {
  type        = number
  default     = 3584
  description = "Each VM's memory: 3 x 3.5 GB in WSL's 12 GB (docs/superpowers/specs/2026-10-31-testbed-design.md)"
}

variable "vcpus" {
  type = number
  # 12a-1's two ran the nodes and rook-ceph. More only with a measurement showing CPU is the limit, four at most: the
  # workstation (i5-12500H) has 4 P-cores and 8 E-cores, and nested virtualization takes its share
  default = 2
}

variable "disk_gib" {
  type        = number
  default     = 100
  description = "Each VM's disk, a sparse qcow2 file: disko's 64G system partition, the rest Ceph's OSD"
}
