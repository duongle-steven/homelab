package test

import (
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// egressCurl probes from a new pod after the network policy controller has had time to apply the namespace's
// policies to it (it does so asynchronously: a pod's first seconds are not yet restricted)
func egressCurl(t *testing.T, namespace, target string) string {
	return curlFrom(t, namespace, "egress-probe", target, "", 20)
}

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

// A namespace not in system/egress's internet table reaches neither the internet nor the nodes' other ports. The
// internet probe uses an address, not a name, so a DNS failure cannot pass for a block
func TestEgressBlocksInternet(t *testing.T) {
	meshSandboxOnly(t)
	waitForEgressPolicies(t, "excalidraw")
	assert.Equal(t, "000", egressCurl(t, "excalidraw", "https://1.1.1.1"), "excalidraw reached the internet")
}

func TestEgressBlocksNodePorts(t *testing.T) {
	meshSandboxOnly(t)
	waitForEgressPolicies(t, "excalidraw")
	node := sandboxAddress(t)
	assert.Equal(t, "000", egressCurl(t, "excalidraw", "https://"+node+":10250/"), "excalidraw reached the kubelet")
	assert.NotEqual(t, "000", egressCurl(t, "excalidraw", "https://kubernetes.default.svc/version"), "excalidraw lost the API server")
}

// A namespace in the table reaches the internet on its ports
func TestEgressAllowsListedNamespaces(t *testing.T) {
	meshSandboxOnly(t)
	waitForEgressPolicies(t, "argocd")
	assert.NotEqual(t, "000", egressCurl(t, "argocd", "https://github.com"), "argocd lost the internet")
}
