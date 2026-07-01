package sync

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlbuilder "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/ipam"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

const (
	finalizerName             = "network-sync.telekom.com/cleanup"
	labelManagedBy            = "network-sync.telekom.com/managed-by"
	labelManagedByValue       = "network-sync"
	annotationSourceNS        = "network-sync.telekom.com/source-namespace"
	annotationSSAAdopted      = "network-sync.telekom.com/ssa-adopted"
	annotationSSAAdoptedValue = "true"
	remoteFieldManager        = "network-sync"
	syncRequestName           = "sync"
	bgpAuthSecretRefField     = "spec.authSecretRef.name" // #nosec G101 -- field index name, not a credential value.

	ownershipManagedByLabel          = "app.kubernetes.io/managed-by"
	ownershipFluxHelmNameLabel       = "helm.toolkit.fluxcd.io/name"
	ownershipFluxHelmNamespaceLabel  = "helm.toolkit.fluxcd.io/namespace"
	ownershipHelmReleaseNameAnn      = "meta.helm.sh/release-name"
	ownershipHelmReleaseNamespaceAnn = "meta.helm.sh/release-namespace"
	lastAppliedConfigurationAnn      = "kubectl.kubernetes.io/last-applied-configuration"
)

const syncRequeueInterval = 10 * time.Second

var ownershipLabelKeys = map[string]struct{}{
	ownershipManagedByLabel:         {},
	ownershipFluxHelmNameLabel:      {},
	ownershipFluxHelmNamespaceLabel: {},
}

var ownershipAnnotationKeys = map[string]struct{}{
	ownershipHelmReleaseNameAnn:      {},
	ownershipHelmReleaseNamespaceAnn: {},
}

// Controller watches intent CRDs on the management cluster and syncs them
// to workload clusters via the RemoteClientManager.
type Controller struct {
	Client          client.Client
	Scheme          *runtime.Scheme
	Log             logr.Logger
	Remotes         *RemoteClientManager
	RemoteNamespace string
	IPAMAllocator   *ipam.Allocator
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
		&nc.NodeAttachment{},
		&nc.InterfaceConfig{},
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
		&nc.InterfaceConfigList{},
		&nc.NodeAttachmentList{},
	}
}

//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=vrfs;networks;destinations;layer2attachments;inbounds;outbounds;podnetworks;bgppeerings;collectors;trafficmirrors;announcementpolicies;nodeattachments;interfaceconfigs,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=vrfs/status;networks/status;destinations/status;layer2attachments/status;inbounds/status;outbounds/status;podnetworks/status;bgppeerings/status;collectors/status;trafficmirrors/status;announcementpolicies/status;nodeattachments/status;interfaceconfigs/status,verbs=get;patch;update
//+kubebuilder:rbac:groups=network-connector.sylvaproject.org,resources=nodenetworkstatuses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch

