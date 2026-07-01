package sync

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
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

	// annotationSourceGeneration records the management object's
	// metadata.generation that the remote object's spec was built from. Status
	// sync-back (workload → management) uses it to decide whether the workload
	// status it reads back corresponds to the intent we last pushed: the remote
	// copy must carry the current management generation before its Ready/Resolved
	// conditions are treated as authoritative.
	annotationSourceGeneration = "network-sync.telekom.com/source-generation"

	// annotationManagedLabels and annotationManagedAnnotations record the label
	// and annotation keys this controller propagated on the last sync. They let a
	// subsequent sync prune the keys we stop setting — source keys that were
	// removed upstream, or Flux/GitOps keys we used to copy before we learned to
	// strip them — without ever touching foreign keys owned by other actors on
	// the remote object. This is the same "last-applied" ownership trick kubectl
	// and server-side apply use, and it is what lets an additive merge converge
	// existing objects instead of accreting stale keys forever.
	annotationManagedLabels      = "network-sync.telekom.com/managed-labels"
	annotationManagedAnnotations = "network-sync.telekom.com/managed-annotations"

	ownershipManagedByLabel          = "app.kubernetes.io/managed-by"
	ownershipFluxHelmNameLabel       = "helm.toolkit.fluxcd.io/name"
	ownershipFluxHelmNamespaceLabel  = "helm.toolkit.fluxcd.io/namespace"
	ownershipHelmReleaseNameAnn      = "meta.helm.sh/release-name"
	ownershipHelmReleaseNamespaceAnn = "meta.helm.sh/release-namespace"
	lastAppliedConfigurationAnn      = "kubectl.kubernetes.io/last-applied-configuration"
)

// gitOpsKeyPrefixes are label/annotation key prefixes owned by GitOps tooling
// (Flux). They are stripped when building the remote object so the management
// cluster's kustomization inventory metadata is never propagated to workload
// clusters, where a local Flux would otherwise treat our synced objects as part
// of its own inventory and continually fight us (or prune them).
var gitOpsKeyPrefixes = []string{
	"kustomize.toolkit.fluxcd.io/",
	"helm.toolkit.fluxcd.io/",
	"reconcile.fluxcd.io/",
}

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

// errMultipleWorkloadClusters is returned by Reconcile (and surfaced on the
// intent resources as a condition + event) when a namespace resolves to more
// than one workload cluster, which is a misconfiguration for the
// one-cluster-per-namespace design. Returning it lets the controller's rate
// limiter apply exponential backoff while the misconfiguration persists.
var errMultipleWorkloadClusters = errors.New("namespace maps to multiple workload clusters")

// statusPollInterval is how often Reconcile requeues on success so that status
// sync-back (workload → management) is refreshed. The controller only watches
// management-cluster intent CRDs; workload-side status changes do not wake it,
// so status is pulled on this cadence instead.
const statusPollInterval = 30 * time.Second

// mirroredConditionTypes is the allowlist of status condition types copied from
// the workload cluster back onto the management object. Everything else in
// status — addresses, interfaceName, workloadASNumber, observedGeneration — is
// deliberately NOT mirrored: those fields are either owned by the management
// cluster or feed the workload spec (mgmt status.addresses → workload
// spec.addresses), so copying an empty or lagging workload value back would
// corrupt the management source of truth.
var mirroredConditionTypes = map[string]bool{
	nc.ConditionTypeReady:    true,
	nc.ConditionTypeResolved: true,
}

// ipamAllocations holds the addresses allocated by the management-cluster IPAM
// during a reconcile, keyed by Inbound/Outbound name. It lets the forward sync
// promote just-allocated addresses without re-reading the (lagging) client
// cache. A nil *ipamAllocations is safe to use — the accessors fall back to the
// object's own status.
type ipamAllocations struct {
	inbound  map[string]*nc.AddressAllocation
	outbound map[string]*nc.AddressAllocation
}

func newIPAMAllocations() *ipamAllocations {
	return &ipamAllocations{
		inbound:  map[string]*nc.AddressAllocation{},
		outbound: map[string]*nc.AddressAllocation{},
	}
}

// inboundAddresses returns the freshly-allocated addresses for the named
// Inbound, or fallback when none were allocated this reconcile.
func (a *ipamAllocations) inboundAddresses(name string, fallback *nc.AddressAllocation) *nc.AddressAllocation {
	if a != nil {
		if addrs, ok := a.inbound[name]; ok {
			return addrs
		}
	}
	return fallback
}

