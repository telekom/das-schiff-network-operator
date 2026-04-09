package sync

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func newCapiCluster(namespace string) *unstructured.Unstructured {
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(capiClusterGVK)
	cluster.SetName("test-cluster")
	cluster.SetNamespace(namespace)
	return cluster
}

func newClusterController(objs ...client.Object) *ClusterController {
	s := testScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		Build()

	return &ClusterController{
		Client:  fakeClient,
		Log:     zap.New(zap.UseDevMode(true)),
		Remotes: NewRemoteClientManager(s),
	}
}

func addRemoteClientForTest(c *ClusterController, key types.NamespacedName, remoteClient client.Client) {
	c.Remotes.mu.Lock()
	defer c.Remotes.mu.Unlock()
	if c.Remotes.clients == nil {
		c.Remotes.clients = make(map[types.NamespacedName]client.Client)
	}
	c.Remotes.clients[key] = remoteClient
}

func TestClusterReconcile_ClusterNotFound(t *testing.T) {
	c := newClusterController()
	// Pre-populate a remote client to verify Remove is called.
	addRemoteClientForTest(c, types.NamespacedName{Namespace: "test-ns", Name: "test-cluster"}, fake.NewClientBuilder().WithScheme(testScheme()).Build())

	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-cluster"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got RequeueAfter=%v", result.RequeueAfter)
	}
	if c.Remotes.Has(types.NamespacedName{Namespace: "test-ns", Name: "test-cluster"}) {
		t.Error("expected remote client to be removed")
	}
}

func TestClusterReconcile_SecretNotFound(t *testing.T) {
	cluster := newCapiCluster("test-ns")
	c := newClusterController(cluster)

	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-cluster"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter=30s, got %v", result.RequeueAfter)
	}
}

func TestClusterReconcile_SecretMissingValueKey(t *testing.T) {
	cluster := newCapiCluster("test-ns")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-kubeconfig",
			Namespace: "test-ns",
		},
		Data: map[string][]byte{
			"other-key": []byte("irrelevant"),
		},
	}
	c := newClusterController(cluster, secret)

	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-cluster"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter=30s, got %v", result.RequeueAfter)
	}
}

func TestClusterReconcile_SecretEmptyValue(t *testing.T) {
	cluster := newCapiCluster("test-ns")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-kubeconfig",
			Namespace: "test-ns",
		},
		Data: map[string][]byte{
			"value": {},
		},
	}
	c := newClusterController(cluster, secret)

	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-cluster"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter=30s, got %v", result.RequeueAfter)
	}
}

func TestClusterReconcile_InvalidKubeconfig(t *testing.T) {
	cluster := newCapiCluster("test-ns")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-kubeconfig",
			Namespace: "test-ns",
		},
		Data: map[string][]byte{
			"value": []byte("not-a-valid-kubeconfig"),
		},
	}
	c := newClusterController(cluster, secret)

	_, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-cluster"},
	})
	if err == nil {
		t.Fatal("expected error for invalid kubeconfig, got nil")
	}
	if !strings.Contains(err.Error(), "updating remote client") {
		t.Errorf("expected error about updating remote client, got: %v", err)
	}
}

func TestClusterReconcile_Success(t *testing.T) {
	cluster := newCapiCluster("test-ns")
	// Minimal valid kubeconfig that clientcmd.RESTConfigFromKubeConfig can parse.
	kubeconfig := []byte(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://127.0.0.1:6443
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user:
    token: fake-token
`)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-kubeconfig",
			Namespace: "test-ns",
		},
		Data: map[string][]byte{
			"value": kubeconfig,
		},
	}
	c := newClusterController(cluster, secret)

	result, err := c.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-cluster"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got RequeueAfter=%v", result.RequeueAfter)
	}
	if !c.Remotes.Has(types.NamespacedName{Namespace: "test-ns", Name: "test-cluster"}) {
		t.Error("expected remote client to be registered after successful reconcile")
	}
}
