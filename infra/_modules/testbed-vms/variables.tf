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
  description = "Each VM's memory: 3 x 3.5 GB in WSL's 13 GB (docs/installation/testbed.md)"
}

variable "vcpus" {
  type = number
  # 12a-1's two ran the nodes and rook-ceph. Four since 2026-11-25, measured: with Istio, Argo CD and the mesh's stopgap,
  # k3s took 142% of two vCPUs at 75% CPU pressure and kept losing its leases and restarting, while the workstation's
  # 16 threads ran at a load of 7. Four at most: the workstation (i5-12500H) has 4 P-cores and 8 E-cores, and nested
  # virtualization takes its share. Memory, not CPU, is what WSL's 13 GB limits
  default = 4
}

variable "disk_gib" {
  type        = number
  default     = 100
  description = "Each VM's disk, a sparse raw file: disko's 64G system partition, the rest Ceph's OSD"
}
