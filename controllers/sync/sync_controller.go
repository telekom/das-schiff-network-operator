package sync

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	finalizerName = "network-sync.telekom.com/cleanup"
	labelManagedBy = "network-sync.telekom.com/managed-by"
	labelManagedByValue = "network-sync"
	annotationSourceNS = "network-sync.telekom.com/source-namespace"

	// RemoteNamespace is the namespace intent CRDs are synced into on workload clusters.
	RemoteNamespace = "default"
)

// SyncController watches intent CRDs on the management cluster and syncs them
// to workload clusters via the RemoteClientManager.
type SyncController struct {
	Client  client.Client
	Scheme  *runtime.Scheme
	Log     logr.Logger
	Remotes *RemoteClientManager
}

// intentCRDTypes returns fresh instances of all intent CRD types to sync.
func intentCRDTypes() []client.Object {
	return []client.Object{
		&nc.VRF{},
		&nc.Network{},
		&nc.Destination{},
		&nc.Layer2Attachment{},
		&nc.Inbound{},
		&nc.Outbound{},
		&nc.PodNetwork{},
		&nc.BGPPeering{},
		&nc.Collector{},
		&nc.TrafficMirror{},
		&nc.AnnouncementPolicy{},
	}
}

// intentCRDLists returns fresh list instances for all intent CRD types.
func intentCRDLists() []client.ObjectList {
	return []client.ObjectList{
		&nc.VRFList{},
		&nc.NetworkList{},
		&nc.DestinationList{},
		&nc.Layer2AttachmentList{},
		&nc.InboundList{},
		&nc.OutboundList{},
		&nc.PodNetworkList{},
		&nc.BGPPeeringList{},
		&nc.CollectorList{},
		&nc.TrafficMirrorList{},
		&nc.AnnouncementPolicyList{},
	}
}

func (r *SyncController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("namespace", req.Namespace)

	remoteClient := r.Remotes.Get(req.Namespace)
	if remoteClient == nil {
		// No remote client yet — ClusterController hasn't set it up.
		return ctrl.Result{RequeueAfter: 10_000_000_000}, nil // 10s
	}

	// List and sync every intent CRD type in this namespace.
	for _, list := range intentCRDLists() {
		if err := r.Client.List(ctx, list, client.InNamespace(req.Namespace)); err != nil {
			return ctrl.Result{}, fmt.Errorf("listing %T: %w", list, err)
		}
		items := extractItems(list)
		for i := range items {
			if err := r.syncObject(ctx, log, remoteClient, items[i]); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

// syncObject handles create/update/delete for a single intent CRD object.
func (r *SyncController) syncObject(ctx context.Context, log logr.Logger, remoteClient client.Client, obj client.Object) error {
	name := obj.GetName()
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	ns := obj.GetNamespace()

	// Handle deletion: if marked for deletion and our finalizer is present,
	// delete remote object, then remove finalizer.
	if !obj.GetDeletionTimestamp().IsZero() {
		if controllerutil.ContainsFinalizer(obj, finalizerName) {
			log.Info("Deleting remote object", "kind", kind, "name", name)
			if err := r.deleteRemote(ctx, remoteClient, obj); err != nil {
				return fmt.Errorf("deleting remote %s/%s: %w", kind, name, err)
			}
			controllerutil.RemoveFinalizer(obj, finalizerName)
			if err := r.Client.Update(ctx, obj); err != nil {
				return fmt.Errorf("removing finalizer from %s/%s: %w", kind, name, err)
			}
		}
		return nil
	}

	// Ensure our finalizer is present.
	if !controllerutil.ContainsFinalizer(obj, finalizerName) {
		controllerutil.AddFinalizer(obj, finalizerName)
		if err := r.Client.Update(ctx, obj); err != nil {
			return fmt.Errorf("adding finalizer to %s/%s: %w", kind, name, err)
		}
	}

	// Build the desired remote object and apply it.
	remote := r.buildRemoteObject(obj, ns)
	log.V(1).Info("Syncing to remote", "kind", kind, "name", name)
	return r.applyRemote(ctx, remoteClient, remote)
}

// buildRemoteObject creates the desired remote object from the mgmt-side source.
func (r *SyncController) buildRemoteObject(src client.Object, sourceNamespace string) client.Object {
	dst := src.DeepCopyObject().(client.Object)

	// Reset metadata for remote cluster.
	dst.SetNamespace(RemoteNamespace)
	dst.SetResourceVersion("")
	dst.SetUID("")
	dst.SetCreationTimestamp(metav1.Time{})
	dst.SetDeletionTimestamp(nil)
	dst.SetDeletionGracePeriodSeconds(nil)
	dst.SetGenerateName("")
	dst.SetSelfLink("")
	dst.SetManagedFields(nil)
	dst.SetFinalizers(nil)       // Remote objects don't need our finalizer
	dst.SetOwnerReferences(nil)  // No cross-cluster owner refs

	// Set sync labels/annotations.
	labels := dst.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[labelManagedBy] = labelManagedByValue
	dst.SetLabels(labels)

	annotations := dst.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[annotationSourceNS] = sourceNamespace
	// Remove system annotations.
	delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
	dst.SetAnnotations(annotations)

	// IPAM promotion: copy status.addresses → spec.addresses for Inbound/Outbound.
	r.promoteIPAMAddresses(dst)

	return dst
}

// promoteIPAMAddresses copies status.addresses into spec.addresses for Inbound/Outbound
// so the workload operator sees pre-allocated IPs from mgmt-cluster IPAM.
func (r *SyncController) promoteIPAMAddresses(obj client.Object) {
	switch v := obj.(type) {
	case *nc.Inbound:
		if v.Spec.Addresses == nil && v.Status.Addresses != nil &&
			(len(v.Status.Addresses.IPv4) > 0 || len(v.Status.Addresses.IPv6) > 0) {
			v.Spec.Addresses = v.Status.Addresses.DeepCopy()
			v.Spec.Count = nil // Switch from count → manual mode
		}
	case *nc.Outbound:
		if v.Spec.Addresses == nil && v.Status.Addresses != nil &&
			(len(v.Status.Addresses.IPv4) > 0 || len(v.Status.Addresses.IPv6) > 0) {
			v.Spec.Addresses = v.Status.Addresses.DeepCopy()
			v.Spec.Count = nil
		}
	}
}

// applyRemote creates or updates the object on the remote cluster.
func (r *SyncController) applyRemote(ctx context.Context, remoteClient client.Client, desired client.Object) error {
	existing := desired.DeepCopyObject().(client.Object)
	err := remoteClient.Get(ctx, types.NamespacedName{
		Namespace: desired.GetNamespace(),
		Name:      desired.GetName(),
	}, existing)

	if apierrors.IsNotFound(err) {
		return remoteClient.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("getting remote object: %w", err)
	}

	// Verify we own this object.
	labels := existing.GetLabels()
	if labels[labelManagedBy] != labelManagedByValue {
		return fmt.Errorf("remote object %s/%s exists but not managed by us", desired.GetNamespace(), desired.GetName())
	}

	// Preserve remote resourceVersion for update.
	desired.SetResourceVersion(existing.GetResourceVersion())
	desired.SetUID(existing.GetUID())
	return remoteClient.Update(ctx, desired)
}

// deleteRemote removes the object from the remote cluster.
func (r *SyncController) deleteRemote(ctx context.Context, remoteClient client.Client, src client.Object) error {
	remote := src.DeepCopyObject().(client.Object)
	remote.SetNamespace(RemoteNamespace)
	remote.SetResourceVersion("")
	remote.SetUID("")

	err := remoteClient.Delete(ctx, remote)
	if apierrors.IsNotFound(err) {
		return nil // Already gone.
	}
	return err
}

// SetupWithManager registers watches for all intent CRD types.
func (r *SyncController) SetupWithManager(mgr ctrl.Manager) error {
	// Map any intent CRD change → reconcile for its namespace.
	enqueueNS := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, obj client.Object) []reconcile.Request {
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Namespace: obj.GetNamespace(),
					Name:      "sync", // Synthetic key; we reconcile the whole namespace.
				},
			}}
		},
	)

	builder := ctrl.NewControllerManagedBy(mgr).
		Named("sync-controller")

	for _, obj := range intentCRDTypes() {
		builder = builder.Watches(obj, enqueueNS)
	}

	return builder.Complete(r)
}

