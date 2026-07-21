/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package intent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/assembler"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/builder"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/finalizer"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/ipam"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/legacy"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/status"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/nncnames"
)

const (
	defaultDebounceTime = 1 * time.Second
	// DefaultTimeout is the default API timeout.
	DefaultTimeout = "60s"
)

// Reconciler is the intent-based reconciler that watches all intent CRDs
// and produces NodeNetworkConfig objects per node.
type Reconciler struct {
	logger           logr.Logger
	debouncer        *debounce.Debouncer
	client           client.Client
	timeout          time.Duration
	namespace        string
	builders         []builder.Builder
	finalizerManager *finalizer.Manager
	statusUpdater    *status.Updater
	ipamAllocator    *ipam.Allocator
	legacyDetector   *legacy.Detector
}

// NewReconciler creates a new intent reconciler.
// The namespace parameter restricts which namespace intent CRDs are read from.
// An empty string means all namespaces (cluster-wide).
func NewReconciler(clusterClient client.Client, logger logr.Logger, timeout time.Duration, namespace string) (*Reconciler, error) {
	r := &Reconciler{
		logger:    logger,
		timeout:   timeout,
		client:    clusterClient,
		namespace: namespace,
		builders: []builder.Builder{
			builder.NewL2ABuilder(),
			builder.NewInboundBuilder(),
			builder.NewOutboundBuilder(),
			builder.NewPodNetworkBuilder(),
			builder.NewBGPPeeringBuilder(),
			builder.NewCollectorBuilder(),
			builder.NewMirrorBuilder(),
			builder.NewAnnouncementBuilder(),
			builder.NewNodeAttachmentBuilder(),
			builder.NewSBRBuilder(),
		},
		finalizerManager: finalizer.NewManager(clusterClient, logger),
		statusUpdater:    status.NewUpdater(clusterClient, logger),
		ipamAllocator:    ipam.NewAllocator(clusterClient, logger),
		legacyDetector:   legacy.NewDetector(clusterClient, logger),
	}

	r.debouncer = debounce.NewDebouncer(r.ReconcileDebounced, defaultDebounceTime, logger)

	return r, nil
}

// Reconcile triggers the debounced reconciliation.
func (r *Reconciler) Reconcile(ctx context.Context) {
	r.debouncer.Debounce(ctx)
}

// ReconcileDebounced is the main reconciliation logic executed after debounce.
func (r *Reconciler) ReconcileDebounced(ctx context.Context) error {
	r.logger.Info("starting intent reconciliation")

	timeoutCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// 0. Detect legacy CRD conflicts (non-blocking).
	if conflicts, err := r.legacyDetector.DetectConflicts(timeoutCtx); err != nil {
		r.logger.Error(err, "legacy conflict detection failed")
	} else if len(conflicts) > 0 {
		r.logger.Info("legacy CRDs detected — reconciliation continues", "conflicts", len(conflicts))
	}

	// 1. Fetch all intent CRDs + nodes.
	fetched, err := r.fetchAll(timeoutCtx)
	if err != nil {
		return fmt.Errorf("failed to fetch intent resources: %w", err)
	}

	// 1b. Clean up orphaned NNCs and NodeNetplanConfigs (nodes that no longer exist).
	if err := r.cleanupOrphanedNNCs(timeoutCtx, fetched.Nodes); err != nil {
		r.logger.Error(err, "orphaned NNC cleanup failed")
	}
	if err := r.cleanupOrphanedNetplanConfigs(timeoutCtx, fetched.Nodes); err != nil {
		r.logger.Error(err, "orphaned NodeNetplanConfig cleanup failed")
	}

	// 2. Reconcile in-use finalizers.
	if err := r.finalizerManager.ReconcileFinalizers(timeoutCtx, fetched); err != nil {
		r.logger.Error(err, "finalizer reconciliation failed")
		// Continue — finalizer failures should not block config generation.
	}

	// 3. Resolve references (VRF, Network, Destination name → data).
	resolved, err := resolver.ResolveAll(fetched)
	if err != nil {
		return fmt.Errorf("failed to resolve references: %w", err)
	}

	// 4. IPAM allocation for count-mode Inbound/Outbound (before builders).
	if err := r.ipamAllocator.ReconcileAllocations(timeoutCtx, fetched, resolved.Networks); err != nil {
		r.logger.Error(err, "IPAM allocation failed")
		// Continue — partial allocation is acceptable.
	}

	// 4b. Per-Collector loopback subnet allocation. Each Collector carries
	// spec.mirrorVRF.loopback.subnet; the controller allocates one host
	// address per in-scope node and persists the map in
	// Collector.status.nodeAddresses. The Collector builder later uses
	// these addresses as the per-node loopback source IPs.
	r.reconcileCollectorAddresses(timeoutCtx, fetched)

	// 5. Run all builders → per-node contributions. A builder failure must not
	// abort the whole pass: builders isolate per-resource data errors internally,
	// so a returned error is unexpected. When one occurs, skip applying the
	// (potentially incomplete) contribution set — fail closed, preserving the
	// last-good NNC — but still run the status update so the failure surfaces.
	contributions := make(map[string][]*builder.NodeContribution) // nodeName → contributions
	buildFailed := false
	report := builder.NewBuildReport()
	buildCtx := builder.WithReport(ctx, report)
	for _, b := range r.builders {
		nodeContribs, err := b.Build(buildCtx, resolved)
		if err != nil {
			r.logger.Error(err, "builder failed", "builder", b.Name())
			buildFailed = true
			continue
		}
		for nodeName, contrib := range nodeContribs {
			contributions[nodeName] = append(contributions[nodeName], contrib)
		}
	}

	// 6-8. Assemble and apply NNC + netplan per node. Skipped when a builder
	// failed, because an incomplete contribution set could push partially-wired
	// VRFs (e.g. an IRB without its BGP/redistribute config) to the fabric.
	if buildFailed {
		r.logger.Info("skipping NodeNetworkConfig apply due to builder failure; preserving last-good config")
	} else {
		r.applyNodeConfigs(timeoutCtx, fetched, contributions)
	}

	// 9. Update status conditions on all intent CRDs. Build issues recorded by
	// the builders surface as Ready=False on the specific offending resource.
	issuesMap := make(map[string]status.ResourceIssue)
	for _, issue := range report.Issues() {
		issuesMap[status.IssueKey(issue.Kind, issue.Namespace, issue.Name)] = status.ResourceIssue{
			Reason:  issue.Reason,
			Message: issue.Message,
		}
	}
	if err := r.statusUpdater.UpdateConditions(timeoutCtx, fetched, resolved, issuesMap, r.nodeLocalASNs(timeoutCtx)); err != nil {
		r.logger.Error(err, "status condition update failed")
	}

	r.logger.Info("intent reconciliation complete")
	return nil
}