func (r *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("namespace", req.Namespace)

	remoteClients := r.Remotes.GetByNamespace(req.Namespace)
	if len(remoteClients) == 0 {
		// No remote client — either the workload cluster's CAPI Cluster has been
		// deleted (or never reached Ready). Drain our finalizer from any intent
		// CRs that are mid-deletion; otherwise they would block forever waiting
		// for a remote cluster that no longer exists.
		if err := r.drainFinalizersForLostRemote(ctx, log, req.Namespace); err != nil {
			return ctrl.Result{}, err
		}
		// ClusterController hasn't set up a client (yet) — wait and retry.
		return ctrl.Result{RequeueAfter: syncRequeueInterval}, nil
	}

	// Run IPAM allocation for count-mode Inbound/Outbound before syncing,
	// so that promoteIPAMAddresses can copy status→spec for the remote copy.
	if r.IPAMAllocator != nil {
		if err := r.reconcileIPAM(ctx, req.Namespace); err != nil {
			log.Error(err, "IPAM allocation failed")
			// Continue — partial allocation should not block syncing.
		}
	}

	// List and sync every intent CRD type in this namespace.
	for _, list := range intentCRDLists() {
		if err := r.Client.List(ctx, list, client.InNamespace(req.Namespace)); err != nil {
			return ctrl.Result{}, fmt.Errorf("listing %T: %w", list, err)
		}
		items := extractItems(list)
		desiredNames := desiredObjectNames(items)
		for i := range items {
			for _, remoteClient := range remoteClients {
				if err := r.syncObject(ctx, log, remoteClient, items[i]); err != nil {
					return ctrl.Result{}, err
				}
			}
		}
		for _, remoteClient := range remoteClients {
			if err := r.sweepRemoteOrphans(ctx, log, remoteClient, list, desiredNames, req.Namespace); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// Sync Secrets referenced by BGPPeering.spec.authSecretRef into the remote
	// namespace so the workload-side intent compiler can resolve the password
	// without any cross-cluster Secret access.
	for _, remoteClient := range remoteClients {
		if err := r.syncBGPSecrets(ctx, log, remoteClient, req.Namespace); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// drainFinalizersForLostRemote walks every intent CRD type in the namespace and
// strips our finalizer from any object that is being deleted. Used when the
// workload cluster's CAPI Cluster (and therefore the remote client) is gone:
// the remote object cannot be deleted, but neither can it leak — the cluster
// it lived in no longer exists. Without this drain, a deleted CAPI Cluster
// wedges every intent CR in the namespace in Terminating forever.
func (r *Controller) drainFinalizersForLostRemote(ctx context.Context, log logr.Logger, namespace string) error {
	for _, list := range intentCRDLists() {
		if err := r.Client.List(ctx, list, client.InNamespace(namespace)); err != nil {
			return fmt.Errorf("listing %T while draining finalizers: %w", list, err)
		}
		items := extractItems(list)
		for i := range items {
			obj := items[i]
			if obj.GetDeletionTimestamp().IsZero() {
				continue
			}
			if !controllerutil.ContainsFinalizer(obj, finalizerName) {
				continue
			}
			log.Info("Remote cluster gone; releasing finalizer without remote delete",
				"kind", obj.GetObjectKind().GroupVersionKind().Kind,
				"name", obj.GetName())
			if err := r.patchFinalizer(ctx, obj, func() {
				controllerutil.RemoveFinalizer(obj, finalizerName)
			}); err != nil {
				return fmt.Errorf("removing finalizer from %s/%s during drain: %w",
					obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
			}
		}
	}
	return nil
}

// syncObject handles create/update/delete for a single intent CRD object.
func (r *Controller) syncObject(ctx context.Context, log logr.Logger, remoteClient client.Client, obj client.Object) error {
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
			if err := r.patchFinalizer(ctx, obj, func() {
				controllerutil.RemoveFinalizer(obj, finalizerName)
			}); err != nil {
				return fmt.Errorf("removing finalizer from %s/%s: %w", kind, name, err)
			}
		}
		return nil
	}

	// Ensure our finalizer is present.
	if !controllerutil.ContainsFinalizer(obj, finalizerName) {
		if err := r.patchFinalizer(ctx, obj, func() {
			controllerutil.AddFinalizer(obj, finalizerName)
		}); err != nil {
			return fmt.Errorf("adding finalizer to %s/%s: %w", kind, name, err)
		}
	}

	// Build the desired remote object and apply it.
	remote := r.buildRemoteObject(obj, ns)
	if remote == nil {
		return fmt.Errorf("buildRemoteObject returned nil for %s/%s", kind, name)
	}
	log.V(1).Info("Syncing to remote", "kind", kind, "name", name)
	return r.applyRemote(ctx, remoteClient, remote)
}

func (r *Controller) patchFinalizer(ctx context.Context, obj client.Object, mutate func()) error {
	before, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("DeepCopyObject did not return client.Object for %s/%s", obj.GetNamespace(), obj.GetName())
	}
	mutate()
	if err := r.Client.Patch(ctx, obj, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("patching finalizer on %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	return nil
}

// buildRemoteObject creates the desired remote object from the mgmt-side source.
func (r *Controller) buildRemoteObject(src client.Object, sourceNamespace string) client.Object {
	dst, ok := src.DeepCopyObject().(client.Object)
	if !ok {
		return nil
	}

	// Reset metadata for remote cluster.
	dst.SetNamespace(r.remoteNamespace())
	dst.SetResourceVersion("")
	dst.SetUID("")
	dst.SetCreationTimestamp(metav1.Time{})
	dst.SetDeletionTimestamp(nil)
	dst.SetDeletionGracePeriodSeconds(nil)
	dst.SetGeneration(0)
	dst.SetGenerateName("")
	dst.SetSelfLink("")
	dst.SetManagedFields(nil)
	dst.SetFinalizers(nil)      // Remote objects don't need our finalizer
	dst.SetOwnerReferences(nil) // No cross-cluster owner refs

	// Set sync labels/annotations. Helm/Flux ownership metadata belongs to
	// the manager that applies the object on each cluster; do not copy the
	// management-side ownership into the workload cluster.
	labels := stripMetadataKeys(dst.GetLabels(), ownershipLabelKeys)
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[labelManagedBy] = labelManagedByValue
	dst.SetLabels(labels)

	annotations := stripMetadataKeys(dst.GetAnnotations(), ownershipAnnotationKeys)
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[annotationSourceNS] = sourceNamespace
	annotations[annotationSSAAdopted] = annotationSSAAdoptedValue
	// Remove system annotations.
	delete(annotations, lastAppliedConfigurationAnn)
	dst.SetAnnotations(annotations)

	// IPAM promotion: copy status.addresses → spec.addresses for Inbound/Outbound.
	r.promoteIPAMAddresses(dst)
	clearObjectStatus(dst)

	return dst
}

func clearObjectStatus(obj client.Object) {
	v := reflect.ValueOf(obj)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return
	}
	status := v.Elem().FieldByName("Status")
	if !status.IsValid() || !status.CanSet() {
		return
	}
	status.Set(reflect.Zero(status.Type()))
}

// reconcileIPAM runs IPAM allocation for count-mode Inbound/Outbound in the given namespace.
// Allocated IPs are written to status.addresses on the management-cluster resources so that
// promoteIPAMAddresses can copy them into spec.addresses for the remote copy.
func (r *Controller) reconcileIPAM(ctx context.Context, namespace string) error {
	inboundList := &nc.InboundList{}
	if err := r.Client.List(ctx, inboundList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing Inbounds for IPAM: %w", err)
	}
	outboundList := &nc.OutboundList{}
	if err := r.Client.List(ctx, outboundList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing Outbounds for IPAM: %w", err)
	}
	networkList := &nc.NetworkList{}
	if err := r.Client.List(ctx, networkList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing Networks for IPAM: %w", err)
	}
	l2aList := &nc.Layer2AttachmentList{}
	if err := r.Client.List(ctx, l2aList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing Layer2Attachments for IPAM: %w", err)
	}

	fetched := &resolver.FetchedResources{
		Inbounds:          inboundList.Items,
		Outbounds:         outboundList.Items,
		Networks:          networkList.Items,
		Layer2Attachments: l2aList.Items,
	}
	networks := resolver.ResolveNetworks(fetched.Networks)

	if err := r.IPAMAllocator.ReconcileAllocations(ctx, fetched, networks); err != nil {
		return fmt.Errorf("IPAM allocation: %w", err)
	}
	return nil
}

// promoteIPAMAddresses copies status.addresses into spec.addresses for Inbound/Outbound
// so the workload operator sees pre-allocated IPs from mgmt-cluster IPAM.
func (*Controller) promoteIPAMAddresses(obj client.Object) {
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

// applyRemote applies the network-sync-owned fields on the remote cluster.
func (r *Controller) applyRemote(ctx context.Context, remoteClient client.Client, desired client.Object) error {
	desiredSourceNamespace := desired.GetAnnotations()[annotationSourceNS]
	if desiredSourceNamespace == "" {
		return fmt.Errorf("desired remote object %s/%s is missing %s annotation",
			desired.GetNamespace(), desired.GetName(), annotationSourceNS)
	}

	existing, ok := desired.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("DeepCopyObject did not return client.Object for %s/%s", desired.GetNamespace(), desired.GetName())
	}
	err := remoteClient.Get(ctx, types.NamespacedName{
		Namespace: desired.GetNamespace(),
		Name:      desired.GetName(),
	}, existing)

	if apierrors.IsNotFound(err) {
		return r.applyRemoteDesired(ctx, remoteClient, desired)
	}
	if err != nil {
		return fmt.Errorf("getting remote object: %w", err)
	}

	if labels := existing.GetLabels(); labels[labelManagedBy] != labelManagedByValue {
		return fmt.Errorf("remote object %s/%s exists but not managed by us", desired.GetNamespace(), desired.GetName())
	}
	existingSourceNamespace, hasSourceNamespace := existing.GetAnnotations()[annotationSourceNS]
	// Older network-sync managed objects did not have annotationSourceNS yet.
	// Adopt only that absent legacy value; reject explicit empty or mismatched
	// values because they break the source namespace ownership boundary.
	if hasSourceNamespace && existingSourceNamespace != desiredSourceNamespace {
		return fmt.Errorf("remote object %s/%s belongs to source namespace %q, not %q",
			desired.GetNamespace(), desired.GetName(), existingSourceNamespace, desiredSourceNamespace)
	}

	if needsLegacySSAAdoption(existing) {
		if err := r.adoptLegacyRemoteObject(ctx, remoteClient, existing, desired); err != nil {
			return err
		}
	}

	return r.applyRemoteDesired(ctx, remoteClient, desired)
}

func needsLegacySSAAdoption(existing client.Object) bool {
	return existing.GetAnnotations()[annotationSSAAdopted] != annotationSSAAdoptedValue
}

func (r *Controller) adoptLegacyRemoteObject(ctx context.Context, remoteClient client.Client, existing, desired client.Object) error {
	legacyDesired, ok := desired.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("DeepCopyObject did not return client.Object for %s/%s", desired.GetNamespace(), desired.GetName())
	}
	if err := r.prepareApplyObject(legacyDesired); err != nil {
		return err
	}
	preserveOwnershipMetadata(existing, legacyDesired)
	legacyDesired.SetResourceVersion(existing.GetResourceVersion())
	legacyDesired.SetUID(existing.GetUID())
	legacyDesired.SetManagedFields(nil)
	if err := remoteClient.Update(ctx, legacyDesired); err != nil {
		return fmt.Errorf("adopting legacy remote object %s/%s before server-side apply: %w",
			desired.GetNamespace(), desired.GetName(), err)
	}
	return nil
}

func (r *Controller) applyRemoteDesired(ctx context.Context, remoteClient client.Client, desired client.Object) error {
	if err := r.prepareApplyObject(desired); err != nil {
		return err
	}
	desired.SetResourceVersion("")
	desired.SetUID("")
	desired.SetManagedFields(nil)
	objMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(desired)
	if err != nil {
		return fmt.Errorf("converting %s/%s to unstructured apply configuration: %w",
			desired.GetNamespace(), desired.GetName(), err)
	}
	unstructuredDesired := &unstructured.Unstructured{Object: objMap}
	unstructuredDesired.SetGroupVersionKind(desired.GetObjectKind().GroupVersionKind())
	if err := remoteClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(unstructuredDesired),
		client.FieldOwner(remoteFieldManager), client.ForceOwnership); err != nil {
		return fmt.Errorf("server-side applying remote object %s/%s: %w", desired.GetNamespace(), desired.GetName(), err)
	}
	return nil
}

func (r *Controller) prepareApplyObject(obj client.Object) error {
	if !obj.GetObjectKind().GroupVersionKind().Empty() {
		return nil
	}
	if r.Scheme == nil {
		return fmt.Errorf("cannot infer GVK for %T without a scheme", obj)
	}
	gvk, err := apiutil.GVKForObject(obj, r.Scheme)
	if err != nil {
		return fmt.Errorf("inferring GVK for %T: %w", obj, err)
	}
	obj.GetObjectKind().SetGroupVersionKind(gvk)
	return nil
}

func stripMetadataKeys(metadata map[string]string, keys map[string]struct{}) map[string]string {
	if len(metadata) == 0 {
		return nil
	}

	filtered := make(map[string]string, len(metadata))
	for key, value := range metadata {
		if _, ok := keys[key]; ok {
			continue
		}
		filtered[key] = value
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func preserveOwnershipMetadata(existing, desired client.Object) {
	desired.SetLabels(preserveMetadataKeys(existing.GetLabels(), desired.GetLabels(), ownershipLabelKeys))
	desired.SetAnnotations(preserveMetadataKeys(existing.GetAnnotations(), desired.GetAnnotations(), ownershipAnnotationKeys))
}

func preserveMetadataKeys(existing, desired map[string]string, keys map[string]struct{}) map[string]string {
	if len(existing) == 0 {
		return desired
	}

	preserved := desired
	for key := range keys {
		value, ok := existing[key]
		if !ok {
			continue
		}
		if preserved == nil {
			preserved = make(map[string]string)
		}
		preserved[key] = value
	}
	return preserved
}

// deleteRemote removes the object from the remote cluster.
func (r *Controller) deleteRemote(ctx context.Context, remoteClient client.Client, src client.Object) error {
	remote, ok := src.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("DeepCopyObject did not return client.Object for %s/%s", src.GetNamespace(), src.GetName())
	}
	remote.SetNamespace(r.remoteNamespace())
	remote.SetResourceVersion("")
	remote.SetUID("")

	err := remoteClient.Get(ctx, types.NamespacedName{
		Namespace: remote.GetNamespace(),
		Name:      remote.GetName(),
	}, remote)
	if apierrors.IsNotFound(err) {
		return nil // Already gone.
	}
	if err != nil {
		return fmt.Errorf("getting remote object %s/%s before delete: %w", remote.GetNamespace(), remote.GetName(), err)
	}

	if remote.GetLabels()[labelManagedBy] != labelManagedByValue {
		return nil
	}
	remoteSourceNamespace, hasSourceNamespace := remote.GetAnnotations()[annotationSourceNS]
	if hasSourceNamespace && remoteSourceNamespace != src.GetNamespace() {
		return nil
	}

	if err := remoteClient.Delete(ctx, remote); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting remote object %s/%s: %w", remote.GetNamespace(), remote.GetName(), err)
	}
	return nil
}

func (r *Controller) sweepRemoteOrphans(ctx context.Context, log logr.Logger, remoteClient client.Client,
	sourceList client.ObjectList, desiredNames map[string]struct{}, sourceNamespace string,
) error {
	remoteList, err := newObjectListLike(sourceList)
	if err != nil {
		return err
	}
	if err := remoteClient.List(ctx, remoteList,
		client.InNamespace(r.remoteNamespace()),
		client.MatchingLabels{labelManagedBy: labelManagedByValue},
	); err != nil {
		return fmt.Errorf("listing remote %T for orphan sweep: %w", remoteList, err)
	}

	items := extractItems(remoteList)
	for i := range items {
		obj := items[i]
		if obj.GetAnnotations()[annotationSourceNS] != sourceNamespace {
			continue
		}
		if _, keep := desiredNames[obj.GetName()]; keep {
			continue
		}
		log.Info("Sweeping orphan synced intent object on workload cluster",
			"kind", obj.GetObjectKind().GroupVersionKind().Kind,
			"namespace", obj.GetNamespace(),
			"name", obj.GetName())
		if err := remoteClient.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting orphan remote %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}

// syncBGPSecrets mirrors Secrets referenced by BGPPeering.spec.authSecretRef
// from the management namespace to the remote workload namespace, and sweeps
// remote Secrets we previously synced that are no longer referenced.
func (r *Controller) syncBGPSecrets(ctx context.Context, log logr.Logger, remoteClient client.Client, namespace string) error {
	bpList := &nc.BGPPeeringList{}
	if err := r.Client.List(ctx, bpList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing BGPPeerings for secret sync: %w", err)
	}

	referenced := map[string]struct{}{}
	for i := range bpList.Items {
		bp := &bpList.Items[i]
		if !bp.GetDeletionTimestamp().IsZero() {
			continue
		}
		if bp.Spec.AuthSecretRef == nil || bp.Spec.AuthSecretRef.Name == "" {
			continue
		}
		referenced[bp.Spec.AuthSecretRef.Name] = struct{}{}
	}

	applied := map[string]struct{}{}
	for name := range referenced {
		src := &corev1.Secret{}
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, src); err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("BGPPeering authSecretRef target Secret missing; skipping",
					"namespace", namespace, "name", name)
				continue
			}
			return fmt.Errorf("getting BGP auth Secret %s/%s: %w", namespace, name, err)
		}
		remote := r.buildRemoteSecret(src, namespace)
		if err := r.applyRemote(ctx, remoteClient, remote); err != nil {
			return fmt.Errorf("syncing BGP auth Secret %s/%s: %w", namespace, name, err)
		}
		applied[name] = struct{}{}
	}

	// Sweep orphaned remote Secrets we previously synced.
	remoteSecrets := &corev1.SecretList{}
	if err := remoteClient.List(ctx, remoteSecrets,
		client.InNamespace(r.remoteNamespace()),
		client.MatchingLabels{labelManagedBy: labelManagedByValue},
	); err != nil {
		return fmt.Errorf("listing remote Secrets for sweep: %w", err)
	}
	for i := range remoteSecrets.Items {
		s := &remoteSecrets.Items[i]
		// Only sweep Secrets that originated from this management namespace.
		if s.Annotations[annotationSourceNS] != namespace {
			continue
		}
		if _, keep := applied[s.Name]; keep {
			continue
		}
		log.Info("Sweeping orphan synced Secret on workload cluster",
			"namespace", s.Namespace, "name", s.Name)
		if err := remoteClient.Delete(ctx, s); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting orphan remote Secret %s/%s: %w", s.Namespace, s.Name, err)
		}
	}

	return nil
}

