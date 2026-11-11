{ hostConfig, defaultDisk, ... }:

{
  disko.devices.disk.main = {
    type = "disk";
    device =
      if hostConfig ? disk then
        hostConfig.disk
      else if defaultDisk != null then
        defaultDisk
      else
        "/dev/sda";
    content = {
      type = "gpt";
      partitions = {
        ESP = {
          priority = 1;
          size = "1G";
          type = "EF00";
          content = {
            type = "filesystem";
            format = "vfat";
            mountpoint = "/boot";
            mountOptions = [ "umask=0077" ];
          };
        };
        root = {
          priority = 2;
          size = "64G";
          content = {
            type = "filesystem";
            format = "ext4";
            mountpoint = "/";
          };
        };
        data = {
          priority = 3;
          size = "100%";
          label = "rook-data";
          content = null;
        };
      };
    };
  };
}
