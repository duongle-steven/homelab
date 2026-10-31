# Staging's machines: libvirt VMs on a NAT network whose DHCP gives each MAC its fixed address. They boot their disk
# first, so an empty one falls through to the network, where nixie (infra/staging/metal) answers as a proxyDHCP and
# installs them like production's machines; an installed one boots NixOS. virsh destroy <host> is a power cut.
provider "libvirt" {
  uri = "qemu:///system"
}

locals {
  pool_dir = "/var/lib/libvirt/images/homelab-staging"
}

resource "libvirt_network" "staging" {
  name      = "homelab-staging"
  autostart = true
  forward   = { mode = "nat" }
  bridge    = { name = "virbr-staging", stp = "on" }
  ips = [{
    address = var.network.gateway
    prefix  = var.network.prefix_length
    dhcp = {
      hosts = [for name, host in var.hosts : { name = name, mac = host.mac_address, ip = host.ip }]
    }
  }]
}

resource "libvirt_pool" "staging" {
  name   = "homelab-staging"
  type   = "dir"
  target = { path = local.pool_dir }
}

resource "libvirt_volume" "disk" {
  for_each = var.hosts
  name     = "${each.key}.qcow2"
  pool     = libvirt_pool.staging.name
  capacity = var.disk_gib * 1024 * 1024 * 1024
  # Sparse: the file grows as the VM writes (it lives in WSL's virtual disk on C:)
  allocation = 0
  target     = { format = { type = "qcow2" } }
}

resource "libvirt_domain" "node" {
  for_each    = var.hosts
  name        = each.key
  type        = "kvm"
  memory      = var.memory_mib
  memory_unit = "MiB"
  vcpu        = var.vcpus
  running     = true
  autostart   = false
  cpu         = { mode = "host-passthrough" }
  # UEFI (systemd-boot, like the hardware) needs ACPI; ACPI also lets virsh shutdown stop a node cleanly
  features = { acpi = true, apic = {} }
  os = {
    type         = "hvm"
    type_arch    = "x86_64"
    type_machine = "q35"
    firmware     = "efi"
  }
  devices = {
    controllers = [{ type = "scsi", model = "virtio-scsi" }]
    disks = [{
      source = { volume = { pool = libvirt_pool.staging.name, volume = libvirt_volume.disk[each.key].name } }
      # SCSI, so the disk is /dev/sda like the hardware's (disks.nix's default): nixie keeps only the keys it knows
      # when it records an install in hosts.json, so a per-host disk would not survive
      target = { dev = "sda", bus = "scsi" }
      driver = { name = "qemu", type = "qcow2", discard = "unmap" }
      boot   = { order = 1 }
    }]
    interfaces = [{
      mac    = { address = each.value.mac_address }
      model  = { type = "virtio" }
      source = { network = { network = libvirt_network.staging.name } }
      boot   = { order = 2 }
    }]
    # The guest hands the pages it frees back to WSL: without it a VM keeps every page it ever touched (an install's
    # page cache held about 3 GB per VM in WSL's 12 GB while the guests used under 1 GB)
    mem_balloon = { model = "virtio", free_page_reporting = "on" }
    serials     = [{ target = { port = 0 } }]
    consoles    = [{ target = { type = "serial", port = 0 } }]
  }
}