// buildRemoteSecret prepares a Secret for application to the remote cluster,
// preserving Data/Type but resetting metadata and stamping our managed-by label.
func (r *Controller) buildRemoteSecret(src *corev1.Secret, sourceNamespace string) *corev1.Secret {
	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      src.Name,
			Namespace: r.remoteNamespace(),
			Labels: map[string]string{
				labelManagedBy: labelManagedByValue,
			},
			Annotations: map[string]string{
				annotationSourceNS:   sourceNamespace,
				annotationSSAAdopted: annotationSSAAdoptedValue,
			},
		},
		Type: src.Type,
		Data: map[string][]byte{},
	}
	for k, v := range src.Data {
		b := make([]byte, len(v))
		copy(b, v)
		dst.Data[k] = b
	}
	return dst
}

func (r *Controller) enqueueForBGPSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	bpList := &nc.BGPPeeringList{}
	if err := r.Client.List(ctx, bpList,
		client.InNamespace(obj.GetNamespace()),
		client.MatchingFields{bgpAuthSecretRefField: obj.GetName()},
	); err != nil {
		r.Log.Error(err, "Listing BGPPeerings for auth Secret failed",
			"namespace", obj.GetNamespace(), "secret", obj.GetName())
		return nil
	}
	for i := range bpList.Items {
		bp := &bpList.Items[i]
		if !bp.GetDeletionTimestamp().IsZero() ||
			bp.Spec.AuthSecretRef == nil ||
			bp.Spec.AuthSecretRef.Name != obj.GetName() {
			continue
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: obj.GetNamespace(),
				Name:      syncRequestName,
			},
		}}
	}
	return nil
}