// outboundAddresses returns the freshly-allocated addresses for the named
// Outbound, or fallback when none were allocated this reconcile.
func (a *ipamAllocations) outboundAddresses(name string, fallback *nc.AddressAllocation) *nc.AddressAllocation {
	if a != nil {
		if addrs, ok := a.outbound[name]; ok {
			return addrs
		}
	}
	return fallback
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
	Recorder        events.EventRecorder
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
		clusterExists, err := r.remoteClusterExists(ctx, req.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !clusterExists {
			// The workload cluster's CAPI Cluster is gone. Drain finalizers from
			// intent CRs that are mid-deletion; otherwise they would block forever
			// waiting for a workload cluster that no longer exists.
			if err := r.drainFinalizersForLostRemote(ctx, log, req.Namespace); err != nil {
				return ctrl.Result{}, err
			}
		}
		// ClusterController hasn't set up a client (yet) — wait and retry.
		return ctrl.Result{RequeueAfter: syncRequeueInterval}, nil
	}
	if len(remoteClients) > 1 {
		// A namespace maps to exactly one workload cluster by design. More than
		// one CAPI Cluster in the same namespace is a misconfiguration: syncing
		// the same intent to several clusters would be ambiguous (duplicate
		// forward writes, no single authoritative status to mirror back), so we
		// refuse to process the namespace until it is resolved rather than act on
		// a guess. Surface the block on every intent resource (condition + event)
		// so it is visible in `kubectl get`, not only in the controller log.
		if err := r.blockOnMultipleRemotes(ctx, req.Namespace, len(remoteClients)); err != nil {
			return ctrl.Result{}, err
		}
		// Return the error so the controller's rate limiter applies exponential
		// backoff instead of a tight fixed-interval requeue: nothing can change
		// until the namespace maps to a single cluster again, and the condition +
		// event already inform the user, so we must not busy-loop LISTing intent
		// CRDs. The backoff resets automatically once a reconcile succeeds (i.e.
		// once the misconfiguration is resolved).
		return ctrl.Result{}, fmt.Errorf("%w: namespace %q maps to %d clusters",
			errMultipleWorkloadClusters, req.Namespace, len(remoteClients))
	}
	remoteClient := remoteClients[0]

	// Run IPAM allocation for count-mode Inbound/Outbound before syncing, so that
	// promoteIPAMAddresses can copy status→spec for the remote copy. The returned
	// allocations carry the freshly-assigned addresses so the promotion does not
	// depend on the read cache catching up to the status write.
	var ipamAddrs *ipamAllocations
	if r.IPAMAllocator != nil {
		allocs, err := r.reconcileIPAM(ctx, req.Namespace)
		if err != nil {
			log.Error(err, "IPAM allocation failed")
			// Continue — partial allocation should not block syncing.
		}
		ipamAddrs = allocs
	}

	// List and sync every intent CRD type in this namespace.
	for _, list := range intentCRDLists() {
		if err := r.Client.List(ctx, list, client.InNamespace(req.Namespace)); err != nil {
			return ctrl.Result{}, fmt.Errorf("listing %T: %w", list, err)
		}
		items := extractItems(list)
		desiredNames := desiredObjectNames(items)
		for i := range items {
			if err := r.syncObject(ctx, log, remoteClient, items[i], ipamAddrs); err != nil {
				return ctrl.Result{}, err
			}
			// Mirror Ready/Resolved conditions from the workload copy back onto
			// the management object, reusing the object already listed above so
			// status sync-back adds no extra LIST traffic. Objects mid-deletion
			// are handled by the forward path and are not resurrected.
			if items[i].GetDeletionTimestamp().IsZero() {
				if err := r.syncObjectStatusBack(ctx, log, remoteClient, items[i]); err != nil {
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
	if err := r.syncBGPSecrets(ctx, log, remoteClient, req.Namespace); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue so workload status changes are pulled back periodically; the
	// controller does not watch the workload clusters.
	return ctrl.Result{RequeueAfter: statusPollInterval}, nil
}

func (r *Controller) remoteClusterExists(ctx context.Context, namespace string) (bool, error) {
	clusterList := &unstructured.UnstructuredList{}
	clusterList.SetGroupVersionKind(capiClusterGVK)
	if err := r.Client.List(ctx, clusterList, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("listing CAPI Clusters in namespace %s: %w", namespace, err)
	}
	return len(clusterList.Items) > 0, nil
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
func (r *Controller) syncObject(ctx context.Context, log logr.Logger, remoteClient client.Client, obj client.Object, ipamAddrs *ipamAllocations) error {
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
	remote := r.buildRemoteObject(obj, ns, ipamAddrs)
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

// syncObjectStatusBack reads the workload copy of a single intent object and
// mirrors its Ready/Resolved conditions plus the read-only derived status fields
// (network CIDRs, VRF lists) back onto the management object. It is the reverse
// of the spec sync — workload → management. Conditions are restricted to
// mirroredConditionTypes, and derived fields are mirrored only when the workload
// has caught up to the current intent, so a lagging or empty workload status can
// never overwrite management-owned status fields (addresses, interfaceName,
// workloadASNumber) that feed the workload spec.
func (r *Controller) syncObjectStatusBack(ctx context.Context, log logr.Logger, remoteClient client.Client, mgmtObj client.Object) error {
	kind := mgmtObj.GetObjectKind().GroupVersionKind().Kind
	name := mgmtObj.GetName()

	if statusConditionsPtr(mgmtObj) == nil {
		// Type carries no status.conditions field; nothing to mirror.
		return nil
	}

	remote, ok := mgmtObj.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("DeepCopyObject did not return client.Object for %s/%s", mgmtObj.GetNamespace(), name)
	}
	err := remoteClient.Get(ctx, types.NamespacedName{Namespace: r.remoteNamespace(), Name: name}, remote)
	if apierrors.IsNotFound(err) {
		// Not synced yet; the forward pass will create it. Nothing to mirror.
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting remote %s/%s for status sync-back: %w", kind, name, err)
	}

	remoteCondsPtr := statusConditionsPtr(remote)
	if remoteCondsPtr == nil {
		return nil
	}
	remoteConds := *remoteCondsPtr
	caughtUp := workloadCaughtUp(remote, mgmtObj.GetGeneration())
	mgmtGen := mgmtObj.GetGeneration()

	_, err = r.updateStatusWithRetry(ctx, mgmtObj, func(o client.Object) {
		if conds := statusConditionsPtr(o); conds != nil {
			for _, c := range desiredMirroredConditions(remoteConds, caughtUp, mgmtGen) {
				apimeta.SetStatusCondition(conds, c)
			}
		}
		// Derived fields are only authoritative once the workload has observed
		// the current spec; until then keep the last mirrored values.
		if caughtUp {
			mirrorDerivedStatus(o, remote)
		}
	})
	if err != nil {
		return err
	}
	log.V(1).Info("Mirrored workload status back to management", "kind", kind, "name", name, "caughtUp", caughtUp)
	return nil
}

// mirrorDerivedStatus copies the read-only, purely-derived status fields
// (network CIDRs and VRF lists) from the workload copy (src) onto the management
// object (dst). These are computed by the intent compiler on the workload
// cluster from the referenced Network/Destinations; the management cluster has
// no compiler, so it receives them via this sync-back. Only these specific
// fields are copied — never addresses/interfaceName/etc.
func mirrorDerivedStatus(dst, src client.Object) {
	switch d := dst.(type) {
	case *nc.Layer2Attachment:
		if s, ok := src.(*nc.Layer2Attachment); ok {
			d.Status.NetworkIPv4 = s.Status.NetworkIPv4
			d.Status.NetworkIPv6 = s.Status.NetworkIPv6
			d.Status.VRFs = s.Status.VRFs
		}
	case *nc.Inbound:
		if s, ok := src.(*nc.Inbound); ok {
			d.Status.VRFs = s.Status.VRFs
		}
	case *nc.Outbound:
		if s, ok := src.(*nc.Outbound); ok {
			d.Status.VRFs = s.Status.VRFs
		}
	case *nc.PodNetwork:
		if s, ok := src.(*nc.PodNetwork); ok {
			d.Status.NetworkIPv4 = s.Status.NetworkIPv4
			d.Status.NetworkIPv6 = s.Status.NetworkIPv6
			d.Status.VRFs = s.Status.VRFs
		}
	case *nc.BGPPeering:
		if s, ok := src.(*nc.BGPPeering); ok {
			d.Status.VRFs = s.Status.VRFs
		}
	case *nc.NodeAttachment:
		if s, ok := src.(*nc.NodeAttachment); ok {
			d.Status.VRFs = s.Status.VRFs
		}
	}
}

// writeStatusConditions applies the desired conditions onto obj's status and
// persists them via the status subresource when they change, retrying on
// optimistic-lock conflicts. The returned bool reports whether a write happened
// (so callers can gate side effects such as events on real changes).
func (r *Controller) writeStatusConditions(ctx context.Context, obj client.Object, desired []metav1.Condition) (bool, error) {
	return r.updateStatusWithRetry(ctx, obj, func(o client.Object) {
		conds := statusConditionsPtr(o)
		if conds == nil {
			return
		}
		for i := range desired {
			apimeta.SetStatusCondition(conds, desired[i])
		}
	})
}

// updateStatusWithRetry applies mutate to obj and, when the object's Status
// actually changed, persists it via the status subresource — retrying on
// optimistic-lock conflicts by re-fetching and re-applying. Only the status
// subresource is written, so spec is never touched. Returns whether a write
// occurred. mutate must be idempotent (it is re-run after a conflict).
func (r *Controller) updateStatusWithRetry(ctx context.Context, obj client.Object, mutate func(client.Object)) (bool, error) {
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		before, ok := obj.DeepCopyObject().(client.Object)
		if !ok {
			return false, fmt.Errorf("DeepCopyObject did not return client.Object for %s", obj.GetName())
		}
		mutate(obj)
		if statusEqual(before, obj) {
			return false, nil
		}
		err := r.Client.Status().Update(ctx, obj)
		if err == nil {
			return true, nil
		}
		if !apierrors.IsConflict(err) {
			return false, fmt.Errorf("updating status for %s/%s: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
		}
		// Conflict: re-fetch to pick up the current resourceVersion and retry.
		fresh, ok := obj.DeepCopyObject().(client.Object)
		if !ok {
			return false, fmt.Errorf("DeepCopyObject did not return client.Object for %s", obj.GetName())
		}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(obj), fresh); err != nil {
			return false, fmt.Errorf("re-fetching %s/%s for status update: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
		}
		obj = fresh
	}
	return false, fmt.Errorf("status update conflict after %d retries for %s/%s", maxRetries, obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
}

// statusEqual reports whether the Status fields of a and b are deeply equal,
// via reflection so it works generically across all intent CRD types.
func statusEqual(a, b client.Object) bool {
	av := reflect.ValueOf(a).Elem().FieldByName("Status")
	bv := reflect.ValueOf(b).Elem().FieldByName("Status")
	if !av.IsValid() || !bv.IsValid() {
		return true
	}
	return reflect.DeepEqual(av.Interface(), bv.Interface())
}

// blockOnMultipleRemotes surfaces the multiple-workload-cluster misconfiguration
// on every intent resource in the namespace: it sets Ready=False (reason
// MultipleWorkloadClusters) and emits a Warning event, so the block is
// discoverable via `kubectl get`/`describe`, not only in the controller log.
// Events fire only when the condition actually transitions, so a namespace held
// in this state across periodic requeues does not spam the event stream.
func (r *Controller) blockOnMultipleRemotes(ctx context.Context, namespace string, count int) error {
	const reason = "MultipleWorkloadClusters"
	message := fmt.Sprintf("Namespace maps to %d workload clusters; refusing to sync until exactly one remains", count)

	for _, list := range intentCRDLists() {
		if err := r.Client.List(ctx, list, client.InNamespace(namespace)); err != nil {
			return fmt.Errorf("listing %T while blocking namespace: %w", list, err)
		}
		items := extractItems(list)
		for i := range items {
			obj := items[i]
			if !obj.GetDeletionTimestamp().IsZero() {
				continue
			}
			cond := metav1.Condition{
				Type:               nc.ConditionTypeReady,
				Status:             metav1.ConditionFalse,
				Reason:             reason,
				Message:            message,
				ObservedGeneration: obj.GetGeneration(),
			}
			changed, err := r.writeStatusConditions(ctx, obj, []metav1.Condition{cond})
			if err != nil {
				return err
			}
			if changed && r.Recorder != nil {
				// action "RefusingSync" describes the operation the reason relates
				// to; note is passed as a literal to avoid interpreting stray %.
				r.Recorder.Eventf(obj, nil, corev1.EventTypeWarning, reason, "RefusingSync", "%s", message)
			}
		}
	}
	return nil
}

// desiredMirroredConditions filters the workload conditions down to the
// allowlist and rewrites each condition's observedGeneration to the management
// object's generation (the workload records it against its own, unrelated
// generation). When the workload has not yet caught up to the intent we pushed,
// every mirrored condition is reported as non-authoritative for the current
// generation instead of the stale workload value: Ready=False/Progressing and
// Resolved=Unknown/Progressing. Downgrading both keeps them consistent with the
// generation gate — otherwise a previously mirrored Resolved=True (with an old
// observedGeneration) would linger while Ready is Progressing for a newer
// generation.
func desiredMirroredConditions(remoteConds []metav1.Condition, caughtUp bool, mgmtGen int64) []metav1.Condition {
	if !caughtUp {
		const msg = "Workload cluster has not yet observed the latest synced spec"
		return []metav1.Condition{
			{
				Type:               nc.ConditionTypeReady,
				Status:             metav1.ConditionFalse,
				Reason:             "Progressing",
				Message:            msg,
				ObservedGeneration: mgmtGen,
			},
			{
				Type:               nc.ConditionTypeResolved,
				Status:             metav1.ConditionUnknown,
				Reason:             "Progressing",
				Message:            msg,
				ObservedGeneration: mgmtGen,
			},
		}
	}

	var out []metav1.Condition
	for i := range remoteConds {
		c := remoteConds[i]
		if !mirroredConditionTypes[c.Type] {
			continue
		}
		c.ObservedGeneration = mgmtGen
		out = append(out, c)
	}
	return out
}

// workloadCaughtUp reports whether the workload copy's status reflects the
// current management intent. Both must hold:
//   - the remote object carries the spec built from the current management
//     generation (source-generation annotation), and
//   - the workload compiler has observed the remote's current spec
//     (status.observedGeneration == metadata.generation on the remote).
//
// Until both are true the remote status is stale relative to the intent and
// must not be mirrored as authoritative.
func workloadCaughtUp(remote client.Object, mgmtGen int64) bool {
	if remote.GetAnnotations()[annotationSourceGeneration] != strconv.FormatInt(mgmtGen, 10) {
		return false
	}
	observed, ok := statusObservedGeneration(remote)
	if !ok {
		return false
	}
	return observed == remote.GetGeneration()
}

// statusConditionsPtr returns a pointer to obj's status.Conditions slice via
// reflection, or nil if the object has no such field. This keeps status
// sync-back generic across every intent CRD without a per-type switch, mirroring
// the reflection approach used by overlayBody and extractItems.
func statusConditionsPtr(obj client.Object) *[]metav1.Condition {
	v := reflect.ValueOf(obj)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return nil
	}
	status := v.Elem().FieldByName("Status")
	if !status.IsValid() {
		return nil
	}
	conds := status.FieldByName("Conditions")
	if !conds.IsValid() || !conds.CanAddr() {
		return nil
	}
	ptr, ok := conds.Addr().Interface().(*[]metav1.Condition)
	if !ok {
		return nil
	}
	return ptr
}

// statusObservedGeneration reads obj's status.ObservedGeneration via reflection.
// The bool is false when the object has no such int64 field.
func statusObservedGeneration(obj client.Object) (int64, bool) {
	v := reflect.ValueOf(obj)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return 0, false
	}
	status := v.Elem().FieldByName("Status")
	if !status.IsValid() {
		return 0, false
	}
	og := status.FieldByName("ObservedGeneration")
	if !og.IsValid() || og.Kind() != reflect.Int64 {
		return 0, false
	}
	return og.Int(), true
}

// buildRemoteObject creates the desired remote object from the mgmt-side source.
func (r *Controller) buildRemoteObject(src client.Object, sourceNamespace string, ipamAddrs *ipamAllocations) client.Object {
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

	// Set sync labels/annotations. Strip GitOps inventory and source-side
	// Helm/Flux ownership metadata first so the workload copy is not adopted by
	// the management cluster's tooling.
	labels := stripMetadataKeys(stripGitOpsKeys(dst.GetLabels()), ownershipLabelKeys)
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[labelManagedBy] = labelManagedByValue
	dst.SetLabels(labels)

	annotations := stripMetadataKeys(stripGitOpsKeys(dst.GetAnnotations()), ownershipAnnotationKeys)
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[annotationSourceNS] = sourceNamespace
	// Record the management generation this spec was built from so status
	// sync-back can tell whether the workload has caught up to the current intent.
	annotations[annotationSourceGeneration] = strconv.FormatInt(src.GetGeneration(), 10)
	annotations[annotationSSAAdopted] = annotationSSAAdoptedValue
	// Remove system annotations.
	delete(annotations, lastAppliedConfigurationAnn)
	dst.SetAnnotations(annotations)

	// Record the keys we own so the next sync can prune the ones we drop.
	recordManagedKeys(dst)

	// IPAM promotion: copy status.addresses → spec.addresses for Inbound/Outbound.
	r.promoteIPAMAddresses(dst, ipamAddrs)
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
//
// It also returns the freshly-allocated addresses in memory, keyed by object
// name. The forward sync must NOT rely on re-reading status.addresses from the
// (cached) client for the promotion: the allocator writes status via a direct
// API update, but the reconciler's List reads are served from an informer cache
// that lags that write, and — since status-only updates are filtered by
// syncCRDPredicate — nothing re-triggers a reconcile to pick up the fresh cache.
// Threading the in-memory allocations through makes the promotion work within
// the same reconcile, independent of cache timing.
func (r *Controller) reconcileIPAM(ctx context.Context, namespace string) (*ipamAllocations, error) {
	inboundList := &nc.InboundList{}
	if err := r.Client.List(ctx, inboundList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing Inbounds for IPAM: %w", err)
	}
	outboundList := &nc.OutboundList{}
	if err := r.Client.List(ctx, outboundList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing Outbounds for IPAM: %w", err)
	}
	networkList := &nc.NetworkList{}
	if err := r.Client.List(ctx, networkList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing Networks for IPAM: %w", err)
	}
	l2aList := &nc.Layer2AttachmentList{}
	if err := r.Client.List(ctx, l2aList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing Layer2Attachments for IPAM: %w", err)
	}

	fetched := &resolver.FetchedResources{
		Inbounds:          inboundList.Items,
		Outbounds:         outboundList.Items,
		Networks:          networkList.Items,
		Layer2Attachments: l2aList.Items,
	}
	networks := resolver.ResolveNetworks(fetched.Networks)

	if err := r.IPAMAllocator.ReconcileAllocations(ctx, fetched, networks); err != nil {
		return nil, fmt.Errorf("IPAM allocation: %w", err)
	}

	// Snapshot the (now freshly-allocated) addresses from the in-memory objects
	// the allocator just mutated, so the forward sync promotes them without a
	// cache round-trip.
	allocs := newIPAMAllocations()
	for i := range fetched.Inbounds {
		if a := fetched.Inbounds[i].Status.Addresses; a != nil {
			allocs.inbound[fetched.Inbounds[i].Name] = a
		}
	}
	for i := range fetched.Outbounds {
		if a := fetched.Outbounds[i].Status.Addresses; a != nil {
			allocs.outbound[fetched.Outbounds[i].Name] = a
		}
	}
	return allocs, nil
}

// promoteIPAMAddresses copies status.addresses into spec.addresses for Inbound/Outbound
// so the workload operator sees pre-allocated IPs from mgmt-cluster IPAM.
//
// The freshly-allocated addresses are taken from allocs (the in-memory result of
// the IPAM allocation performed earlier in this reconcile) when available, so a
// lagging read cache cannot hide a just-allocated address; otherwise the object's
// own status.addresses (from a previous reconcile) is used.
//
// IPAM stores allocated addresses as bare host IPs (e.g. "10.100.148.1"), but
// spec.addresses is a CIDR-typed field: the vinbound/voutbound webhooks validate
// each entry with net.ParseCIDR. A bare IP therefore gets rejected on the remote
// cluster. Inbound/Outbound addresses are advertised as individual routed hosts,
// so each allocated IP is promoted to its /32 (IPv4) or /128 (IPv6) host CIDR.
func (*Controller) promoteIPAMAddresses(obj client.Object, allocs *ipamAllocations) {
	switch v := obj.(type) {
	case *nc.Inbound:
		addrs := allocs.inboundAddresses(v.Name, v.Status.Addresses)
		if v.Spec.Addresses == nil && hasAddresses(addrs) {
			v.Spec.Addresses = hostCIDRAllocation(addrs)
			v.Spec.Count = nil // Switch from count → manual mode
		}
	case *nc.Outbound:
		addrs := allocs.outboundAddresses(v.Name, v.Status.Addresses)
		if v.Spec.Addresses == nil && hasAddresses(addrs) {
			v.Spec.Addresses = hostCIDRAllocation(addrs)
			v.Spec.Count = nil
		}
	}
}

// hasAddresses reports whether a is non-nil and carries at least one address.
func hasAddresses(a *nc.AddressAllocation) bool {
	return a != nil && (len(a.IPv4) > 0 || len(a.IPv6) > 0)
}

// hostCIDRAllocation returns a copy of src with every bare host IP normalised to
// its host CIDR (/32 for IPv4, /128 for IPv6). Entries that already carry a
// prefix length are left untouched so explicit subnets survive round-trips.
func hostCIDRAllocation(src *nc.AddressAllocation) *nc.AddressAllocation {
	out := &nc.AddressAllocation{}
	for _, addr := range src.IPv4 {
		out.IPv4 = append(out.IPv4, toHostCIDR(addr))
	}
	for _, addr := range src.IPv6 {
		out.IPv6 = append(out.IPv6, toHostCIDR(addr))
	}
	return out
}

// toHostCIDR normalises a single address to a host CIDR. A value that already
// contains a prefix length is returned unchanged; a bare IP gets /32 (IPv4) or
// /128 (IPv6). A value that parses as neither is returned as-is so the webhook
// surfaces the original malformed input rather than a mangled one.
func toHostCIDR(addr string) string {
	if strings.Contains(addr, "/") {
		return addr
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return addr
	}
	if ip.To4() != nil {
		return addr + "/32"
	}
	return addr + "/128"
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
	if err := overlayBody(existing, desired); err != nil {
		return fmt.Errorf("overlaying desired state onto %s/%s for SSA adoption: %w",
			desired.GetNamespace(), desired.GetName(), err)
	}
	reconcileManagedMetadata(existing, desired)
	existing.SetManagedFields(nil)
	if err := remoteClient.Update(ctx, existing); err != nil {
		return fmt.Errorf("adopting legacy remote object %s/%s before server-side apply: %w",
			desired.GetNamespace(), desired.GetName(), err)
	}
	return nil
}

func (r *Controller) applyRemoteDesired(ctx context.Context, remoteClient client.Client, desired client.Object) error {
	unstructuredDesired, err := r.buildApplyObject(desired)
	if err != nil {
		return err
	}
	if err := remoteClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(unstructuredDesired),
		client.FieldOwner(remoteFieldManager), client.ForceOwnership); err != nil {
		return fmt.Errorf("server-side applying remote object %s/%s: %w", desired.GetNamespace(), desired.GetName(), err)
	}
	return nil
}

func (r *Controller) buildApplyObject(desired client.Object) (*unstructured.Unstructured, error) {
	if err := r.prepareApplyObject(desired); err != nil {
		return nil, err
	}
	objMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(desired)
	if err != nil {
		return nil, fmt.Errorf("converting %s/%s to unstructured apply configuration: %w",
			desired.GetNamespace(), desired.GetName(), err)
	}

	gvk := desired.GetObjectKind().GroupVersionKind()
	metadata := map[string]interface{}{
		"name":      desired.GetName(),
		"namespace": desired.GetNamespace(),
	}
	if labels := desired.GetLabels(); len(labels) > 0 {
		metadata["labels"] = stringMapToUnstructured(labels)
	}
	if annotations := desired.GetAnnotations(); len(annotations) > 0 {
		metadata["annotations"] = stringMapToUnstructured(annotations)
	}

	applyMap := map[string]interface{}{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata":   metadata,
	}
	if spec, ok := objMap["spec"]; ok {
		applyMap["spec"] = spec
	} else if _, isSecret := desired.(*corev1.Secret); !isSecret {
		applyMap["spec"] = map[string]interface{}{}
	}
	if _, ok := desired.(*corev1.Secret); ok {
		if typ, ok := objMap["type"]; ok {
			applyMap["type"] = typ
		}
		if data, ok := objMap["data"]; ok {
			applyMap["data"] = data
		} else {
			applyMap["data"] = map[string]interface{}{}
		}
	}

	unstructuredDesired := &unstructured.Unstructured{Object: applyMap}
	unstructuredDesired.SetGroupVersionKind(gvk)
	return unstructuredDesired, nil
}

func stringMapToUnstructured(in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
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

// overlayBody copies the source-of-truth payload from src onto dst without
// touching dst's server-managed metadata (resourceVersion, UID, foreign labels,
// etc.). For CRDs that means the Spec field; for Secrets it overlays Data so
// workload-local keys survive SSA adoption.
func overlayBody(dst, src client.Object) error {
	if dstSecret, ok := dst.(*corev1.Secret); ok {
		srcSecret, ok := src.(*corev1.Secret)
		if !ok {
			return fmt.Errorf("type mismatch overlaying %T onto *corev1.Secret", src)
		}
		data := make(map[string][]byte, len(dstSecret.Data)+len(srcSecret.Data))
		for k, v := range dstSecret.Data {
			data[k] = append([]byte(nil), v...)
		}
		for k, v := range srcSecret.Data {
			data[k] = append([]byte(nil), v...)
		}
		dstSecret.Data = data
		dstSecret.StringData = srcSecret.StringData
		if dstSecret.Type == "" {
			dstSecret.Type = srcSecret.Type
		}
		return nil
	}

	dstSpec := reflect.ValueOf(dst).Elem().FieldByName("Spec")
	srcSpec := reflect.ValueOf(src).Elem().FieldByName("Spec")
	if !dstSpec.IsValid() || !srcSpec.IsValid() {
		return fmt.Errorf("object %T has no Spec field to overlay", dst)
	}
	if !dstSpec.CanSet() {
		return fmt.Errorf("spec field on %T is not settable", dst)
	}
	dstSpec.Set(srcSpec)
	return nil
}

// reconcileManagedMetadata merges the labels and annotations this controller
// manages (carried on src) into dst, prunes the keys we managed on a previous
// sync but no longer set, and strips GitOps/Flux-owned keys outright. Foreign
// keys we never propagated ourselves are otherwise preserved. Together this ends
// the label war and converges existing objects:
//
//   - a source label/annotation that was deleted upstream disappears from the
//     remote instead of lingering forever;
//   - a Flux/GitOps key is removed whether we propagated it before (old
//     full-Update code) or a controller stamped it on the workload cluster — our
//     synced objects are not part of any Flux inventory, so those keys never
//     belong on them. If a live Flux genuinely owns the object it will simply
//     reapply, but the patch helper only emits the keys we actually change, so
//     unrelated foreign metadata is still never touched.
//
// The set of keys we own is read back from the tracking annotations that
// recordManagedKeys stamped on the previous sync (present on dst) and rewritten
// from the freshly built desired object (src).
func reconcileManagedMetadata(dst, src client.Object) {
	prevLabelKeys := parseKeyList(dst.GetAnnotations()[annotationManagedLabels])
	prevAnnotationKeys := parseKeyList(dst.GetAnnotations()[annotationManagedAnnotations])

	dst.SetLabels(mergeAndPrune(dst.GetLabels(), src.GetLabels(), prevLabelKeys, ownershipLabelKeys))
	dst.SetAnnotations(mergeAndPrune(dst.GetAnnotations(), src.GetAnnotations(), prevAnnotationKeys, ownershipAnnotationKeys))
}

// mergeAndPrune overlays the keys we manage now (desired) onto existing, removes
// keys we managed on the previous sync (prevManaged) that are no longer desired,
// and drops any GitOps/Flux-owned key except remote-side ownership keys that
// belong to the workload cluster's manager. Foreign keys we never managed and
// that are not GitOps-owned are left untouched.
func mergeAndPrune(existing, desired map[string]string, prevManaged []string, preserveKeys map[string]struct{}) map[string]string {
	out := make(map[string]string, len(existing)+len(desired))
	for k, v := range existing {
		out[k] = v
	}
	// Drop keys we used to own but no longer set.
	for _, k := range prevManaged {
		if _, stillManaged := desired[k]; !stillManaged {
			if _, preserve := preserveKeys[k]; preserve {
				continue
			}
			delete(out, k)
		}
	}
	// Drop GitOps-owned keys outright. buildRemoteObject never propagates them,
	// so any present here were copied by the old full-Update code or stamped by a
	// controller on the workload cluster; neither belongs on an object the sync
	// operator owns. A live Flux would just reapply, but these synced objects are
	// not part of any Flux inventory.
	for k := range out {
		if hasAnyPrefix(k, gitOpsKeyPrefixes) {
			if _, preserve := preserveKeys[k]; preserve {
				continue
			}
			delete(out, k)
		}
	}
	// Assert the keys we own now.
	for k, v := range desired {
		out[k] = v
	}
	return out
}

// recordManagedKeys stamps the tracking annotations that list the label and
// annotation keys this controller owns on obj. The tracking annotations are
// never counted as part of the managed set (we always rewrite them), so they are
// dropped before the keys are enumerated and re-added afterwards.
func recordManagedKeys(obj client.Object) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	delete(annotations, annotationManagedLabels)
	delete(annotations, annotationManagedAnnotations)

	labelKeys := strings.Join(sortedKeys(obj.GetLabels()), ",")
	annotationKeys := strings.Join(sortedKeys(annotations), ",")

	annotations[annotationManagedLabels] = labelKeys
	annotations[annotationManagedAnnotations] = annotationKeys
	obj.SetAnnotations(annotations)
}

// sortedKeys returns the keys of m in deterministic order so the tracking
// annotations are stable across syncs and never generate spurious patches.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// parseKeyList splits a comma-separated tracking annotation value back into keys.
func parseKeyList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// stripGitOpsKeys returns a copy of in with all GitOps-owned keys removed.
func stripGitOpsKeys(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if hasAnyPrefix(k, gitOpsKeyPrefixes) {
			continue
		}
		out[k] = v
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
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
				missingSrc := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
				if err := r.deleteRemote(ctx, remoteClient, missingSrc); err != nil {
					return fmt.Errorf("deleting remote BGP auth Secret %s/%s after source Secret disappeared: %w", namespace, name, err)
				}
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
	// Record the keys we own so a later sync can prune the ones we drop, keeping
	// synced Secrets on the same convergence path as the intent CRDs.
	recordManagedKeys(dst)
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
		return syncNamespaceRequest(obj.GetNamespace())
	}
	for i := range bpList.Items {
		bp := &bpList.Items[i]
		if !bp.GetDeletionTimestamp().IsZero() ||
			bp.Spec.AuthSecretRef == nil ||
			bp.Spec.AuthSecretRef.Name != obj.GetName() {
			continue
		}
		return syncNamespaceRequest(obj.GetNamespace())
	}
	return nil
}

func syncNamespaceRequest(namespace string) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: namespace,
			Name:      syncRequestName,
		},
	}}
}

