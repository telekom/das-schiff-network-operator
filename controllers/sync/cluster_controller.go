package sync

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var capiClusterGVK = schema.GroupVersionKind{
	Group:   "cluster.x-k8s.io",
	Version: "v1beta1",
	Kind:    "Cluster",
}

// ClusterController watches CAPI Cluster objects and maintains remote clients
// from their kubeconfig Secrets.
type ClusterController struct {
	Client  client.Client
	Log     logr.Logger
	Remotes *RemoteClientManager
}

func (r *ClusterController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("cluster", req.NamespacedName)

	// Fetch the Cluster object.
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(capiClusterGVK)
	err := r.Client.Get(ctx, req.NamespacedName, cluster)
	if apierrors.IsNotFound(err) {
		log.Info("Cluster deleted, removing remote client")
		r.Remotes.Remove(req.Namespace)
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("fetching Cluster: %w", err)
	}

	// Look up the kubeconfig Secret: <cluster-name>-kubeconfig
	secretName := cluster.GetName() + "-kubeconfig"
	secret := &corev1.Secret{}
	err = r.Client.Get(ctx, types.NamespacedName{
		Namespace: req.Namespace,
		Name:      secretName,
	}, secret)
	if apierrors.IsNotFound(err) {
		log.Info("kubeconfig Secret not found yet, waiting", "secret", secretName)
		return ctrl.Result{RequeueAfter: 30_000_000_000}, nil // 30s
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("fetching kubeconfig Secret %q: %w", secretName, err)
	}

	kubeconfig, ok := secret.Data["value"]
	if !ok || len(kubeconfig) == 0 {
		log.Info("kubeconfig Secret missing 'value' key", "secret", secretName)
		return ctrl.Result{RequeueAfter: 30_000_000_000}, nil
	}

	if err := r.Remotes.UpdateFromKubeconfig(req.Namespace, kubeconfig); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating remote client for %q: %w", req.Namespace, err)
	}

	log.Info("Remote client ready", "namespace", req.Namespace)
	return ctrl.Result{}, nil
}

// SetupWithManager registers watches for CAPI Cluster and kubeconfig Secrets.
func (r *ClusterController) SetupWithManager(mgr ctrl.Manager) error {
	clusterObj := &unstructured.Unstructured{}
	clusterObj.SetGroupVersionKind(capiClusterGVK)

	// Map Secrets back to their owning Cluster.
	secretToCluster := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			secret, ok := obj.(*corev1.Secret)
			if !ok {
				return nil
			}
			// Convention: <cluster-name>-kubeconfig
			name := secret.GetName()
			const suffix = "-kubeconfig"
			if len(name) <= len(suffix) {
				return nil
			}
			clusterName := name[:len(name)-len(suffix)]
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Namespace: secret.GetNamespace(),
					Name:      clusterName,
				},
			}}
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named("cluster-controller").
		Watches(clusterObj, &handler.EnqueueRequestForObject{}).
		Watches(&corev1.Secret{}, secretToCluster).
		Complete(r)
}
