{ modulesPath, ... }:

{
  imports = [
    (modulesPath + "/installer/netboot/netboot-minimal.nix")
    # On a VM (staging) the installer gives its page cache back too: it writes the whole system to disk from memory
    ./vm-memory.nix
  ];

  installer.cloneConfig = false;

  users.users.root.openssh.authorizedKeys.keyFiles = [ ../authorized_keys ];

  system.stateVersion = "26.05";
}