func indexBGPAuthSecretRef(obj client.Object) []string {
	bp, ok := obj.(*nc.BGPPeering)
	if !ok || bp.Spec.AuthSecretRef == nil || bp.Spec.AuthSecretRef.Name == "" {
		return nil
	}
	return []string{bp.Spec.AuthSecretRef.Name}
}

func bgpAuthSecretPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return e.Object != nil
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return bgpAuthSecretContentChanged(e.ObjectOld, e.ObjectNew)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return e.Object != nil
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return e.Object != nil
		},
	}
}

func bgpAuthSecretContentChanged(oldObj, newObj client.Object) bool {
	if oldObj == nil || newObj == nil {
		return false
	}
	oldSecret, oldOK := oldObj.(*corev1.Secret)
	newSecret, newOK := newObj.(*corev1.Secret)
	if !oldOK || !newOK {
		return true
	}
	return oldSecret.Type != newSecret.Type || !reflect.DeepEqual(oldSecret.Data, newSecret.Data)
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

	builder := ctrl.NewControllerManagedBy(mgr).
		Named("sync-controller")

	for _, obj := range intentCRDTypes() {
		builder = builder.Watches(obj, enqueueNS, ctrlbuilder.WithPredicates(syncCRDPredicate()))
	}
	builder = builder.Watches(&corev1.Secret{}, secretToSyncNamespace, ctrlbuilder.WithPredicates(bgpAuthSecretPredicate()))

	if err := builder.Complete(r); err != nil {
		return fmt.Errorf("setting up sync controller: %w", err)
	}
	return nil
}

// syncCRDPredicate filters the intent-CRD watch down to events that require a
// (re)sync, so the controller does not reconcile in response to its own status
// sync-back writes. Reconciling on every status write would form a feedback
// loop — status write → watch event → reconcile → status write — that storms
// the reconciler and floods the workload-cluster API (rate-limited), delaying
// the forward spec sync.
//
// Forward sync depends only on the spec (generation) and the managed
// labels/annotations; status sync-back is driven by the periodic requeue, not
// by watch events. Deletion is surfaced explicitly (deletionTimestamp is a
// metadata change that does not bump the generation) so remote objects are
// still cleaned up promptly rather than only on the next poll.
func syncCRDPredicate() predicate.Predicate {
	return predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.LabelChangedPredicate{},
		predicate.AnnotationChangedPredicate{},
		predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				if e.ObjectOld == nil || e.ObjectNew == nil {
					return false
				}
				// Fire when the object first enters deletion.
				return e.ObjectOld.GetDeletionTimestamp() == nil &&
					e.ObjectNew.GetDeletionTimestamp() != nil
			},
		},
	)
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
