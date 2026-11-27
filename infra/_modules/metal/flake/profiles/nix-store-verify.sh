#!/usr/bin/env bash
# The node's weekly store check (G11, docs/superpowers/specs/2026-11-27-node-patches-design.md): every path in the Nix
# store checked against its recorded hash, the result written for node-exporter's textfile collector, where
# NodeStoreVerifyFailed and NodeStoreVerifyMissing read it. The output stays in the journal: it names what changed.
# Usage: nix-store-verify [directory], /var/lib/node-exporter by default
set -euo pipefail
dir=${1:-/var/lib/node-exporter}
mkdir -p "$dir"
log=$(mktemp)
trap 'rm -f "$log"' EXIT
success=1
nix-store --verify --check-contents 2>&1 | tee "$log" || success=0
# Some Nix versions report a modified path and still exit 0
if grep -q 'was modified!' "$log"; then
	success=0
fi
# Written beside the final file, then renamed: a run cut off leaves no half-written result that reads as a success
tmp=$(mktemp "$dir/.nix-store-verify.XXXXXX")
cat > "$tmp" <<PROM
# HELP node_nix_store_verify_success Whether the last nix-store --verify --check-contents found the store intact.
# TYPE node_nix_store_verify_success gauge
node_nix_store_verify_success $success
# HELP node_nix_store_verify_last_run_seconds When the last store check ended.
# TYPE node_nix_store_verify_last_run_seconds gauge
node_nix_store_verify_last_run_seconds $(date +%s)
PROM
chmod 0644 "$tmp"
mv "$tmp" "$dir/nix-store-verify.prom"
[ "$success" = 1 ]
