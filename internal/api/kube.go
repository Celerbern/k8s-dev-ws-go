package api

import (
	"context"
	"io"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

// KubeClient gives the API server direct Kubernetes access for the few
// things the cached controller-runtime client cannot do: streaming pod logs
// (a subresource request) and listing events without caching the whole
// cluster's event history.
type KubeClient interface {
	// StreamPodLogs opens a log stream for a container in a pod. A
	// tailLines <= 0 streams the full log.
	StreamPodLogs(ctx context.Context, namespace, pod, container string, tailLines int64, follow, previous bool) (io.ReadCloser, error)
	// ListEvents returns all events recorded in a namespace.
	ListEvents(ctx context.Context, namespace string) ([]corev1.Event, error)
}

// clientgoKubeClient implements KubeClient with a client-go clientset
// built from the same rest config as the controller manager.
type clientgoKubeClient struct {
	core corev1client.CoreV1Interface
}

// NewKubeClient builds a KubeClient from a rest config.
func NewKubeClient(cfg *rest.Config) (KubeClient, error) {
	core, err := corev1client.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &clientgoKubeClient{core: core}, nil
}

func (k *clientgoKubeClient) StreamPodLogs(ctx context.Context, namespace, pod, container string, tailLines int64, follow, previous bool) (io.ReadCloser, error) {
	opts := &corev1.PodLogOptions{Container: container, Follow: follow, Previous: previous}
	if tailLines > 0 {
		n := tailLines
		opts.TailLines = &n
	}
	return k.core.Pods(namespace).GetLogs(pod, opts).Stream(ctx)
}

func (k *clientgoKubeClient) ListEvents(ctx context.Context, namespace string) ([]corev1.Event, error) {
	list, err := k.core.Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}
