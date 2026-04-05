// Package framework provides helpers for E2E tests.
package framework

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

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

	"github.com/telekom/das-schiff-network-operator/e2etests/config"
)

// Global is the shared framework instance, set during BeforeSuite.
var Global *Framework

// Framework provides test helpers backed by a real cluster connection.
type Framework struct {
	Config     *config.Config
	KubeClient kubernetes.Interface
	Client     client.Client

	// Cluster-2 (gateway cluster) clients — initialized when Cluster2Kubeconfig is set.
	Cluster2KubeClient  kubernetes.Interface
	cluster2Client      client.Client

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
	f.cluster2Client, err = client.New(restCfg, client.Options{})
	if err != nil {
		return fmt.Errorf("create cluster2 controller-runtime client: %w", err)
	}
	return nil
}

// Cluster2Client returns the controller-runtime client for cluster-2.
func (f *Framework) Cluster2Client() client.Client {
	return f.cluster2Client
}

// ObjectKey returns a client.ObjectKey for the given namespace and name.
func ObjectKey(namespace, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: name}
}

// CreateNamespace creates a namespace and tracks it for cleanup.
func (f *Framework) CreateNamespace(ctx context.Context, name string) error {
	// If namespace is terminating from a previous test, wait for it to be gone.
	for i := 0; i < 60; i++ {
		existing := &corev1.Namespace{}
		if err := f.Client.Get(ctx, types.NamespacedName{Name: name}, existing); err != nil {
			if apierrors.IsNotFound(err) {
				break
			}
			return err
		}
		if existing.Status.Phase == corev1.NamespaceTerminating {
			time.Sleep(1 * time.Second)
			continue
		}
		// Namespace exists and is active — reuse it.
		f.testNamespaces = append(f.testNamespaces, name)
		return nil
	}

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

// DeleteNamespace deletes a namespace. Subsequent CreateNamespace calls for the
// same name will wait for termination to complete before recreating.
func (f *Framework) DeleteNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	return client.IgnoreNotFound(f.Client.Delete(ctx, ns))
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

	// Retry on conflict (operator may update resource version concurrently)
	// and on transient webhook failures (connection reset after operator restart).
	for attempt := 0; attempt < 10; attempt++ {
		existing := obj.DeepCopy()
		if err := f.Client.Get(ctx, key, existing); err == nil {
			obj.SetResourceVersion(existing.GetResourceVersion())
			err = f.Client.Update(ctx, obj)
			if apierrors.IsConflict(err) || isWebhookTransient(err) {
				time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
				continue
			}
			return err
		} else if !apierrors.IsNotFound(err) {
			if isWebhookTransient(err) {
				time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
				continue
			}
			return fmt.Errorf("get %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}
		err = f.Client.Create(ctx, obj)
		if isWebhookTransient(err) {
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
			continue
		}
		return err
	}
	return fmt.Errorf("failed to apply %s/%s after retries", obj.GetKind(), obj.GetName())
}

// isWebhookTransient returns true for transient webhook errors (connection reset,
// refused, EOF) that occur briefly after an operator restart.
func isWebhookTransient(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsInternalError(err) {
		msg := err.Error()
		return strings.Contains(msg, "connection reset by peer") ||
			strings.Contains(msg, "connection refused") ||
			strings.Contains(msg, "EOF") ||
			strings.Contains(msg, "webhook")
	}
	return false
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

// DynamicGet fetches an unstructured object by namespace and name.
// The caller must set the GVK on obj before calling.
func (f *Framework) DynamicGet(ctx context.Context, namespace, name string, obj *unstructured.Unstructured) error {
	return f.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj)
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

// WaitForDeploymentReady waits for a Deployment to have all replicas available.
func (f *Framework) WaitForDeploymentReady(namespace, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return Poll(ctx, 5*time.Second, func() (bool, error) {
		deploy, err := f.KubeClient.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if deploy.Spec.Replicas == nil {
			return false, nil
		}
		return deploy.Status.AvailableReplicas == *deploy.Spec.Replicas &&
			deploy.Status.UpdatedReplicas == *deploy.Spec.Replicas, nil
	})
}