func indexBGPAuthSecretRef(obj client.Object) []string {
	bp, ok := obj.(*nc.BGPPeering)
	if !ok || bp.Spec.AuthSecretRef == nil || bp.Spec.AuthSecretRef.Name == "" {
		return nil
	}
	return []string{bp.Spec.AuthSecretRef.Name}
}

// SetupWithManager registers watches for all intent CRD types.
func (r *Controller) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &nc.BGPPeering{}, bgpAuthSecretRefField, indexBGPAuthSecretRef); err != nil {
		return fmt.Errorf("indexing BGPPeerings by auth Secret: %w", err)
	}

	// Map any intent CRD change → reconcile for its namespace.
	enqueueNS := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, obj client.Object) []reconcile.Request {
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Namespace: obj.GetNamespace(),
					Name:      syncRequestName, // Synthetic key; we reconcile the whole namespace.
				},
			}}
		},
	)

	secretToSyncNamespace := handler.EnqueueRequestsFromMapFunc(r.enqueueForBGPSecret)
	secretPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return e.Object != nil
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return e.ObjectNew != nil
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return e.Object != nil
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return e.Object != nil
		},
	}

	builder := ctrl.NewControllerManagedBy(mgr).
		Named("sync-controller")

	for _, obj := range intentCRDTypes() {
		builder = builder.Watches(obj, enqueueNS)
	}
	builder = builder.Watches(&corev1.Secret{}, secretToSyncNamespace, ctrlbuilder.WithPredicates(secretPredicate))

	if err := builder.Complete(r); err != nil {
		return fmt.Errorf("setting up sync controller: %w", err)
	}
	return nil
}

