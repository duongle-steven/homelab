package test

import (
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitForEgressPolicies waits until Argo CD has synced system/egress into a namespace: these tests run before the
// slower ones, while a fresh sandbox is still syncing
func waitForEgressPolicies(t *testing.T, namespace string) {
	options := k8s.NewKubectlOptions("", "", namespace)
	options.Logger = logger.Discard
	require.Eventually(t, func() bool {
		_, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "networkpolicy", "deny-all-egress")
		return err == nil
	}, 30*time.Minute, 10*time.Second, "no egress policies in %s", namespace)
}

// A namespace not in system/egress's internet table reaches neither the internet nor the nodes' other ports
func TestEgressBlocksInternet(t *testing.T) {
	meshSandboxOnly(t)
	waitForEgressPolicies(t, "excalidraw")
	assert.Equal(t, "000", meshCurlIn(t, "excalidraw", "egress-probe", "https://github.com", ""), "excalidraw reached the internet")
}

func TestEgressBlocksNodePorts(t *testing.T) {
	meshSandboxOnly(t)
	waitForEgressPolicies(t, "excalidraw")
	node := sandboxAddress(t)
	assert.Equal(t, "000", meshCurlIn(t, "excalidraw", "egress-probe", "https://"+node+":10250/", ""), "excalidraw reached the kubelet")
	assert.NotEqual(t, "000", meshCurlIn(t, "excalidraw", "egress-probe", "https://kubernetes.default.svc/version", ""), "excalidraw lost the API server")
}

// A namespace in the table reaches the internet on its ports
func TestEgressAllowsListedNamespaces(t *testing.T) {
	meshSandboxOnly(t)
	waitForEgressPolicies(t, "argocd")
	assert.NotEqual(t, "000", meshCurlIn(t, "argocd", "egress-probe", "https://github.com", ""), "argocd lost the internet")
}
