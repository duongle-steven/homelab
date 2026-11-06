# From its release: nixos-26.05 ships 4.0.8 and nixos-unstable 4.0.16 (make dev-scan). A static Go binary
{ lib, stdenv, fetchurl }:

let
  version = "4.0.17";
  assets = {
    x86_64-linux = { arch = "amd64"; hash = "sha256-0HDdJ/r3KCzor04wJjOFpR/k7juln0bVf28wwLPMs0I="; };
    aarch64-linux = { arch = "arm64"; hash = "sha256-LnpIx6BXfJ1OyB6M3wFJ+7Z/LitkkWX3eh2pu3LzD6c="; };
  };
  asset = assets.${stdenv.hostPlatform.system};
in
stdenv.mkDerivation {
  pname = "kubescape";
  inherit version;

  src = fetchurl {
    url = "https://github.com/kubescape/kubescape/releases/download/v${version}/kubescape_${version}_linux_${asset.arch}";
    inherit (asset) hash;
  };

  dontUnpack = true;
  dontBuild = true;
  dontStrip = true;

  installPhase = ''
    install -Dm755 $src $out/bin/kubescape
  '';

  meta.platforms = lib.attrNames assets;
}
