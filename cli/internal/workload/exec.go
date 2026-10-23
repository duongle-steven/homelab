package workload

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// Exec runs commands in a workload's pods, like kubectl exec.
type Exec struct {
	Kube   kubernetes.Interface
	Config *rest.Config
}

// Exec runs command in container of a running, ready pod of w, with stdin (a password goes there, never in command)
// and stdout. Its error ends with the command's last stderr line.
func (x Exec) Exec(ctx context.Context, namespace string, w Ref, container string, command []string, stdin io.Reader, stdout io.Writer) error {
	pod, err := RunningPod(ctx, x.Kube, namespace, w)
	if err != nil {
		return err
	}
	req := x.Kube.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container, Command: command, Stdin: stdin != nil, Stdout: true, Stderr: true,
		}, scheme.ParameterCodec)
	spdy, err := remotecommand.NewSPDYExecutor(x.Config, "POST", req.URL())
	if err != nil {
		return err
	}
	websocket, err := remotecommand.NewWebSocketExecutor(x.Config, "GET", req.URL().String())
	if err != nil {
		return err
	}
	// kubectl's choice: WebSocket, or SPDY where an older API server or a proxy refuses the upgrade (before the command
	// starts, so stdin is not read twice)
	executor, err := remotecommand.NewFallbackExecutor(websocket, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	var stderr bytes.Buffer
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: &stderr}); err != nil {
		return fmt.Errorf("%s in %s/%s container %s: %w%s", command[0], namespace, pod, container, err, lastLine(stderr.String()))
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return ": " + last
	}
	return ""
}

// RunningPod names a running pod of w whose containers are all ready and which is not being deleted.
func RunningPod(ctx context.Context, kube kubernetes.Interface, namespace string, w Ref) (string, error) {
	var selector *metav1.LabelSelector
	switch w.Kind {
	case deployment:
		d, err := kube.AppsV1().Deployments(namespace).Get(ctx, w.Name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		selector = d.Spec.Selector
	case statefulSet:
		s, err := kube.AppsV1().StatefulSets(namespace).Get(ctx, w.Name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		selector = s.Spec.Selector
	default:
		return "", fmt.Errorf("workload %s/%s: kind %q is neither a Deployment nor a StatefulSet", namespace, w.Name, w.Kind)
	}
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return "", err
	}
	if sel.Empty() {
		return "", fmt.Errorf("workload %s/%s has no pod selector", namespace, w.Name)
	}
	pods, err := kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return "", err
	}
	for _, p := range pods.Items {
		if p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning && ready(p) {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("no running, ready pod of %s %s/%s", w.Kind, namespace, w.Name)
}

func ready(p corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
