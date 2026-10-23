// Package secrets reads and writes OpenBao's KV store as an admin, through the Kubernetes API server's service proxy.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"

	bao "github.com/openbao/openbao/api/v2"
	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Store is a logged-in OpenBao client for KV v2 mount secret/.
type Store struct {
	client *bao.Client
	kv     *bao.KVv2
}

// Open logs in to OpenBao as service account openbao/openbao-admin with the role admin (OpenBao has no root
// token), reaching it through the API server's proxy for service openbao.
func Open(ctx context.Context, config *rest.Config) (*Store, error) {
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, err
	}
	address := kube.CoreV1().RESTClient().Get().Namespace("openbao").Resource("services").Name("http:openbao:8200").SubResource("proxy").URL().String()
	return open(ctx, kube, address, httpClient)
}

func open(ctx context.Context, kube kubernetes.Interface, address string, httpClient *http.Client) (*Store, error) {
	expiry := int64(600)
	request, err := kube.CoreV1().ServiceAccounts("openbao").CreateToken(ctx, "openbao-admin", &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{Audiences: []string{"openbao"}, ExpirationSeconds: &expiry},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("token for openbao/openbao-admin: %w", err)
	}
	config := bao.DefaultConfig()
	config.Address = address
	config.HttpClient = httpClient
	client, err := bao.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("OpenBao client: %w", err)
	}
	// Never a token from the environment (BAO_TOKEN): only the admin login below
	client.ClearToken()
	login, err := client.Logical().WriteWithContext(ctx, "auth/kubernetes/login", map[string]any{"role": "admin", "jwt": request.Status.Token})
	if err != nil {
		return nil, fmt.Errorf("log in to OpenBao with the role admin failed")
	}
	if login == nil || login.Auth == nil || login.Auth.ClientToken == "" {
		return nil, fmt.Errorf("OpenBao returned no token for the role admin")
	}
	client.SetToken(login.Auth.ClientToken)
	return &Store{client: client, kv: client.KVv2("secret")}, nil
}

// Read returns the record at secret/<path>, or nil when there is none.
func (s *Store) Read(ctx context.Context, path string) (map[string]string, error) {
	record, err := s.kv.Get(ctx, path)
	if errors.Is(err, bao.ErrSecretNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read OpenBao secret/%s failed", path)
	}
	data := make(map[string]string, len(record.Data))
	for key, value := range record.Data {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("OpenBao secret/%s has a non-string field %s", path, key)
		}
		data[key] = text
	}
	return data, nil
}

// Write stores data at secret/<path> unless it is already there.
func (s *Store) Write(ctx context.Context, path string, data map[string]string) error {
	existing, err := s.Read(ctx, path)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(existing, data) {
		return nil
	}
	fields := make(map[string]any, len(data))
	for key, value := range data {
		fields[key] = value
	}
	if _, err := s.kv.Put(ctx, path, fields); err != nil {
		return fmt.Errorf("write OpenBao secret/%s failed", path)
	}
	return nil
}

// Metadata returns the custom metadata of secret/<path>, or nil when the path has no metadata.
func (s *Store) Metadata(ctx context.Context, path string) (map[string]string, error) {
	meta, err := s.kv.GetMetadata(ctx, path)
	if errors.Is(err, bao.ErrSecretNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the metadata of OpenBao secret/%s failed", path)
	}
	custom := make(map[string]string, len(meta.CustomMetadata))
	for key, value := range meta.CustomMetadata {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("OpenBao secret/%s has a non-string custom metadata key %s", path, key)
		}
		custom[key] = text
	}
	return custom, nil
}

// SetMetadata merges set into the custom metadata of secret/<path>; an empty value removes its key. The other keys
// (ESO's managed-by) and the path's settings stay. It reads and writes the whole metadata, so it needs only the
// read and update capabilities, not patch.
func (s *Store) SetMetadata(ctx context.Context, path string, set map[string]string) error {
	input := bao.KVMetadataPutInput{CustomMetadata: map[string]any{}}
	meta, err := s.kv.GetMetadata(ctx, path)
	switch {
	case errors.Is(err, bao.ErrSecretNotFound):
	case err != nil:
		return fmt.Errorf("read the metadata of OpenBao secret/%s failed", path)
	default:
		input = bao.KVMetadataPutInput{
			CASRequired:        meta.CASRequired,
			CustomMetadata:     meta.CustomMetadata,
			DeleteVersionAfter: meta.DeleteVersionAfter,
			MaxVersions:        meta.MaxVersions,
		}
		if input.CustomMetadata == nil {
			input.CustomMetadata = map[string]any{}
		}
	}
	for key, value := range set {
		if value == "" {
			delete(input.CustomMetadata, key)
		} else {
			input.CustomMetadata[key] = value
		}
	}
	if err := s.kv.PutMetadata(ctx, path, input); err != nil {
		return fmt.Errorf("write the metadata of OpenBao secret/%s failed", path)
	}
	return nil
}

// Close revokes the admin token.
func (s *Store) Close(ctx context.Context) error {
	if err := s.client.Auth().Token().RevokeSelfWithContext(ctx, ""); err != nil {
		return fmt.Errorf("revoke the OpenBao admin token failed")
	}
	return nil
}