// remoteNamespace returns the target namespace on workload clusters.
// Defaults to "default" if not configured.
func (r *Controller) remoteNamespace() string {
	if r.RemoteNamespace != "" {
		return r.RemoteNamespace
	}
	return "default"
}

// extractItems pulls []client.Object from a typed ObjectList using reflection
// to iterate the Items field. This avoids a per-type switch that grows with
// each new CRD and triggers cyclomatic complexity linters.
func extractItems(list client.ObjectList) []client.Object {
	v := reflect.ValueOf(list).Elem()
	items := v.FieldByName("Items")
	if !items.IsValid() || items.Kind() != reflect.Slice {
		return nil
	}

	out := make([]client.Object, items.Len())
	for i := 0; i < items.Len(); i++ {
		obj, ok := items.Index(i).Addr().Interface().(client.Object)
		if !ok {
			return nil
		}
		out[i] = obj
	}

	return out
}

func desiredObjectNames(items []client.Object) map[string]struct{} {
	names := make(map[string]struct{}, len(items))
	for i := range items {
		if !items[i].GetDeletionTimestamp().IsZero() {
			continue
		}
		names[items[i].GetName()] = struct{}{}
	}
	return names
}

func newObjectListLike(list client.ObjectList) (client.ObjectList, error) {
	elem, ok := pointerElementType(reflect.TypeOf(list))
	if !ok {
		return nil, fmt.Errorf("expected pointer ObjectList, got %T", list)
	}
	copyList, ok := reflect.New(elem).Interface().(client.ObjectList)
	if !ok {
		return nil, fmt.Errorf("new %s is not a client.ObjectList", elem)
	}
	return copyList, nil
}

func pointerElementType(t reflect.Type) (elem reflect.Type, ok bool) {
	if t == nil {
		return nil, false
	}
	defer func() {
		if recover() != nil {
			elem = nil
			ok = false
		}
	}()
	elem = t.Elem()
	return elem, reflect.PointerTo(elem) == t
}