// nodeLocalASNs returns a map of node name → local (platform-side) BGP AS
// number, as reported by each node's agent on NodeNetworkConfig.status.asNumber
// (the NNC object name is the node name). Nodes that have not reported an ASN
// yet (value 0) are omitted. The map lets the status updater resolve the ASN
// per BGPPeering from only the nodes that peering actually lands on, and fail
// closed when those nodes disagree.
func (r *Reconciler) nodeLocalASNs(ctx context.Context) map[string]int64 {
	nncList := &networkv1alpha1.NodeNetworkConfigList{}
	if err := r.client.List(ctx, nncList); err != nil {
		r.logger.Error(err, "unable to list NodeNetworkConfigs for local ASN")
		return nil
	}
	asns := make(map[string]int64, len(nncList.Items))
	for i := range nncList.Items {
		if asn := nncList.Items[i].Status.ASNumber; asn != 0 {
			asns[nncList.Items[i].Name] = asn
		}
	}
	return asns
}

// applyNodeConfigs assembles each node's contributions and applies the resulting
// NodeNetworkConfig and NodeNetplanConfig. Per-node failures are logged and
// skipped so one bad node does not block the others.
func (r *Reconciler) applyNodeConfigs(
	ctx context.Context,
	fetched *resolver.FetchedResources,
	contributions map[string][]*builder.NodeContribution,
) {
	for i := range fetched.Nodes {
		node := &fetched.Nodes[i]
		result, err := assembler.Assemble(contributions[node.Name])
		if err != nil {
			r.logger.Error(err, "assembly failed", "node", node.Name)
			continue
		}

		// Reduce all VRF names (map keys and cross-references) to their
		// datapath-safe form before hashing and applying the config.
		if err := nncnames.Reduce(result.Spec); err != nil {
			r.logger.Error(err, "VRF name reduction failed", "node", node.Name)
			continue
		}

		// Compute revision hash.
		revision, err := computeRevision(result.Spec)
		if err != nil {
			r.logger.Error(err, "revision hash failed", "node", node.Name)
			continue
		}
		result.Spec.Revision = revision

		// 7. Create or update NNC.
		if err := r.applyNNC(ctx, node, result.Spec, result.Origins); err != nil {
			r.logger.Error(err, "failed to apply NodeNetworkConfig", "node", node.Name)
			continue
		}

		// 8. Create or update NodeNetplanConfig (host-side VLANs for HBN-L2 agent).
		netplanState := buildNetplanState(result.Spec, result.NetplanNodeIPs)
		if err := r.applyNetplanConfig(ctx, node, netplanState); err != nil {
			r.logger.Error(err, "failed to apply NodeNetplanConfig", "node", node.Name)
			continue
		}
	}
}

