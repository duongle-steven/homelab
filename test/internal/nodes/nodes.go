// Package nodes holds what the node specs run on the nodes themselves, so the offline tests can run it too
package nodes

// K3sNameUIDs prints the UID of every process whose comm is exactly k3s, one per line (NodeMeshAgents, the stopgap
// k3s-name-for-istio: docs/superpowers/specs/2026-11-25-istio-cni-k3s-name-design.md). An if, not &&: a for loop ends
// with its last command's status, and a false test there would fail the script
const K3sNameUIDs = `for p in /proc/[0-9]*; do if [ "$(cat $p/comm 2>/dev/null)" = k3s ]; then awk '/^Uid/{print $2}' $p/status; fi; done`
