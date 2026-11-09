// Package kube runs kubectl for the e2e suite, every call bound to the spec's context so a spec timeout stops it.
// KUBECONFIG comes from the environment (just test e2e sets it).
package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Kubectl runs kubectl with args in namespace ("" adds no --namespace) and returns stdout and stderr together,
// trailing newlines trimmed; an error carries that output too.
func Kubectl(ctx context.Context, namespace string, args ...string) (string, error) {
	return run(ctx, namespace, "", args...)
}

// GetJSON runs kubectl get args --output json and decodes its stdout into out (stderr's warnings ignored).
func GetJSON(ctx context.Context, namespace string, out any, args ...string) error {
	cmd := command(ctx, namespace, append(append([]string{"get"}, args...), "--output", "json")...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl get %s: %w: %s", strings.Join(args, " "), stop(ctx, err), strings.TrimSpace(stderr.String()))
	}
	return json.Unmarshal(stdout.Bytes(), out)
}

// Apply applies manifest in namespace.
func Apply(ctx context.Context, namespace, manifest string) error {
	_, err := run(ctx, namespace, manifest, "apply", "-f", "-")
	return err
}

// Delete deletes what manifest names in namespace.
func Delete(ctx context.Context, namespace, manifest string) error {
	_, err := run(ctx, namespace, manifest, "delete", "-f", "-")
	return err
}

// CreateNamespace creates a namespace.
func CreateNamespace(ctx context.Context, name string) error {
	_, err := run(ctx, "", "", "create", "namespace", name)
	return err
}

// DeleteNamespace deletes a namespace without waiting for it to go, as terratest did: a namespace holding volumes can
// take longer than a cleanup's timeout, and a caller that needs it gone waits itself
func DeleteNamespace(ctx context.Context, name string) error {
	_, err := run(ctx, "", "", "delete", "namespace", name, "--wait=false")
	return err
}

func run(ctx context.Context, namespace, stdin string, args ...string) (string, error) {
	cmd := command(ctx, namespace, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	output := strings.TrimRight(out.String(), "\n")
	if err != nil {
		return output, fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), stop(ctx, err), output)
	}
	return output, nil
}

func command(ctx context.Context, namespace string, args ...string) *exec.Cmd {
	if namespace != "" {
		args = append([]string{"--namespace", namespace}, args...)
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	// A child kubectl left holding the pipes would keep Wait past the context
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// stop names the context's error when it ended the command, so callers can tell a timeout from a kubectl failure.
func stop(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w (%v)", ctx.Err(), err)
	}
	return err
}