// filterActive returns only items without a DeletionTimestamp (not being deleted).
func filterActive[T any, PT interface {
	*T
	GetDeletionTimestamp() *metav1.Time
}](items []T) []T {
	active := make([]T, 0, len(items))
	for i := range items {
		if PT(&items[i]).GetDeletionTimestamp() == nil {
			active = append(active, items[i])
		}
	}
	return active
}

// listInto fetches all objects of a list type in a single call and passes them to consume.
// The manager's cached client already holds all watched objects in memory, so pagination
// via Limit/Continue is neither necessary nor supported by the cache.
func listInto[L interface {
	client.ObjectList
	*U
}, U any](ctx context.Context, c client.Client, baseOpts []client.ListOption, consume func(list L)) error {
	list := L(new(U))
	if err := c.List(ctx, list, baseOpts...); err != nil {
		return fmt.Errorf("list: %w", err)
	}
	consume(list)
	return nil
}

func (r *Reconciler) fetchAll(ctx context.Context) (*resolver.FetchedResources, error) {
	f := &resolver.FetchedResources{}

	var nsOpts []client.ListOption
	if r.namespace != "" {
		nsOpts = append(nsOpts, client.InNamespace(r.namespace))
	}

	if err := listInto[*corev1.NodeList](ctx, r.client, nil, func(l *corev1.NodeList) {
		f.Nodes = append(f.Nodes, l.Items...)
	}); err != nil {
		return nil, fmt.Errorf("error listing Nodes: %w", err)
	}

	if err := listInto[*nc.VRFList](ctx, r.client, nsOpts, func(l *nc.VRFList) {
		f.AllVRFs = append(f.AllVRFs, l.Items...)
		f.VRFs = append(f.VRFs, filterActive(l.Items)...)
	}); err != nil {
		return nil, fmt.Errorf("error listing VRFs: %w", err)
	}

	if err := listInto[*nc.NetworkList](ctx, r.client, nsOpts, func(l *nc.NetworkList) {
		f.AllNetworks = append(f.AllNetworks, l.Items...)
		f.Networks = append(f.Networks, filterActive(l.Items)...)
	}); err != nil {
		return nil, fmt.Errorf("error listing Networks: %w", err)
	}

	if err := listInto[*nc.DestinationList](ctx, r.client, nsOpts, func(l *nc.DestinationList) {
		f.AllDestinations = append(f.AllDestinations, l.Items...)
		f.Destinations = append(f.Destinations, filterActive(l.Items)...)
	}); err != nil {
		return nil, fmt.Errorf("error listing Destinations: %w", err)
	}

	if err := listInto[*nc.Layer2AttachmentList](ctx, r.client, nsOpts, func(l *nc.Layer2AttachmentList) {
		f.Layer2Attachments = append(f.Layer2Attachments, filterActive(l.Items)...)
	}); err != nil {
		return nil, fmt.Errorf("error listing Layer2Attachments: %w", err)
	}

	if err := listInto[*nc.InboundList](ctx, r.client, nsOpts, func(l *nc.InboundList) {
		f.Inbounds = append(f.Inbounds, filterActive(l.Items)...)
	}); err != nil {
		return nil, fmt.Errorf("error listing Inbounds: %w", err)
	}

	if err := listInto[*nc.OutboundList](ctx, r.client, nsOpts, func(l *nc.OutboundList) {
		f.Outbounds = append(f.Outbounds, filterActive(l.Items)...)
	}); err != nil {
		return nil, fmt.Errorf("error listing Outbounds: %w", err)
	}

	if err := listInto[*nc.PodNetworkList](ctx, r.client, nsOpts, func(l *nc.PodNetworkList) {
		f.PodNetworks = append(f.PodNetworks, filterActive(l.Items)...)
	}); err != nil {
		return nil, fmt.Errorf("error listing PodNetworks: %w", err)
	}

	if err := listInto[*nc.BGPPeeringList](ctx, r.client, nsOpts, func(l *nc.BGPPeeringList) {
		f.BGPPeerings = append(f.BGPPeerings, filterActive(l.Items)...)
	}); err != nil {
		return nil, fmt.Errorf("error listing BGPPeerings: %w", err)
	}

	if err := listInto[*nc.CollectorList](ctx, r.client, nsOpts, func(l *nc.CollectorList) {
		f.Collectors = append(f.Collectors, filterActive(l.Items)...)
	}); err != nil {
		return nil, fmt.Errorf("error listing Collectors: %w", err)
	}

	if err := listInto[*nc.TrafficMirrorList](ctx, r.client, nsOpts, func(l *nc.TrafficMirrorList) {
		f.TrafficMirrors = append(f.TrafficMirrors, filterActive(l.Items)...)
	}); err != nil {
		return nil, fmt.Errorf("error listing TrafficMirrors: %w", err)
	}

	if err := listInto[*nc.AnnouncementPolicyList](ctx, r.client, nsOpts, func(l *nc.AnnouncementPolicyList) {
		f.AnnouncementPolicies = append(f.AnnouncementPolicies, filterActive(l.Items)...)
	}); err != nil {
		return nil, fmt.Errorf("error listing AnnouncementPolicies: %w", err)
	}

	if err := listInto[*nc.NodeAttachmentList](ctx, r.client, nsOpts, func(l *nc.NodeAttachmentList) {
		f.NodeAttachments = append(f.NodeAttachments, filterActive(l.Items)...)
	}); err != nil {
		return nil, fmt.Errorf("error listing NodeAttachments: %w", err)
	}

	// Resolve BGPPeering AuthSecretRefs to inline passwords. Skipping (with a
	// log) is preferred over failing the whole reconcile: a missing or
	// malformed Secret should degrade only the affected peering.
	f.BGPPasswords = r.resolveBGPPasswords(ctx, f.BGPPeerings)

	return f, nil
}

