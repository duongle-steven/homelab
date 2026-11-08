# From its release: nixos-26.05 ships 1.51.0 and nixos-unstable 1.58.0 (the justfiles' runner). A static musl binary
{ lib, stdenv, fetchurl }:

let
  version = "1.59.0";
  assets = {
    x86_64-linux = { arch = "x86_64"; hash = "sha256-QJBEYXtvBlF8EDXlXKOMjIVv+LZQ5BvCqh74LVkDoj4="; };
    aarch64-linux = { arch = "aarch64"; hash = "sha256-I4cbFwK/1VD7X5AXeP1CkOzy7gNKRaiJ++ltibGICG0="; };
  };
  asset = assets.${stdenv.hostPlatform.system};
in
stdenv.mkDerivation {
  pname = "just";
  inherit version;

  src = fetchurl {
    url = "https://github.com/casey/just/releases/download/${version}/just-${version}-${asset.arch}-unknown-linux-musl.tar.gz";
    inherit (asset) hash;
  };

  sourceRoot = ".";
  dontBuild = true;
  dontStrip = true;

  installPhase = ''
    install -Dm755 just $out/bin/just
  '';

  meta.platforms = lib.attrNames assets;
}
