package backup

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBucketBytesThroughAPortForwardInTheSandbox(t *testing.T) {
	bsl := location("ReadWrite")
	_ = unstructured.SetNestedField(bsl.Object, "velero", "spec", "objectStorage", "bucket")
	c, _ := fakeClient(bsl)
	ctx := context.Background()
	if _, err := c.Kube.CoreV1().Secrets(Namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: "velero-credentials"},
		Data:       map[string][]byte{"cloud": []byte("[default]\naws_access_key_id = velero\naws_secret_access_key = s3cret\nendpoint_url = http://s3.velero:7070\n")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	forwarded := ""
	c.PortForward = func(_ context.Context, namespace, service string, port int) (string, func(), error) {
		forwarded = namespace + "/" + service
		return "http://127.0.0.1:40000", func() {}, nil
	}
	var got Credentials
	c.ListBucket = func(_ context.Context, creds Credentials, bucket string) (int64, error) {
		got = creds
		if bucket != "velero" {
			t.Fatalf("bucket = %q", bucket)
		}
		return 3_000_000_000, nil
	}
	size, err := c.BucketBytes(ctx)
	if err != nil || size != 3_000_000_000 {
		t.Fatalf("size = %d, %v", size, err)
	}
	if forwarded != "velero/s3" || got.Endpoint != "http://127.0.0.1:40000" || got.AccessKey != "velero" || got.SecretKey != "s3cret" {
		t.Fatalf("forwarded %q, credentials %+v", forwarded, got)
	}
}

func TestBucketBytesDirectToR2(t *testing.T) {
	bsl := location("ReadWrite")
	_ = unstructured.SetNestedField(bsl.Object, "homelab-production-backup", "spec", "objectStorage", "bucket")
	c, _ := fakeClient(bsl)
	ctx := context.Background()
	if _, err := c.Kube.CoreV1().Secrets(Namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: "velero-credentials"},
		Data:       map[string][]byte{"cloud": []byte("[default]\naws_access_key_id = a\naws_secret_access_key = b\nendpoint_url = https://account.r2.cloudflarestorage.com\n")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.PortForward = func(context.Context, string, string, int) (string, func(), error) {
		t.Fatal("R2 needs no port-forward")
		return "", nil, nil
	}
	c.ListBucket = func(_ context.Context, creds Credentials, _ string) (int64, error) {
		if creds.Endpoint != "https://account.r2.cloudflarestorage.com" {
			t.Fatalf("endpoint = %q", creds.Endpoint)
		}
		return 7, nil
	}
	if size, err := c.BucketBytes(ctx); err != nil || size != 7 {
		t.Fatalf("size = %d, %v", size, err)
	}
}

// R2's endpoint names the account: an S3 error (DNS, TLS) must not print it
func TestBucketBytesErrorHidesTheEndpoint(t *testing.T) {
	bsl := location("ReadWrite")
	_ = unstructured.SetNestedField(bsl.Object, "homelab-production-backup", "spec", "objectStorage", "bucket")
	c, _ := fakeClient(bsl)
	ctx := context.Background()
	if _, err := c.Kube.CoreV1().Secrets(Namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: "velero-credentials"},
		Data:       map[string][]byte{"cloud": []byte("[default]\naws_access_key_id = a\naws_secret_access_key = b\nendpoint_url = https://0123abcd.r2.cloudflarestorage.com\n")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.ListBucket = func(context.Context, Credentials, string) (int64, error) {
		return 0, errors.New(`list bucket homelab-production-backup: Get "https://0123abcd.r2.cloudflarestorage.com/homelab-production-backup?list-type=2": dial tcp: lookup 0123abcd.r2.cloudflarestorage.com: no such host`)
	}
	_, err := c.BucketBytes(ctx)
	if err == nil || strings.Contains(err.Error(), "0123abcd") || !strings.Contains(err.Error(), "no such host") {
		t.Fatalf("err = %v", err)
	}
}