// resolveBGPPasswords fetches the Secret referenced by each BGPPeering's
// AuthSecretRef (in the same namespace) and returns a map keyed by
// "<namespace>/<name>" of the BGPPeering. Missing/malformed Secrets are
// logged and skipped — the affected BGPPeering will simply have no password.
func (r *Reconciler) resolveBGPPasswords(ctx context.Context, peerings []nc.BGPPeering) map[string]string {
	out := map[string]string{}
	for i := range peerings {
		bp := &peerings[i]
		if bp.Spec.AuthSecretRef == nil || bp.Spec.AuthSecretRef.Name == "" {
			continue
		}
		secret := &corev1.Secret{}
		key := client.ObjectKey{Namespace: bp.Namespace, Name: bp.Spec.AuthSecretRef.Name}
		if err := r.client.Get(ctx, key, secret); err != nil {
			r.logger.Info("BGPPeering authSecretRef not resolvable; peering will have no password",
				"bgppeering", client.ObjectKeyFromObject(bp).String(),
				"secret", key.String(),
				"error", err.Error())
			continue
		}
		raw, ok := secret.Data["password"]
		if !ok || len(raw) == 0 {
			r.logger.Info("BGPPeering authSecretRef Secret has no 'password' key; peering will have no password",
				"bgppeering", client.ObjectKeyFromObject(bp).String(),
				"secret", key.String())
			continue
		}
		out[client.ObjectKeyFromObject(bp).String()] = string(raw)
	}
	return out
}

const originsAnnotation = "network-connector.sylvaproject.org/origins"

// applyNNC creates or updates a NodeNetworkConfig for a node.
// It skips nodes that are currently provisioning (rolling update gate).
func (r *Reconciler) applyNNC(ctx context.Context, node *corev1.Node, spec *networkv1alpha1.NodeNetworkConfigSpec, origins map[string]string) error {
	const maxRetries = 5
	const conflictRetryDelay = 200 * time.Millisecond
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := r.tryApplyNNC(ctx, node, spec, origins)
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return err
		}
		r.logger.Info("NNC update conflict, retrying", "node", node.Name, "attempt", attempt+1)
		select {
		case <-time.After(conflictRetryDelay):
		case <-ctx.Done():
			return fmt.Errorf("context done while retrying NNC update: %w", ctx.Err())
		}
	}
	return fmt.Errorf("NNC update for node %s failed after %d retries due to conflicts", node.Name, maxRetries)
}

