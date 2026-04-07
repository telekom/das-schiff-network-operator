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
)

const (
	defaultDebounceTime = 1 * time.Second
	// DefaultTimeout is the default API timeout.
	DefaultTimeout = "60s"
	// listLimit caps the number of objects returned per List call to prevent
	// unbounded memory consumption on large clusters.
	listLimit = 500
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

	// 5. Run all builders → per-node contributions.
	contributions := make(map[string][]*builder.NodeContribution) // nodeName → contributions
	for _, b := range r.builders {
		nodeContribs, err := b.Build(ctx, resolved)
		if err != nil {
			r.logger.Error(err, "builder failed", "builder", b.Name())
			return fmt.Errorf("builder %s failed: %w", b.Name(), err)
		}
		for nodeName, contrib := range nodeContribs {
			contributions[nodeName] = append(contributions[nodeName], contrib)
		}
	}

	// 6. Assemble NNC spec per node.
	for i := range fetched.Nodes {
		node := &fetched.Nodes[i]
		result, err := assembler.Assemble(contributions[node.Name])
		if err != nil {
			r.logger.Error(err, "assembly failed", "node", node.Name)
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
		if err := r.applyNNC(timeoutCtx, node, result.Spec, result.Origins); err != nil {
			r.logger.Error(err, "failed to apply NodeNetworkConfig", "node", node.Name)
			continue
		}

		// 8. Create or update NodeNetplanConfig (host-side VLANs for HBN-L2 agent).
		netplanState := buildNetplanState(result.Spec)
		if err := r.applyNetplanConfig(timeoutCtx, node, netplanState); err != nil {
			r.logger.Error(err, "failed to apply NodeNetplanConfig", "node", node.Name)
			continue
		}
	}

	// 9. Update status conditions on all intent CRDs.
	if err := r.statusUpdater.UpdateConditions(timeoutCtx, fetched, resolved); err != nil {
		r.logger.Error(err, "status condition update failed")
	}

	r.logger.Info("intent reconciliation complete")
	return nil
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

func (r *Reconciler) fetchAll(ctx context.Context) (*resolver.FetchedResources, error) {
	f := &resolver.FetchedResources{}

	// Build base list options — restrict to namespace if configured.
	var nsOpts []client.ListOption
	if r.namespace != "" {
		nsOpts = append(nsOpts, client.InNamespace(r.namespace))
	}

	// Fetch nodes (always cluster-wide, paginated).
	for continueToken := ""; ; {
		nodeList := &corev1.NodeList{}
		opts := []client.ListOption{client.Limit(listLimit)}
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := r.client.List(ctx, nodeList, opts...); err != nil {
			return nil, fmt.Errorf("error listing Nodes: %w", err)
		}
		f.Nodes = append(f.Nodes, nodeList.Items...)
		continueToken = nodeList.GetContinue()
		if continueToken == "" {
			break
		}
	}

	// Fetch VRFs (paginated).
	for continueToken := ""; ; {
		vrfList := &nc.VRFList{}
		opts := append(append([]client.ListOption{}, nsOpts...), client.Limit(listLimit))
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := r.client.List(ctx, vrfList, opts...); err != nil {
			return nil, fmt.Errorf("error listing VRFs: %w", err)
		}
		f.AllVRFs = append(f.AllVRFs, vrfList.Items...)
		f.VRFs = append(f.VRFs, filterActive(vrfList.Items)...)
		continueToken = vrfList.GetContinue()
		if continueToken == "" {
			break
		}
	}

	// Fetch Networks (paginated).
	for continueToken := ""; ; {
		networkList := &nc.NetworkList{}
		opts := append(append([]client.ListOption{}, nsOpts...), client.Limit(listLimit))
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := r.client.List(ctx, networkList, opts...); err != nil {
			return nil, fmt.Errorf("error listing Networks: %w", err)
		}
		f.AllNetworks = append(f.AllNetworks, networkList.Items...)
		f.Networks = append(f.Networks, filterActive(networkList.Items)...)
		continueToken = networkList.GetContinue()
		if continueToken == "" {
			break
		}
	}

	// Fetch Destinations (paginated).
	for continueToken := ""; ; {
		destList := &nc.DestinationList{}
		opts := append(append([]client.ListOption{}, nsOpts...), client.Limit(listLimit))
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := r.client.List(ctx, destList, opts...); err != nil {
			return nil, fmt.Errorf("error listing Destinations: %w", err)
		}
		f.AllDestinations = append(f.AllDestinations, destList.Items...)
		f.Destinations = append(f.Destinations, filterActive(destList.Items)...)
		continueToken = destList.GetContinue()
		if continueToken == "" {
			break
		}
	}

	// Fetch Layer2Attachments (paginated).
	for continueToken := ""; ; {
		l2aList := &nc.Layer2AttachmentList{}
		opts := append(append([]client.ListOption{}, nsOpts...), client.Limit(listLimit))
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := r.client.List(ctx, l2aList, opts...); err != nil {
			return nil, fmt.Errorf("error listing Layer2Attachments: %w", err)
		}
		f.Layer2Attachments = append(f.Layer2Attachments, filterActive(l2aList.Items)...)
		continueToken = l2aList.GetContinue()
		if continueToken == "" {
			break
		}
	}

	// Fetch Inbounds (paginated).
	for continueToken := ""; ; {
		inboundList := &nc.InboundList{}
		opts := append(append([]client.ListOption{}, nsOpts...), client.Limit(listLimit))
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := r.client.List(ctx, inboundList, opts...); err != nil {
			return nil, fmt.Errorf("error listing Inbounds: %w", err)
		}
		f.Inbounds = append(f.Inbounds, filterActive(inboundList.Items)...)
		continueToken = inboundList.GetContinue()
		if continueToken == "" {
			break
		}
	}

	// Fetch Outbounds (paginated).
	for continueToken := ""; ; {
		outboundList := &nc.OutboundList{}
		opts := append(append([]client.ListOption{}, nsOpts...), client.Limit(listLimit))
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := r.client.List(ctx, outboundList, opts...); err != nil {
			return nil, fmt.Errorf("error listing Outbounds: %w", err)
		}
		f.Outbounds = append(f.Outbounds, filterActive(outboundList.Items)...)
		continueToken = outboundList.GetContinue()
		if continueToken == "" {
			break
		}
	}

	// Fetch PodNetworks (paginated).
	for continueToken := ""; ; {
		podNetworkList := &nc.PodNetworkList{}
		opts := append(append([]client.ListOption{}, nsOpts...), client.Limit(listLimit))
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := r.client.List(ctx, podNetworkList, opts...); err != nil {
			return nil, fmt.Errorf("error listing PodNetworks: %w", err)
		}
		f.PodNetworks = append(f.PodNetworks, filterActive(podNetworkList.Items)...)
		continueToken = podNetworkList.GetContinue()
		if continueToken == "" {
			break
		}
	}

	// Fetch BGPPeerings (paginated).
	for continueToken := ""; ; {
		bgpList := &nc.BGPPeeringList{}
		opts := append(append([]client.ListOption{}, nsOpts...), client.Limit(listLimit))
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := r.client.List(ctx, bgpList, opts...); err != nil {
			return nil, fmt.Errorf("error listing BGPPeerings: %w", err)
		}
		f.BGPPeerings = append(f.BGPPeerings, filterActive(bgpList.Items)...)
		continueToken = bgpList.GetContinue()
		if continueToken == "" {
			break
		}
	}

	// Fetch Collectors (paginated).
	for continueToken := ""; ; {
		collectorList := &nc.CollectorList{}
		opts := append(append([]client.ListOption{}, nsOpts...), client.Limit(listLimit))
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := r.client.List(ctx, collectorList, opts...); err != nil {
			return nil, fmt.Errorf("error listing Collectors: %w", err)
		}
		f.Collectors = append(f.Collectors, filterActive(collectorList.Items)...)
		continueToken = collectorList.GetContinue()
		if continueToken == "" {
			break
		}
	}

	// Fetch TrafficMirrors (paginated).
	for continueToken := ""; ; {
		mirrorList := &nc.TrafficMirrorList{}
		opts := append(append([]client.ListOption{}, nsOpts...), client.Limit(listLimit))
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := r.client.List(ctx, mirrorList, opts...); err != nil {
			return nil, fmt.Errorf("error listing TrafficMirrors: %w", err)
		}
		f.TrafficMirrors = append(f.TrafficMirrors, filterActive(mirrorList.Items)...)
		continueToken = mirrorList.GetContinue()
		if continueToken == "" {
			break
		}
	}

	// Fetch AnnouncementPolicies (paginated).
	for continueToken := ""; ; {
		policyList := &nc.AnnouncementPolicyList{}
		opts := append(append([]client.ListOption{}, nsOpts...), client.Limit(listLimit))
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := r.client.List(ctx, policyList, opts...); err != nil {
			return nil, fmt.Errorf("error listing AnnouncementPolicies: %w", err)
		}
		f.AnnouncementPolicies = append(f.AnnouncementPolicies, filterActive(policyList.Items)...)
		continueToken = policyList.GetContinue()
		if continueToken == "" {
			break
		}
	}

	return f, nil
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
	if err := r.client.List(ctx, nncList, client.Limit(listLimit)); err != nil {
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
// It creates VLAN devices from the NNC Layer2 entries. The hbn parent
// ethernet is defined in a static netplan config on the node (10-hbn.yaml),
// so netplan can wire VLANs to it when this state is applied.
func buildNetplanState(spec *networkv1alpha1.NodeNetworkConfigSpec) *netplan.State {
	state := netplan.NewEmptyState()

	if spec == nil || len(spec.Layer2s) == 0 {
		return &state
	}

	// Sort VLAN keys for deterministic output.
	keys := make([]string, 0, len(spec.Layer2s))
	for k := range spec.Layer2s {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		l2 := spec.Layer2s[k]
		if l2.VLAN == 0 {
			continue
		}

		vlan := map[string]interface{}{
			"id":         l2.VLAN,
			"link":       "hbn",
			"mtu":        l2.MTU,
			"critical":   true,
			"link-local": []interface{}{},
		}

		rawVlan, err := json.Marshal(vlan)
		if err != nil {
			continue
		}

		state.Network.VLans[fmt.Sprintf("vlan.%d", l2.VLAN)] = netplan.Device{
			Raw: rawVlan,
		}
	}

	return &state
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
	if err := r.client.List(ctx, npcList, client.Limit(listLimit)); err != nil {
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
