// Package k8s is the Kubernetes integration layer. It wraps client-go with a
// typed clientset, a dynamic client (for arbitrary CRDs the agent wasn't
// compiled against), discovery, and metrics. Reads go through here; writes go
// through here too but only the tools package calls the write helpers, so every
// mutation inherits the policy/approval/audit chain.
package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Cluster is a connected handle to a single Kubernetes cluster.
type Cluster struct {
	Name    string
	Context string
	typed   kubernetes.Interface
	dynamic dynamic.Interface
	metrics metricsclient.Interface
	restCfg *rest.Config
}

// Connect builds a Cluster from a kubeconfig context. An empty context uses the
// current-context. When running in-cluster, pass inCluster=true to use the
// mounted ServiceAccount instead.
func Connect(name, kubeContext string, inCluster bool) (*Cluster, error) {
	var cfg *rest.Config
	var err error
	if inCluster {
		cfg, err = rest.InClusterConfig()
	} else {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
		).ClientConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("build rest config: %w", err)
	}
	// Defensive client-side throttling so the agent never overwhelms an API server.
	cfg.QPS, cfg.Burst = 50, 100

	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("typed client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	mc, err := metricsclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metrics client: %w", err)
	}

	if name == "" {
		name = kubeContext
	}
	return &Cluster{
		Name: name, Context: kubeContext,
		typed: typed, dynamic: dyn, metrics: mc, restCfg: cfg,
	}, nil
}

// Typed exposes the typed clientset for core/apps/batch reads.
func (c *Cluster) Typed() kubernetes.Interface { return c.typed }

// Dynamic exposes the dynamic client for arbitrary GVRs (CRDs).
func (c *Cluster) Dynamic() dynamic.Interface { return c.dynamic }

// Metrics exposes metrics.k8s.io for live CPU/memory.
func (c *Cluster) Metrics() metricsclient.Interface { return c.metrics }

// Health performs a cheap liveness probe against the API server.
func (c *Cluster) Health(ctx context.Context) error {
	_, err := c.typed.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
	return err
}

// CanI runs a SelfSubjectAccessReview so the agent never assumes a permission it
// lacks. Tools call this before any mutation and report the exact missing verb.
func (c *Cluster) CanI(ctx context.Context, verb, group, resource, namespace string) (bool, error) {
	review := &authReview{verb: verb, group: group, resource: resource, namespace: namespace}
	return review.run(ctx, c.typed)
}

// ListPods is a convenience read used by context cards and audit rules.
func (c *Cluster) ListPods(ctx context.Context, namespace string) ([]corev1.Pod, error) {
	list, err := c.typed.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// GVR is a small helper for constructing dynamic resource references.
func GVR(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
}