func (r *Reconciler) tryApplyNNC(ctx context.Context, node *corev1.Node, spec *networkv1alpha1.NodeNetworkConfigSpec, origins map[string]string) error {
	existing := &networkv1alpha1.NodeNetworkConfig{}
	err := r.client.Get(ctx, client.ObjectKey{Name: node.Name}, existing)

	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("error getting NodeNetworkConfig for node %s: %w", node.Name, err)
	}

	if err == nil {
		// NNC exists — check if update needed.
		if existing.Spec.Revision == spec.Revision && hasIntentManagedLabel(existing) {
			return nil // no change
		}

		// Skip nodes currently provisioning (rolling update gate).
		if existing.Status.ConfigStatus == "provisioning" {
			r.logger.Info("skipping node — currently provisioning", "node", node.Name)
			return nil
		}

		// Strip legacy owner refs (NetworkConfigRevision) and ensure only Node owner.
		r.stripLegacyOwnerRefs(existing, node)
		setIntentManagedLabel(existing)
		existing.Spec = *spec
		setOriginsAnnotation(existing, origins)
		if err := r.client.Update(ctx, existing); err != nil {
			return fmt.Errorf("error updating NodeNetworkConfig for node %s: %w", node.Name, err)
		}
		return nil
	}

	// NNC does not exist — create it.
	nnc := &networkv1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: node.Name,
		},
		Spec: *spec,
	}
	setIntentManagedLabel(nnc)
	setOriginsAnnotation(nnc, origins)

	if err := controllerutil.SetOwnerReference(node, nnc, r.client.Scheme()); err != nil {
		return fmt.Errorf("error setting owner reference for NNC %s: %w", node.Name, err)
	}

	if err := r.client.Create(ctx, nnc); err != nil {
		return fmt.Errorf("error creating NodeNetworkConfig for node %s: %w", node.Name, err)
	}
	return nil
}

// setOriginsAnnotation writes the origins map as a JSON annotation on the NNC.
func setOriginsAnnotation(nnc *networkv1alpha1.NodeNetworkConfig, origins map[string]string) {
	if len(origins) == 0 {
		return
	}
	data, err := json.Marshal(origins)
	if err != nil {
		return
	}
	if nnc.Annotations == nil {
		nnc.Annotations = make(map[string]string)
	}
	nnc.Annotations[originsAnnotation] = string(data)
}

// computeRevision computes a SHA256 hash of the NNC spec for change detection.
func computeRevision(spec *networkv1alpha1.NodeNetworkConfigSpec) (string, error) {
	// Zero out revision before hashing to avoid self-reference.
	specCopy := *spec
	specCopy.Revision = ""

	data, err := json.Marshal(specCopy)
	if err != nil {
		return "", fmt.Errorf("error marshaling NNC spec: %w", err)
	}

	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash), nil
}

const intentManagedLabel = "network-connector.sylvaproject.org/managed-by"
const intentLabelValue = "intent"

// setIntentManagedLabel marks an NNC as managed by the intent reconciler.
func setIntentManagedLabel(nnc *networkv1alpha1.NodeNetworkConfig) {
	if nnc.Labels == nil {
		nnc.Labels = make(map[string]string)
	}
	nnc.Labels[intentManagedLabel] = intentLabelValue
}

// hasIntentManagedLabel returns true if the NNC is already marked as intent-managed.
func hasIntentManagedLabel(nnc *networkv1alpha1.NodeNetworkConfig) bool {
	return nnc.Labels != nil && nnc.Labels[intentManagedLabel] == intentLabelValue
}

// stripLegacyOwnerRefs removes owner references that are not the Node.
// Legacy NNCs have 2 owner refs (Node + NetworkConfigRevision); intent uses Node only.
func (r *Reconciler) stripLegacyOwnerRefs(nnc *networkv1alpha1.NodeNetworkConfig, node *corev1.Node) {
	filtered := make([]metav1.OwnerReference, 0, 1)
	for _, ref := range nnc.OwnerReferences {
		if ref.UID == node.UID {
			filtered = append(filtered, ref)
		} else {
			r.logger.Info("stripping legacy owner reference", "nnc", nnc.Name,
				"ownerKind", ref.Kind, "ownerName", ref.Name)
		}
	}
	nnc.OwnerReferences = filtered

	// Ensure Node owner ref exists.
	if err := controllerutil.SetOwnerReference(node, nnc, r.client.Scheme()); err != nil {
		r.logger.Error(err, "failed to set Node owner reference", "nnc", nnc.Name)
	}
}

// cleanupOrphanedNNCs deletes NNCs that don't correspond to any current node.
func (r *Reconciler) cleanupOrphanedNNCs(ctx context.Context, nodes []corev1.Node) error {
	nodeNames := make(map[string]struct{}, len(nodes))
	for i := range nodes {
		nodeNames[nodes[i].Name] = struct{}{}
	}

	nncList := &networkv1alpha1.NodeNetworkConfigList{}
	if err := r.client.List(ctx, nncList); err != nil {
		return fmt.Errorf("error listing NodeNetworkConfigs: %w", err)
	}

	for i := range nncList.Items {
		nnc := &nncList.Items[i]
		if _, exists := nodeNames[nnc.Name]; exists {
			continue
		}
		// Only delete NNCs that were created by the intent reconciler.
		// NNCs without the intent-managed label belong to the legacy reconciler
		// and must not be touched during migration.
		if !hasIntentManagedLabel(nnc) {
			continue
		}
		r.logger.Info("deleting orphaned NodeNetworkConfig", "name", nnc.Name)
		if err := r.client.Delete(ctx, nnc); err != nil && !apierrors.IsNotFound(err) {
			r.logger.Error(err, "failed to delete orphaned NNC", "name", nnc.Name)
		}
	}
	return nil
}

