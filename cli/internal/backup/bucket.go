package backup

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// BucketBytes is what location default's bucket holds, listed with Velero's own credentials. A cluster-local
// endpoint (the sandbox's S3 server) is reached through a port-forward to its Service.
func (c *Client) BucketBytes(ctx context.Context) (int64, error) {
	bsl, err := c.Dyn.Resource(StorageLocations).Namespace(Namespace).Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("backupstoragelocations.velero.io default: %w", err)
	}
	bucket, _, _ := unstructured.NestedString(bsl.Object, "spec", "objectStorage", "bucket")
	secret, err := c.Kube.CoreV1().Secrets(Namespace).Get(ctx, "velero-credentials", metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("secret velero-credentials: %w", err)
	}
	creds, err := parseCredentials(string(secret.Data["cloud"]))
	if err != nil {
		return 0, err
	}
	if creds.Region, _, _ = unstructured.NestedString(bsl.Object, "spec", "config", "region"); creds.Region == "" {
		creds.Region = "auto"
	}
	endpoint, err := url.Parse(creds.Endpoint)
	if err != nil {
		return 0, fmt.Errorf("velero-credentials' endpoint_url is not a URL")
	}
	if service, namespace, ok := clusterService(endpoint.Hostname()); ok {
		port, _ := strconv.Atoi(endpoint.Port())
		local, stop, err := c.PortForward(ctx, namespace, service, port)
		if err != nil {
			return 0, fmt.Errorf("port-forward to %s/%s: %w", namespace, service, err)
		}
		defer stop()
		creds.Endpoint = local
	}
	size, err := c.ListBucket(ctx, creds, bucket)
	if err != nil && endpoint.Hostname() != "" {
		// R2's host names the account: the error keeps everything else
		return 0, errors.New(strings.ReplaceAll(err.Error(), endpoint.Hostname(), "<endpoint_url>"))
	}
	return size, err
}

// clusterService splits a Service's in-cluster name (<service>.<namespace>, optionally .svc or .svc.cluster.local)
func clusterService(host string) (service, namespace string, ok bool) {
	host = strings.TrimSuffix(strings.TrimSuffix(host, ".cluster.local"), ".svc")
	parts := strings.Split(host, ".")
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}
