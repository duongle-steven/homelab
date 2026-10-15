package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A namespace not in system/egress's internet table reaches neither the internet nor the nodes' other ports
func TestEgressBlocksInternet(t *testing.T) {
	meshSandboxOnly(t)
	assert.Equal(t, "000", meshCurlIn(t, "excalidraw", "egress-probe", "https://github.com", ""), "excalidraw reached the internet")
}

func TestEgressBlocksNodePorts(t *testing.T) {
	meshSandboxOnly(t)
	node := sandboxAddress(t)
	assert.Equal(t, "000", meshCurlIn(t, "excalidraw", "egress-probe", "https://"+node+":10250/", ""), "excalidraw reached the kubelet")
	assert.NotEqual(t, "000", meshCurlIn(t, "excalidraw", "egress-probe", "https://kubernetes.default.svc/version", ""), "excalidraw lost the API server")
}

// A namespace in the table reaches the internet on its ports
func TestEgressAllowsListedNamespaces(t *testing.T) {
	meshSandboxOnly(t)
	assert.NotEqual(t, "000", meshCurlIn(t, "argocd", "egress-probe", "https://github.com", ""), "argocd lost the internet")
}