// buildNetplanState derives a netplan State from the assembled NNC spec.
// It creates VLAN devices from the NNC Layer2 entries and pure-L2 netplan-only
// entries. The parent link of a VLAN (the hbn trunk in HBN mode, or the
// interfaceRef NIC/bond in non-HBN mode) must be declared out-of-band — via the
// static 10-hbn.yaml for hbn, or via an InterfaceConfig / host netplan for a
// non-HBN parent — since its type (ethernet vs bond vs bridge) is not knowable
// here. When nodeIPs are allocated, the VLAN device also gets per-node addresses
// and routes pointing to the IRB anycast gateway. When no VLAN tag is set
// (native/untagged mode), addresses and routes are placed directly on the parent
// ethernet interface.
func buildNetplanState(spec *networkv1alpha1.NodeNetworkConfigSpec, nodeIPs map[string]builder.NetplanNodeIP) *netplan.State {
	state := netplan.NewEmptyState()

	if (spec == nil || len(spec.Layer2s) == 0) && len(nodeIPs) == 0 {
		return &state
	}

	for _, k := range sortedNetplanKeys(spec, nodeIPs) {
		l2 := layer2Entry(spec, k)
		nip, hasNip := nodeIPs[k]
		vlanID, mtu, link := netplanDeviceParams(&l2, &nip, hasNip)

		if hasNip && vlanID == 0 && nip.InterfaceRef != "" {
			// Native/untagged mode writes the per-node addresses/routes directly
			// onto the parent interface under ethernets:. Gated on an explicit
			// InterfaceRef (not link != hbnTrunk) so it also triggers if a user
			// names the parent "hbn". This is scoped to physical ethernet NICs
			// (the driving VMware/bare-metal use case). A bond/bridge parent must
			// instead be addressed via a tagged VLAN (vlans: link: <bond>), where
			// the bond/bridge stays declared out-of-band under bonds:/bridges:
			// and is never rewritten here — emitting it under ethernets: would be
			// an invalid device-type redefinition. See the Layer2Attachment guide.
			rawDev, err := json.Marshal(buildNativeEthernetDevice(&nip, mtu))
			if err != nil {
				continue
			}
			state.Network.Ethernets[link] = netplan.Device{Raw: rawDev}
			continue
		}

		if vlanID == 0 {
			continue
		}

		vlan := buildVLANDevice(vlanID, mtu, link, &nip)

		rawVlan, err := json.Marshal(vlan)
		if err != nil {
			continue
		}

		ifName := fmt.Sprintf("vlan.%d", vlanID)
		if hasNip && nip.InterfaceName != "" {
			ifName = nip.InterfaceName
		}

		state.Network.VLans[ifName] = netplan.Device{
			Raw: rawVlan,
		}
	}

	return &state
}

