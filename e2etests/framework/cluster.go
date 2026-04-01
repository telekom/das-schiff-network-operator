// Package framework provides helpers for E2E tests.
package framework

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/telekom/das-schiff-network-operator/e2etests/config"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Global is the shared framework instance, set during BeforeSuite.
var Global *Framework

// Framework provides test helpers backed by a real cluster connection.
type Framework struct {
	Config     *config.Config
	KubeClient kubernetes.Interface
	Client     client.Client

	// Cluster-2 (gateway cluster) clients — initialized when Cluster2Kubeconfig is set.
	Cluster2KubeClient kubernetes.Interface

	// Track namespaces created during tests for cleanup.
	testNamespaces []string
}

// New creates a new Framework from the given config.
func New(cfg *config.Config) (*Framework, error) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("build kubeconfig: %w", err)
	}

	kubeClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	// Use a plain unstructured client so we don't need to register every CRD scheme.
	c, err := client.New(restCfg, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("create controller-runtime client: %w", err)
	}

	return &Framework{
		Config:     cfg,
		KubeClient: kubeClient,
		Client:     c,
	}, nil
}

// InitCluster2 initializes the cluster-2 kubernetes client.
func (f *Framework) InitCluster2() error {
	restCfg, err := clientcmd.BuildConfigFromFlags("", f.Config.Cluster2Kubeconfig)
	if err != nil {
		return fmt.Errorf("build cluster2 kubeconfig: %w", err)
	}
	f.Cluster2KubeClient, err = kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("create cluster2 kubernetes client: %w", err)
	}
	return nil
}

// CreateNamespace creates a namespace and tracks it for cleanup.
func (f *Framework) CreateNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	if err := f.Client.Create(ctx, ns); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	f.testNamespaces = append(f.testNamespaces, name)
	return nil
}

// CleanupTestNamespaces deletes all namespaces created during the test run.
func (f *Framework) CleanupTestNamespaces() {
	ctx := context.Background()
	for _, ns := range f.testNamespaces {
		_ = f.Client.Delete(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})
	}
}

// PatchWebhookFailurePolicy sets all webhooks in the named ValidatingWebhookConfiguration
// to the given failure policy (e.g., Ignore) so that unreachable webhooks don't block e2e tests.
func (f *Framework) PatchWebhookFailurePolicy(ctx context.Context, name string, policy admissionregistrationv1.FailurePolicyType) error {
	whc := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	if err := f.Client.Get(ctx, types.NamespacedName{Name: name}, whc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	for i := range whc.Webhooks {
		whc.Webhooks[i].FailurePolicy = &policy
	}
	return f.Client.Update(ctx, whc)
}

// ApplyManifest applies a YAML manifest (supports multi-document) to the cluster.
func (f *Framework) ApplyManifest(ctx context.Context, yamlData []byte) error {
	return f.ApplyManifestInNamespace(ctx, yamlData, "")
}

// ApplyManifestInNamespace applies a YAML manifest with a namespace override.
// If ns is non-empty, it overrides metadata.namespace on each object.
// Supports multi-document YAML (separated by ---).
func (f *Framework) ApplyManifestInNamespace(ctx context.Context, yamlData []byte, ns string) error {
	docs := splitYAMLDocuments(yamlData)
	for _, doc := range docs {
		if err := f.applySingleObject(ctx, doc, ns); err != nil {
			return err
		}
	}
	return nil
}

func (f *Framework) applySingleObject(ctx context.Context, yamlData []byte, ns string) error {
	obj := &unstructured.Unstructured{}
	dec := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	_, _, err := dec.Decode(yamlData, nil, obj)
	if err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}

	if ns != "" {
		obj.SetNamespace(ns)
	}

	key := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	// Retry on conflict (operator may update resource version concurrently).
	for attempt := 0; attempt < 5; attempt++ {
		existing := obj.DeepCopy()
		if err := f.Client.Get(ctx, key, existing); err == nil {
			obj.SetResourceVersion(existing.GetResourceVersion())
			err = f.Client.Update(ctx, obj)
			if apierrors.IsConflict(err) {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			return err
		}
		return f.Client.Create(ctx, obj)
	}
	return fmt.Errorf("failed to apply %s/%s after retries: conflict", obj.GetKind(), obj.GetName())
}

// splitYAMLDocuments splits multi-document YAML into individual documents.
func splitYAMLDocuments(data []byte) [][]byte {
	docs := bytes.Split(data, []byte("\n---"))
	var result [][]byte
	for _, doc := range docs {
		trimmed := bytes.TrimSpace(doc)
		if len(trimmed) == 0 || string(trimmed) == "---" {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}

// DeleteManifest deletes a YAML manifest (supports multi-document) from the cluster.
func (f *Framework) DeleteManifest(ctx context.Context, yamlData []byte) error {
	return f.DeleteManifestInNamespace(ctx, yamlData, "")
}

// DeleteManifestInNamespace deletes a YAML manifest with a namespace override.
// If ns is non-empty, it overrides metadata.namespace on each object before deletion.
// Deletes in reverse order to respect dependency ordering (dependents before parents).
func (f *Framework) DeleteManifestInNamespace(ctx context.Context, yamlData []byte, ns string) error {
	docs := splitYAMLDocuments(yamlData)
	// Delete in reverse order so dependents are removed before their references.
	for i := len(docs) - 1; i >= 0; i-- {
		obj := &unstructured.Unstructured{}
		dec := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
		if _, _, err := dec.Decode(docs[i], nil, obj); err != nil {
			return fmt.Errorf("decode manifest: %w", err)
		}
		if ns != "" {
			obj.SetNamespace(ns)
		}
		if err := client.IgnoreNotFound(f.Client.Delete(ctx, obj)); err != nil {
			return err
		}
	}
	return nil
}

// WaitForNodesReady waits for all nodes to report Ready.
func (f *Framework) WaitForNodesReady(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return Poll(ctx, 5*time.Second, func() (bool, error) {
		nodes, err := f.KubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, nil
		}
		if len(nodes.Items) == 0 {
			return false, nil
		}
		for i := range nodes.Items {
			node := &nodes.Items[i]
			ready := false
			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					ready = true
					break
				}
			}
			if !ready {
				return false, nil
			}
		}
		return true, nil
	})
}

// WaitForDaemonSetReady waits for a DaemonSet to have all pods ready.
func (f *Framework) WaitForDaemonSetReady(namespace, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return Poll(ctx, 5*time.Second, func() (bool, error) {
		ds, err := f.KubeClient.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return ds.Status.DesiredNumberScheduled > 0 &&
			ds.Status.DesiredNumberScheduled == ds.Status.NumberReady, nil
	})
}
