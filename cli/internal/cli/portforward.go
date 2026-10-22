package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// portForward forwards a free local port to a running pod behind a Service (the sandbox's S3 server, unreachable from
// the workstation) and returns its URL and a function that ends the forward.
func portForward(ctx context.Context, config *rest.Config, kube kubernetes.Interface, namespace, service string, port int) (string, func(), error) {
	svc, err := kube.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return "", nil, err
	}
	pods, err := kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(svc.Spec.Selector).String(), FieldSelector: "status.phase=Running"})
	if err != nil {
		return "", nil, err
	}
	if len(pods.Items) == 0 {
		return "", nil, fmt.Errorf("no running pod behind service %s/%s", namespace, service)
	}
	target := port
	for _, p := range svc.Spec.Ports {
		if int(p.Port) == port && p.TargetPort.IntValue() != 0 {
			target = p.TargetPort.IntValue()
		}
	}
	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return "", nil, err
	}
	url := kube.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pods.Items[0].Name).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, url)
	stop, ready := make(chan struct{}), make(chan struct{})
	forwarder, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", target)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return "", nil, err
	}
	failed := make(chan error, 1)
	go func() { failed <- forwarder.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-failed:
		return "", nil, err
	case <-ctx.Done():
		close(stop)
		return "", nil, ctx.Err()
	}
	ports, err := forwarder.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stop)
		return "", nil, fmt.Errorf("port-forward to %s/%s: no local port", namespace, service)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", ports[0].Local), func() { close(stop) }, nil
}