func sortedNetplanKeys(spec *networkv1alpha1.NodeNetworkConfigSpec, nodeIPs map[string]builder.NetplanNodeIP) []string {
	keys := make([]string, 0, len(nodeIPs))
	for k := range nodeIPs {
		keys = append(keys, k)
	}
	if spec != nil {
		for k := range spec.Layer2s {
			if _, exists := nodeIPs[k]; !exists {
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

func layer2Entry(spec *networkv1alpha1.NodeNetworkConfigSpec, key string) networkv1alpha1.Layer2 {
	if spec == nil {
		return networkv1alpha1.Layer2{}
	}
	return spec.Layer2s[key]
}

func netplanDeviceParams(l2 *networkv1alpha1.Layer2, nip *builder.NetplanNodeIP, hasNip bool) (vlanID, mtu uint16, link string) {
	vlanID, mtu = l2.VLAN, l2.MTU
	// Fall back to the netplan-only metadata when the NNC Layer2 entry carries
	// no VLAN — either there is no Layer2 entry, or one exists with zero scalars
	// (e.g. a mirror-only entry contributing just MirrorACLs).
	if vlanID == 0 && hasNip {
		vlanID, mtu = nip.VLAN, nip.MTU
	}
	link = hbnTrunk
	if hasNip && nip.InterfaceRef != "" {
		link = nip.InterfaceRef
	}
	return
}

const hbnTrunk = "hbn"

func buildNetplanRoutes(nip *builder.NetplanNodeIP) []interface{} {
	routes := make([]interface{}, 0, len(nip.Gateways)+len(nip.Routes))
	for _, gw := range nip.Gateways {
		routes = append(routes, map[string]interface{}{"to": "default", "via": gw})
	}
	for _, route := range nip.Routes {
		routes = append(routes, map[string]interface{}{"to": route.To, "via": route.Via})
	}
	return routes
}

// buildNativeEthernetDevice renders a native/untagged device: per-node
// addresses and routes placed directly on the parent NIC. It emits an
// ethernets: device and is therefore scoped to physical ethernet parents; a
// bond/bridge parent must be addressed via a tagged VLAN instead.
func buildNativeEthernetDevice(nip *builder.NetplanNodeIP, mtu uint16) map[string]interface{} {
	dev := map[string]interface{}{
		"link-local": []interface{}{},
		"critical":   true,
	}
	if mtu != 0 {
		dev["mtu"] = mtu
	}
	if len(nip.Addresses) > 0 {
		addrs := make([]interface{}, 0, len(nip.Addresses))
		for _, a := range nip.Addresses {
			addrs = append(addrs, a)
		}
		dev["addresses"] = addrs
	}
	if len(nip.Gateways) > 0 || len(nip.Routes) > 0 {
		routes := buildNetplanRoutes(nip)
		if len(routes) > 0 {
			dev["routes"] = routes
		}
	}
	return dev
}

func buildVLANDevice(vlanID, mtu uint16, link string, nip *builder.NetplanNodeIP) map[string]interface{} {
	vlan := map[string]interface{}{
		"id":         vlanID,
		"link":       link,
		"mtu":        mtu,
		"critical":   true,
		"link-local": []interface{}{},
	}
	if len(nip.Addresses) > 0 || len(nip.Gateways) > 0 || len(nip.Routes) > 0 {
		if len(nip.Addresses) > 0 {
			addrs := make([]interface{}, 0, len(nip.Addresses))
			for _, a := range nip.Addresses {
				addrs = append(addrs, a)
			}
			vlan["addresses"] = addrs
		}
		if len(nip.Gateways) > 0 || len(nip.Routes) > 0 {
			routes := buildNetplanRoutes(nip)
			if len(routes) > 0 {
				vlan["routes"] = routes
			}
		}
	}
	return vlan
}

// applyNetplanConfig creates or updates a NodeNetplanConfig for a node.
func (r *Reconciler) applyNetplanConfig(ctx context.Context, node *corev1.Node, state *netplan.State) error {
	existing := &networkv1alpha1.NodeNetplanConfig{}
	err := r.client.Get(ctx, client.ObjectKey{Name: node.Name}, existing)

	desiredSpec := networkv1alpha1.NodeNetplanConfigSpec{
		DesiredState: *state,
	}

	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("error getting NodeNetplanConfig for node %s: %w", node.Name, err)
	}

	if err == nil {
		// Exists — check if update needed.
		if existing.Spec.DesiredState.Equals(state) && hasIntentManagedNetplanLabel(existing) {
			return nil // no change
		}

		setIntentManagedNetplanLabel(existing)
		existing.Spec = desiredSpec
		if err := r.client.Update(ctx, existing); err != nil {
			return fmt.Errorf("error updating NodeNetplanConfig for node %s: %w", node.Name, err)
		}
		return nil
	}

	// Does not exist — create.
	npc := &networkv1alpha1.NodeNetplanConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: node.Name,
		},
		Spec: desiredSpec,
	}
	setIntentManagedNetplanLabel(npc)

	if err := controllerutil.SetOwnerReference(node, npc, r.client.Scheme()); err != nil {
		return fmt.Errorf("error setting owner reference for NodeNetplanConfig %s: %w", node.Name, err)
	}

	if err := r.client.Create(ctx, npc); err != nil {
		return fmt.Errorf("error creating NodeNetplanConfig for node %s: %w", node.Name, err)
	}
	return nil
}

// setIntentManagedNetplanLabel marks a NodeNetplanConfig as managed by the intent reconciler.
func setIntentManagedNetplanLabel(npc *networkv1alpha1.NodeNetplanConfig) {
	if npc.Labels == nil {
		npc.Labels = make(map[string]string)
	}
	npc.Labels[intentManagedLabel] = intentLabelValue
}

// hasIntentManagedNetplanLabel returns true if the NodeNetplanConfig is already managed by intent.
func hasIntentManagedNetplanLabel(npc *networkv1alpha1.NodeNetplanConfig) bool {
	return npc.Labels != nil && npc.Labels[intentManagedLabel] == intentLabelValue
}

// cleanupOrphanedNetplanConfigs deletes NodeNetplanConfigs that don't correspond to any current node.
func (r *Reconciler) cleanupOrphanedNetplanConfigs(ctx context.Context, nodes []corev1.Node) error {
	nodeNames := make(map[string]struct{}, len(nodes))
	for i := range nodes {
		nodeNames[nodes[i].Name] = struct{}{}
	}

	npcList := &networkv1alpha1.NodeNetplanConfigList{}
	if err := r.client.List(ctx, npcList); err != nil {
		return fmt.Errorf("error listing NodeNetplanConfigs: %w", err)
	}

	for i := range npcList.Items {
		npc := &npcList.Items[i]
		if _, exists := nodeNames[npc.Name]; exists {
			continue
		}
		if !hasIntentManagedNetplanLabel(npc) {
			continue // not ours
		}
		r.logger.Info("deleting orphaned NodeNetplanConfig", "name", npc.Name)
		if err := r.client.Delete(ctx, npc); err != nil && !apierrors.IsNotFound(err) {
			r.logger.Error(err, "failed to delete orphaned NodeNetplanConfig", "name", npc.Name)
		}
	}
	return nil
}

// reconcileCollectorAddresses allocates per-node loopback addresses for every
// Collector from its mirrorVRF.loopback.subnet and persists the resulting map
// (and AddressesAllocated condition) in Collector.status.
//
// Allocations are stable: an entry is preserved across reconciles for as long
// as the corresponding node is still in the cluster, so loopback IPs do not
// reshuffle on unrelated reconciles. The function mutates fetched.Collectors
// in place so downstream builders see the up-to-date status.
func (r *Reconciler) reconcileCollectorAddresses(ctx context.Context, fetched *resolver.FetchedResources) {
	if len(fetched.Collectors) == 0 {
		return
	}
	nodeNames := make([]string, 0, len(fetched.Nodes))
	for i := range fetched.Nodes {
		nodeNames = append(nodeNames, fetched.Nodes[i].Name)
	}

	for i := range fetched.Collectors {
		col := &fetched.Collectors[i]
		subnet := col.Spec.MirrorVRF.Loopback.Subnet
		if subnet == "" {
			continue
		}
		res, err := ipam.AllocateSubnet(subnet, nodeNames, col.Status.NodeAddresses)
		if err != nil {
			r.logger.Error(err, "subnet allocation failed", "collector", col.Name, "subnet", subnet)
			cond := newCollectorCondition(col.Generation, metav1.ConditionFalse, "InvalidSubnet", err.Error())
			r.applyCollectorCondition(ctx, col, &cond)
			continue
		}
		// Skip Status update when nothing changed, to avoid resourceVersion churn.
		if mapsEqual(col.Status.NodeAddresses, res.Updated) {
			continue
		}
		col.Status.NodeAddresses = res.Updated
		reason := "AllAllocated"
		msg := fmt.Sprintf("allocated %d/%d node addresses from %s", len(res.Updated), len(nodeNames), subnet)
		condStatus := metav1.ConditionTrue
		if len(res.Unallocated) > 0 {
			condStatus = metav1.ConditionFalse
			reason = "SubnetExhausted"
			msg = fmt.Sprintf("subnet %s exhausted: %d node(s) unallocated: %v", subnet, len(res.Unallocated), res.Unallocated)
		}
		cond := newCollectorCondition(col.Generation, condStatus, reason, msg)
		r.applyCollectorCondition(ctx, col, &cond)
	}
}

// applyCollectorCondition upserts the given condition on the Collector and
// persists the status. Errors are logged but not returned so a single failure
// does not abort the wider reconcile.
func (r *Reconciler) applyCollectorCondition(ctx context.Context, col *nc.Collector, cond *metav1.Condition) {
	upsertCondition(&col.Status.Conditions, cond)
	if err := r.client.Status().Update(ctx, col); err != nil {
		r.logger.Error(err, "failed to update Collector status", "collector", col.Name)
	}
}

func newCollectorCondition(generation int64, condStatus metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{
		Type:               nc.CollectorConditionAddressesAllocated,
		Status:             condStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
	}
}

func upsertCondition(conds *[]metav1.Condition, cond *metav1.Condition) {
	for i := range *conds {
		if (*conds)[i].Type == cond.Type {
			if (*conds)[i].Status == cond.Status {
				cond.LastTransitionTime = (*conds)[i].LastTransitionTime
			}
			(*conds)[i] = *cond
			return
		}
	}
	*conds = append(*conds, *cond)
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}