// extractItems pulls []client.Object from a typed ObjectList.
func extractItems(list client.ObjectList) []client.Object {
	switch v := list.(type) {
	case *nc.VRFList:
		out := make([]client.Object, len(v.Items))
		for i := range v.Items {
			out[i] = &v.Items[i]
		}
		return out
	case *nc.NetworkList:
		out := make([]client.Object, len(v.Items))
		for i := range v.Items {
			out[i] = &v.Items[i]
		}
		return out
	case *nc.DestinationList:
		out := make([]client.Object, len(v.Items))
		for i := range v.Items {
			out[i] = &v.Items[i]
		}
		return out
	case *nc.Layer2AttachmentList:
		out := make([]client.Object, len(v.Items))
		for i := range v.Items {
			out[i] = &v.Items[i]
		}
		return out
	case *nc.InboundList:
		out := make([]client.Object, len(v.Items))
		for i := range v.Items {
			out[i] = &v.Items[i]
		}
		return out
	case *nc.OutboundList:
		out := make([]client.Object, len(v.Items))
		for i := range v.Items {
			out[i] = &v.Items[i]
		}
		return out
	case *nc.PodNetworkList:
		out := make([]client.Object, len(v.Items))
		for i := range v.Items {
			out[i] = &v.Items[i]
		}
		return out
	case *nc.BGPPeeringList:
		out := make([]client.Object, len(v.Items))
		for i := range v.Items {
			out[i] = &v.Items[i]
		}
		return out
	case *nc.CollectorList:
		out := make([]client.Object, len(v.Items))
		for i := range v.Items {
			out[i] = &v.Items[i]
		}
		return out
	case *nc.TrafficMirrorList:
		out := make([]client.Object, len(v.Items))
		for i := range v.Items {
			out[i] = &v.Items[i]
		}
		return out
	case *nc.AnnouncementPolicyList:
		out := make([]client.Object, len(v.Items))
		for i := range v.Items {
			out[i] = &v.Items[i]
		}
		return out
	default:
		return nil
	}
}
