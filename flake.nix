{
  description = "Homelab";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, nixpkgs-unstable, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        # Only for tools whose stable release is newer than nixos-26.05 ships
        unstable = import nixpkgs-unstable { inherit system; };
      in
      with pkgs;
      {
        devShells.default = mkShell {
          packages = [
            chromium
            diffutils
            docker
            dyff
            git
            glibcLocales
            go
            gotestsum
            jq
            k9s
            kanidm_1_11
            kubectl
            kubernetes-helm
            openssh_gssapi
            unstable.opentofu # Drop-in replacement for Terraform (nixos-26.05 ships 1.11)
            unstable.terragrunt
            qrencode
            shellcheck
            unstable.k3d
            (callPackage ./nix/cloudflare-cf.nix { }) # cf: Cloudflare's CLI, its OAuth login gives OpenTofu its token
            wireguard-tools
            yamllint
            yq-go

            (python3.withPackages (p: with p; [
              mkdocs-material
              pyyaml # scripts/check-*
            ]))
          ];
        };

        # metal/ (Ansible, the PXE server) until step 12 replaces it: nix develop .#metal
        devShells.metal = mkShell {
          inputsFrom = [ self.devShells.${system}.default ];
          packages = [
            ansible
            ansible-lint
            docker-compose
            libisoburn
            python3Packages.netaddr # Ansible's ansible.netcommon.ipmath (metal/roles/pxe_server)
          ];
        };
      }
    );
}
