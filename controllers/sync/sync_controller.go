package sync

import (
	"context"
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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/ipam"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

const (
	finalizerName       = "network-sync.telekom.com/cleanup"
	labelManagedBy      = "network-sync.telekom.com/managed-by"
	labelManagedByValue = "network-sync"
	annotationSourceNS  = "network-sync.telekom.com/source-namespace"

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

	// A single workload cluster per namespace is required for status sync-back:
	// with several, one management object cannot carry a single authoritative
	// status, so we mirror nothing back (forward sync below still fans out to
	// every workload cluster).
	singleRemote := len(remoteClients) == 1

	// List and sync every intent CRD type in this namespace.
	for _, list := range intentCRDLists() {
		if err := r.Client.List(ctx, list, client.InNamespace(req.Namespace)); err != nil {
			return ctrl.Result{}, fmt.Errorf("listing %T: %w", list, err)
		}
		items := extractItems(list)
		for i := range items {
			for _, remoteClient := range remoteClients {
				if err := r.syncObject(ctx, log, remoteClient, items[i]); err != nil {
					return ctrl.Result{}, err
				}
			}
			// Mirror Ready/Resolved conditions from the workload copy back onto
			// the management object, reusing the object already listed above so
			// status sync-back adds no extra LIST traffic. Objects mid-deletion
			// are handled by the forward path and are not resurrected.
			if singleRemote && items[i].GetDeletionTimestamp().IsZero() {
				if err := r.syncObjectStatusBack(ctx, log, remoteClients[0], items[i]); err != nil {
					return ctrl.Result{}, err
				}
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

	// Requeue only when status sync-back is active, so workload status changes
	// are pulled back periodically (the controller does not watch workload
	// clusters). With multiple workload clusters there is nothing to poll back,
	// so a change-driven reconcile is enough and we avoid needless full resyncs.
	if singleRemote {
		return ctrl.Result{RequeueAfter: statusPollInterval}, nil
	}
	log.V(1).Info("Multiple workload clusters in namespace; skipping status sync-back",
		"count", len(remoteClients))
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
			controllerutil.RemoveFinalizer(obj, finalizerName)
			if err := r.Client.Update(ctx, obj); err != nil {
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
	if remote == nil {
		return fmt.Errorf("buildRemoteObject returned nil for %s/%s", kind, name)
	}
	log.V(1).Info("Syncing to remote", "kind", kind, "name", name)
	return r.applyRemote(ctx, remoteClient, remote)
}

// syncObjectStatusBack reads the workload copy of a single intent object and
// mirrors its Ready/Resolved conditions back onto the management object. It is
// the reverse of the spec sync — workload → management — and is deliberately
// restricted to mirroredConditionTypes so a lagging or empty workload status can
// never overwrite management-owned status fields (addresses, interfaceName,
// workloadASNumber) that feed the workload spec.
func (r *Controller) syncObjectStatusBack(ctx context.Context, log logr.Logger, remoteClient client.Client, mgmtObj client.Object) error {
	kind := mgmtObj.GetObjectKind().GroupVersionKind().Kind
	name := mgmtObj.GetName()

	mgmtConds := statusConditionsPtr(mgmtObj)
	if mgmtConds == nil {
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

	remoteConds := statusConditionsPtr(remote)
	if remoteConds == nil {
		return nil
	}

	caughtUp := workloadCaughtUp(remote, mgmtObj.GetGeneration())
	desired := desiredMirroredConditions(*remoteConds, caughtUp, mgmtObj.GetGeneration())
	if len(desired) == 0 {
		return nil
	}

	if err := r.writeMirroredConditions(ctx, mgmtObj, desired); err != nil {
		return err
	}
	log.V(1).Info("Mirrored workload status back to management", "kind", kind, "name", name, "caughtUp", caughtUp)
	return nil
}

// writeMirroredConditions applies the desired conditions onto obj's status and
// persists them via the status subresource, retrying on optimistic-lock
// conflicts. Only the status subresource is written, so spec is never touched.
// SetStatusCondition is a no-op when a condition is unchanged, so a stable
// workload status produces no writes and no lastTransitionTime flapping.
func (r *Controller) writeMirroredConditions(ctx context.Context, obj client.Object, desired []metav1.Condition) error {
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		conds := statusConditionsPtr(obj)
		if conds == nil {
			return nil
		}
		changed := false
		for i := range desired {
			if apimeta.SetStatusCondition(conds, desired[i]) {
				changed = true
			}
		}
		if !changed {
			return nil
		}
		err := r.Client.Status().Update(ctx, obj)
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return fmt.Errorf("updating status for %s/%s: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
		}
		// Conflict: re-fetch to pick up the current resourceVersion and retry.
		fresh, ok := obj.DeepCopyObject().(client.Object)
		if !ok {
			return fmt.Errorf("DeepCopyObject did not return client.Object for %s", obj.GetName())
		}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(obj), fresh); err != nil {
			return fmt.Errorf("re-fetching %s/%s for status sync-back: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
		}
		obj = fresh
	}
	return fmt.Errorf("status sync-back conflict after %d retries for %s/%s", maxRetries, obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
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
	dst.SetGenerateName("")
	dst.SetSelfLink("")
	dst.SetManagedFields(nil)
	dst.SetFinalizers(nil)      // Remote objects don't need our finalizer
	dst.SetOwnerReferences(nil) // No cross-cluster owner refs

	// Set sync labels/annotations. Strip GitOps (Flux) keys first so that the
	// management cluster's kustomization inventory metadata is not carried over
	// to the workload cluster; otherwise a Flux running there would claim our
	// synced objects and the two controllers would fight over them.
	labels := stripGitOpsKeys(dst.GetLabels())
	labels[labelManagedBy] = labelManagedByValue
	dst.SetLabels(labels)

	annotations := stripGitOpsKeys(dst.GetAnnotations())
	annotations[annotationSourceNS] = sourceNamespace
	// Record the management generation this spec was built from so status
	// sync-back can tell whether the workload has caught up to the current intent.
	annotations[annotationSourceGeneration] = strconv.FormatInt(src.GetGeneration(), 10)
	// Remove system annotations.
	delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
	dst.SetAnnotations(annotations)

	// Record the keys we own so the next sync can prune the ones we drop.
	recordManagedKeys(dst)

	// IPAM promotion: copy status.addresses → spec.addresses for Inbound/Outbound.
	r.promoteIPAMAddresses(dst)

	return dst
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
//
// IPAM stores allocated addresses as bare host IPs (e.g. "10.100.148.1"), but
// spec.addresses is a CIDR-typed field: the vinbound/voutbound webhooks validate
// each entry with net.ParseCIDR. A bare IP therefore gets rejected on the remote
// cluster. Inbound/Outbound addresses are advertised as individual routed hosts,
// so each allocated IP is promoted to its /32 (IPv4) or /128 (IPv6) host CIDR.
func (*Controller) promoteIPAMAddresses(obj client.Object) {
	switch v := obj.(type) {
	case *nc.Inbound:
		if v.Spec.Addresses == nil && v.Status.Addresses != nil &&
			(len(v.Status.Addresses.IPv4) > 0 || len(v.Status.Addresses.IPv6) > 0) {
			v.Spec.Addresses = hostCIDRAllocation(v.Status.Addresses)
			v.Spec.Count = nil // Switch from count → manual mode
		}
	case *nc.Outbound:
		if v.Spec.Addresses == nil && v.Status.Addresses != nil &&
			(len(v.Status.Addresses.IPv4) > 0 || len(v.Status.Addresses.IPv6) > 0) {
			v.Spec.Addresses = hostCIDRAllocation(v.Status.Addresses)
			v.Spec.Count = nil
		}
	}
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

// applyRemote creates the object on the remote cluster if it does not yet exist,
// otherwise it patches only the fields this controller owns (the spec/data plus
// its own managed metadata) using the Cluster API patch helper.
//
// The patch helper snapshots the freshly fetched object, we then mutate that same
// object in place, and Patch emits a minimal before→after merge patch. This is
// what ends the label war with Flux: any label or annotation a GitOps controller
// set on the remote object is part of the fetched "before" state, is never
// touched, and therefore never appears in the emitted patch. The previous full
// client.Update replaced the entire object and clobbered every label the sync
// operator did not itself set, so Flux and the sync operator flapped forever.
func (*Controller) applyRemote(ctx context.Context, remoteClient client.Client, desired client.Object) error {
	existing, ok := desired.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("DeepCopyObject did not return client.Object for %s/%s", desired.GetNamespace(), desired.GetName())
	}
	err := remoteClient.Get(ctx, types.NamespacedName{
		Namespace: desired.GetNamespace(),
		Name:      desired.GetName(),
	}, existing)

	if apierrors.IsNotFound(err) {
		if err := remoteClient.Create(ctx, desired); err != nil {
			return fmt.Errorf("creating remote object %s/%s: %w", desired.GetNamespace(), desired.GetName(), err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting remote object: %w", err)
	}

	// Verify we own this object before mutating it.
	if existing.GetLabels()[labelManagedBy] != labelManagedByValue {
		return fmt.Errorf("remote object %s/%s exists but not managed by us", desired.GetNamespace(), desired.GetName())
	}

	// Snapshot the fetched object, then mutate it in place so the patch helper
	// only diffs the fields we actually change.
	helper, err := patch.NewHelper(existing, remoteClient)
	if err != nil {
		return fmt.Errorf("creating patch helper for %s/%s: %w", desired.GetNamespace(), desired.GetName(), err)
	}
	if err := overlayBody(existing, desired); err != nil {
		return fmt.Errorf("overlaying desired state onto %s/%s: %w", desired.GetNamespace(), desired.GetName(), err)
	}
	reconcileManagedMetadata(existing, desired)

	if err := helper.Patch(ctx, existing); err != nil {
		return fmt.Errorf("patching remote object %s/%s: %w", desired.GetNamespace(), desired.GetName(), err)
	}
	return nil
}

// overlayBody copies the source-of-truth payload from src onto dst without
// touching dst's server-managed metadata (resourceVersion, UID, foreign labels,
// etc.). For CRDs that means the Spec field; for Secrets it means Data/StringData
// (Type is immutable after creation and is only set when unset).
func overlayBody(dst, src client.Object) error {
	if dstSecret, ok := dst.(*corev1.Secret); ok {
		srcSecret, ok := src.(*corev1.Secret)
		if !ok {
			return fmt.Errorf("type mismatch overlaying %T onto *corev1.Secret", src)
		}
		dstSecret.Data = srcSecret.Data
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

	dst.SetLabels(mergeAndPrune(dst.GetLabels(), src.GetLabels(), prevLabelKeys))
	dst.SetAnnotations(mergeAndPrune(dst.GetAnnotations(), src.GetAnnotations(), prevAnnotationKeys))
}

// mergeAndPrune overlays the keys we manage now (desired) onto existing, removes
// keys we managed on the previous sync (prevManaged) that are no longer desired,
// and drops any GitOps/Flux-owned key. Foreign keys we never managed and that are
// not GitOps-owned are left untouched.
func mergeAndPrune(existing, desired map[string]string, prevManaged []string) map[string]string {
	out := make(map[string]string, len(existing)+len(desired))
	for k, v := range existing {
		out[k] = v
	}
	// Drop keys we used to own but no longer set.
	for _, k := range prevManaged {
		if _, stillManaged := desired[k]; !stillManaged {
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

// deleteRemote removes the object from the remote cluster.
func (r *Controller) deleteRemote(ctx context.Context, remoteClient client.Client, src client.Object) error {
	remote, ok := src.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("DeepCopyObject did not return client.Object for %s/%s", src.GetNamespace(), src.GetName())
	}
	remote.SetNamespace(r.remoteNamespace())
	remote.SetResourceVersion("")
	remote.SetUID("")

	err := remoteClient.Delete(ctx, remote)
	if apierrors.IsNotFound(err) {
		return nil // Already gone.
	}
	if err != nil {
		return fmt.Errorf("deleting remote object %s/%s: %w", remote.GetNamespace(), remote.GetName(), err)
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

	desired := map[string]struct{}{}
	for i := range bpList.Items {
		bp := &bpList.Items[i]
		if !bp.GetDeletionTimestamp().IsZero() {
			continue
		}
		if bp.Spec.AuthSecretRef == nil || bp.Spec.AuthSecretRef.Name == "" {
			continue
		}
		desired[bp.Spec.AuthSecretRef.Name] = struct{}{}
	}

	for name := range desired {
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
		if _, keep := desired[s.Name]; keep {
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
				annotationSourceNS: sourceNamespace,
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

// SetupWithManager registers watches for all intent CRD types.
func (r *Controller) SetupWithManager(mgr ctrl.Manager) error {
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
	for i := range items.Len() {
		obj, ok := items.Index(i).Addr().Interface().(client.Object)
		if !ok {
			return nil
		}
		out[i] = obj
	}

	return out
}
