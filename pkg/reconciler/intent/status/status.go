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

package status

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

const (
	reasonAllResolved = "AllResolved"
	msgAllResolved    = "All references resolved"
)

// ResourceIssue marks an intent resource that a builder skipped during the
// build phase (e.g. an ambiguous AnnouncementPolicy or a route-target
// collision). It drives a Ready=False condition on the offending resource so
// the failure is visible in the resource status, not only the controller log.
type ResourceIssue struct {
	Reason  string
	Message string
}

// IssueKey builds the map key used to look up a ResourceIssue by kind and name.
func IssueKey(kind, name string) string {
	return kind + "/" + name
}

// applyBuildIssue forces Ready=False (with the issue's reason/message) when the
// resource has a recorded build issue; otherwise the inputs are returned
// unchanged. The Resolved condition is left untouched — references may have
// resolved fine while the build still failed for another reason.
func applyBuildIssue(issues map[string]ResourceIssue, kind, name string,
	status metav1.ConditionStatus, reason, message string,
) (readyStatus metav1.ConditionStatus, readyReason, readyMessage string) {
	if issue, ok := issues[IssueKey(kind, name)]; ok {
		return metav1.ConditionFalse, issue.Reason, issue.Message
	}
	return status, reason, message
}

// Updater handles status condition updates for intent CRDs.
type Updater struct {
	client client.Client
	logger logr.Logger
}

// NewUpdater creates a new status Updater.
func NewUpdater(c client.Client, logger logr.Logger) *Updater {
	return &Updater{
		client: c,
		logger: logger.WithName("status-updater"),
	}
}

// statusUpdateWithRetry performs a status update with conflict retry.
// On conflict, it re-fetches the object and reapplies the update function.
func (u *Updater) statusUpdateWithRetry(ctx context.Context, obj client.Object, applyStatus func(obj client.Object)) error {
	const maxRetries = 3
	const statusRetryDelay = 100 * time.Millisecond
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Re-fetch to get current resourceVersion.
			freshObj := obj.DeepCopyObject()
			fresh, ok := freshObj.(client.Object)
			if !ok {
				return fmt.Errorf("deep copy of %s did not implement client.Object", obj.GetObjectKind().GroupVersionKind().Kind)
			}
			if err := u.client.Get(ctx, client.ObjectKeyFromObject(obj), fresh); err != nil {
				return fmt.Errorf("re-fetching %s/%s for status update: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
			}
			obj = fresh
		}
		applyStatus(obj)
		err := u.client.Status().Update(ctx, obj)
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return fmt.Errorf("updating status for %s/%s: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
		}
		time.Sleep(statusRetryDelay)
	}
	return fmt.Errorf("status update conflict after %d retries for %s/%s", maxRetries, obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
}

// UpdateConditions sets Ready/Resolved conditions on intent CRDs. The issues
// map (keyed by IssueKey(kind, name)) carries per-resource build failures so
// resources skipped during the build phase surface Ready=False.
func (u *Updater) UpdateConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData, issues map[string]ResourceIssue) error {
	if err := u.updateVRFConditions(ctx, fetched); err != nil {
		return fmt.Errorf("VRF conditions: %w", err)
	}
	if err := u.updateNetworkConditions(ctx, fetched); err != nil {
		return fmt.Errorf("network conditions: %w", err)
	}
	if err := u.updateDestinationConditions(ctx, fetched, resolved); err != nil {
		return fmt.Errorf("destination conditions: %w", err)
	}
	if err := u.updateInboundConditions(ctx, fetched, resolved, issues); err != nil {
		return fmt.Errorf("inbound conditions: %w", err)
	}
	if err := u.updateOutboundConditions(ctx, fetched, resolved, issues); err != nil {
		return fmt.Errorf("outbound conditions: %w", err)
	}
	if err := u.updateLayer2AttachmentConditions(ctx, fetched, resolved, issues); err != nil {
		return fmt.Errorf("layer2Attachment conditions: %w", err)
	}
	if err := u.updatePodNetworkConditions(ctx, fetched, resolved, issues); err != nil {
		return fmt.Errorf("podNetwork conditions: %w", err)
	}
	if err := u.updateCollectorConditions(ctx, fetched, issues); err != nil {
		return fmt.Errorf("collector conditions: %w", err)
	}
	if err := u.updateTrafficMirrorConditions(ctx, fetched, resolved, issues); err != nil {
		return fmt.Errorf("trafficMirror conditions: %w", err)
	}
	if err := u.updateNodeAttachmentConditions(ctx, fetched, resolved, issues); err != nil {
		return fmt.Errorf("nodeAttachment conditions: %w", err)
	}
	if err := u.updateBGPPeeringConditions(ctx, fetched, resolved, issues); err != nil {
		return fmt.Errorf("bgpPeering conditions: %w", err)
	}
	return nil
}

func (u *Updater) updateVRFConditions(ctx context.Context, fetched *resolver.FetchedResources) error {
	for i := range fetched.VRFs {
		vrf := &fetched.VRFs[i]
		if err := u.statusUpdateWithRetry(ctx, vrf, func(obj client.Object) {
			v := obj.(*nc.VRF)
			setCondition(&v.Status.Conditions, nc.ConditionTypeResolved, metav1.ConditionTrue, reasonAllResolved, "VRF has no external references to resolve", v.Generation)
			setCondition(&v.Status.Conditions, nc.ConditionTypeReady, metav1.ConditionTrue, "Ready", "VRF is ready", v.Generation)
			v.Status.ObservedGeneration = v.Generation
		}); err != nil {
			return fmt.Errorf("updating VRF %q status: %w", vrf.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateNetworkConditions(ctx context.Context, fetched *resolver.FetchedResources) error {
	for i := range fetched.Networks {
		net := &fetched.Networks[i]
		if err := u.statusUpdateWithRetry(ctx, net, func(obj client.Object) {
			n := obj.(*nc.Network)
			setCondition(&n.Status.Conditions, nc.ConditionTypeResolved, metav1.ConditionTrue, reasonAllResolved, "Network has no external references to resolve", n.Generation)
			setCondition(&n.Status.Conditions, nc.ConditionTypeReady, metav1.ConditionTrue, "Ready", "Network is ready", n.Generation)
			n.Status.ObservedGeneration = n.Generation
		}); err != nil {
			return fmt.Errorf("updating Network %q status: %w", net.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateDestinationConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData) error {
	for i := range fetched.Destinations {
		dest := &fetched.Destinations[i]
		resolvedStatus := metav1.ConditionTrue
		resolvedReason := reasonAllResolved
		resolvedMsg := msgAllResolved

		if dest.Spec.VRFRef != nil {
			if _, ok := resolved.VRFs[*dest.Spec.VRFRef]; !ok {
				resolvedStatus = metav1.ConditionFalse
				resolvedReason = "VRFNotFound"
				resolvedMsg = fmt.Sprintf("referenced VRF %q not found", *dest.Spec.VRFRef)
			}
		}

		readyStatus := resolvedStatus
		readyMsg := "Destination is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}

		if err := u.statusUpdateWithRetry(ctx, dest, func(obj client.Object) {
			d := obj.(*nc.Destination)
			setCondition(&d.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, d.Generation)
			setCondition(&d.Status.Conditions, nc.ConditionTypeReady, readyStatus, resolvedReason, readyMsg, d.Generation)
			d.Status.ObservedGeneration = d.Generation
		}); err != nil {
			return fmt.Errorf("updating Destination %q status: %w", dest.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateInboundConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData, issues map[string]ResourceIssue) error {
	for i := range fetched.Inbounds {
		inb := &fetched.Inbounds[i]
		resolvedStatus, resolvedReason, resolvedMsg := checkNetworkRef(inb.Spec.NetworkRef, resolved)

		readyStatus := resolvedStatus
		readyReason := resolvedReason
		readyMsg := "Inbound is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		readyStatus, readyReason, readyMsg = applyBuildIssue(issues, "Inbound", inb.Name, readyStatus, readyReason, readyMsg)

		if err := u.statusUpdateWithRetry(ctx, inb, func(obj client.Object) {
			in := obj.(*nc.Inbound)
			setCondition(&in.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, in.Generation)
			setCondition(&in.Status.Conditions, nc.ConditionTypeReady, readyStatus, readyReason, readyMsg, in.Generation)
			in.Status.ObservedGeneration = in.Generation
		}); err != nil {
			return fmt.Errorf("updating Inbound %q status: %w", inb.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateOutboundConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData, issues map[string]ResourceIssue) error {
	for i := range fetched.Outbounds {
		outb := &fetched.Outbounds[i]
		resolvedStatus, resolvedReason, resolvedMsg := checkNetworkRef(outb.Spec.NetworkRef, resolved)

		readyStatus := resolvedStatus
		readyReason := resolvedReason
		readyMsg := "Outbound is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		readyStatus, readyReason, readyMsg = applyBuildIssue(issues, "Outbound", outb.Name, readyStatus, readyReason, readyMsg)

		if err := u.statusUpdateWithRetry(ctx, outb, func(obj client.Object) {
			o := obj.(*nc.Outbound)
			setCondition(&o.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, o.Generation)
			setCondition(&o.Status.Conditions, nc.ConditionTypeReady, readyStatus, readyReason, readyMsg, o.Generation)
			o.Status.ObservedGeneration = o.Generation
		}); err != nil {
			return fmt.Errorf("updating Outbound %q status: %w", outb.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateLayer2AttachmentConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData, issues map[string]ResourceIssue) error {
	for i := range fetched.Layer2Attachments {
		l2a := &fetched.Layer2Attachments[i]
		resolvedStatus, resolvedReason, resolvedMsg := checkNetworkRef(l2a.Spec.NetworkRef, resolved)

		readyStatus := resolvedStatus
		readyReason := resolvedReason
		readyMsg := "Layer2Attachment is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		readyStatus, readyReason, readyMsg = applyBuildIssue(issues, "Layer2Attachment", l2a.Name, readyStatus, readyReason, readyMsg)

		effIfName := effectiveInterfaceName(l2a, resolved)

		if err := u.statusUpdateWithRetry(ctx, l2a, func(obj client.Object) {
			la := obj.(*nc.Layer2Attachment)
			setCondition(&la.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, la.Generation)
			setCondition(&la.Status.Conditions, nc.ConditionTypeReady, readyStatus, readyReason, readyMsg, la.Generation)
			la.Status.InterfaceName = effIfName
			la.Status.ObservedGeneration = la.Generation
		}); err != nil {
			return fmt.Errorf("updating Layer2Attachment %q status: %w", l2a.Name, err)
		}
	}
	return nil
}

func (u *Updater) updatePodNetworkConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData, issues map[string]ResourceIssue) error {
	for i := range fetched.PodNetworks {
		pn := &fetched.PodNetworks[i]
		resolvedStatus, resolvedReason, resolvedMsg := checkNetworkRef(pn.Spec.NetworkRef, resolved)

		readyStatus := resolvedStatus
		readyReason := resolvedReason
		readyMsg := "PodNetwork is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		readyStatus, readyReason, readyMsg = applyBuildIssue(issues, "PodNetwork", pn.Name, readyStatus, readyReason, readyMsg)

		if err := u.statusUpdateWithRetry(ctx, pn, func(obj client.Object) {
			p := obj.(*nc.PodNetwork)
			setCondition(&p.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, p.Generation)
			setCondition(&p.Status.Conditions, nc.ConditionTypeReady, readyStatus, readyReason, readyMsg, p.Generation)
			p.Status.ObservedGeneration = p.Generation
		}); err != nil {
			return fmt.Errorf("updating PodNetwork %q status: %w", pn.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateCollectorConditions(ctx context.Context, fetched *resolver.FetchedResources, issues map[string]ResourceIssue) error {
	for i := range fetched.Collectors {
		col := &fetched.Collectors[i]
		readyStatus, readyReason, readyMsg := applyBuildIssue(issues, "Collector", col.Name,
			metav1.ConditionTrue, "Ready", "Collector is ready")
		if err := u.statusUpdateWithRetry(ctx, col, func(obj client.Object) {
			c := obj.(*nc.Collector)
			setCondition(&c.Status.Conditions, nc.ConditionTypeResolved, metav1.ConditionTrue, reasonAllResolved, "Collector references resolved", c.Generation)
			setCondition(&c.Status.Conditions, nc.ConditionTypeReady, readyStatus, readyReason, readyMsg, c.Generation)
			c.Status.ObservedGeneration = c.Generation
		}); err != nil {
			return fmt.Errorf("updating Collector %q status: %w", col.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateTrafficMirrorConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData, issues map[string]ResourceIssue) error {
	collectorNames := make(map[string]bool, len(resolved.Collectors))
	for i := range resolved.Collectors {
		collectorNames[resolved.Collectors[i].Name] = true
	}

	for i := range fetched.TrafficMirrors {
		tm := &fetched.TrafficMirrors[i]
		resolvedStatus := metav1.ConditionTrue
		resolvedReason := reasonAllResolved
		resolvedMsg := msgAllResolved

		if !collectorNames[tm.Spec.Collector] {
			resolvedStatus = metav1.ConditionFalse
			resolvedReason = "CollectorNotFound"
			resolvedMsg = fmt.Sprintf("referenced Collector %q not found", tm.Spec.Collector)
		}

		readyStatus := resolvedStatus
		readyReason := resolvedReason
		readyMsg := "TrafficMirror is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		readyStatus, readyReason, readyMsg = applyBuildIssue(issues, "TrafficMirror", tm.Name, readyStatus, readyReason, readyMsg)

		if err := u.statusUpdateWithRetry(ctx, tm, func(obj client.Object) {
			t := obj.(*nc.TrafficMirror)
			setCondition(&t.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, t.Generation)
			setCondition(&t.Status.Conditions, nc.ConditionTypeReady, readyStatus, readyReason, readyMsg, t.Generation)
			t.Status.ObservedGeneration = t.Generation
		}); err != nil {
			return fmt.Errorf("updating TrafficMirror %q status: %w", tm.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateNodeAttachmentConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData, issues map[string]ResourceIssue) error {
	for i := range fetched.NodeAttachments {
		na := &fetched.NodeAttachments[i]
		resolvedStatus := metav1.ConditionTrue
		resolvedReason := reasonAllResolved
		resolvedMsg := msgAllResolved

		if _, ok := resolved.VRFs[na.Spec.VRFRef]; !ok {
			resolvedStatus = metav1.ConditionFalse
			resolvedReason = "VRFNotFound"
			resolvedMsg = fmt.Sprintf("referenced VRF %q not found", na.Spec.VRFRef)
		}

		readyStatus := resolvedStatus
		readyReason := resolvedReason
		readyMsg := "NodeAttachment is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		readyStatus, readyReason, readyMsg = applyBuildIssue(issues, "NodeAttachment", na.Name, readyStatus, readyReason, readyMsg)

		if err := u.statusUpdateWithRetry(ctx, na, func(obj client.Object) {
			n := obj.(*nc.NodeAttachment)
			setCondition(&n.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, n.Generation)
			setCondition(&n.Status.Conditions, nc.ConditionTypeReady, readyStatus, readyReason, readyMsg, n.Generation)
			n.Status.ObservedGeneration = n.Generation
		}); err != nil {
			return fmt.Errorf("updating NodeAttachment %q status: %w", na.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateBGPPeeringConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData, issues map[string]ResourceIssue) error {
	for i := range fetched.BGPPeerings {
		bp := &fetched.BGPPeerings[i]
		resolvedStatus, resolvedReason, resolvedMsg := checkBGPPeeringRefs(bp, resolved)

		readyStatus := resolvedStatus
		readyReason := resolvedReason
		readyMsg := "BGPPeering is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		readyStatus, readyReason, readyMsg = applyBuildIssue(issues, "BGPPeering", bp.Name, readyStatus, readyReason, readyMsg)

		if err := u.statusUpdateWithRetry(ctx, bp, func(obj client.Object) {
			b := obj.(*nc.BGPPeering)
			setCondition(&b.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, b.Generation)
			setCondition(&b.Status.Conditions, nc.ConditionTypeReady, readyStatus, readyReason, readyMsg, b.Generation)
			b.Status.WorkloadASNumber = b.Spec.WorkloadAS
			b.Status.ObservedGeneration = b.Generation
		}); err != nil {
			return fmt.Errorf("updating BGPPeering %q status: %w", bp.Name, err)
		}
	}
	return nil
}

// checkBGPPeeringRefs validates that the references declared in a BGPPeering
// spec resolve to existing intent resources. The checks are mode-specific and
// mirror the requirements enforced by the BGPPeering builder:
//   - listenRange requires attachmentRef (an existing Layer2Attachment) and
//     networkRefs (each an existing Network).
//   - loopbackPeer requires inboundRefs (each an existing Inbound).
func checkBGPPeeringRefs(bp *nc.BGPPeering, resolved *resolver.ResolvedData) (condStatus metav1.ConditionStatus, reason, message string) {
	switch bp.Spec.Mode {
	case nc.BGPPeeringModeListenRange:
		if bp.Spec.Ref.AttachmentRef == nil {
			return metav1.ConditionFalse, "AttachmentRefMissing", "listenRange mode requires attachmentRef"
		}
		if !layer2AttachmentExists(*bp.Spec.Ref.AttachmentRef, resolved) {
			return metav1.ConditionFalse, "AttachmentNotFound", fmt.Sprintf("referenced Layer2Attachment %q not found", *bp.Spec.Ref.AttachmentRef)
		}
		for _, ref := range bp.Spec.Ref.NetworkRefs {
			if _, ok := resolved.Networks[ref]; !ok {
				return metav1.ConditionFalse, "NetworkNotFound", fmt.Sprintf("referenced Network %q not found", ref)
			}
		}
	case nc.BGPPeeringModeLoopbackPeer:
		for _, ref := range bp.Spec.Ref.InboundRefs {
			if !inboundExists(ref, resolved) {
				return metav1.ConditionFalse, "InboundNotFound", fmt.Sprintf("referenced Inbound %q not found", ref)
			}
		}
	}
	return metav1.ConditionTrue, reasonAllResolved, msgAllResolved
}

// layer2AttachmentExists reports whether a Layer2Attachment with the given name
// is present in the resolved data.
func layer2AttachmentExists(name string, resolved *resolver.ResolvedData) bool {
	for i := range resolved.Layer2Attachments {
		if resolved.Layer2Attachments[i].Name == name {
			return true
		}
	}
	return false
}

// inboundExists reports whether an Inbound with the given name is present in the
// resolved data.
func inboundExists(name string, resolved *resolver.ResolvedData) bool {
	for i := range resolved.Inbounds {
		if resolved.Inbounds[i].Name == name {
			return true
		}
	}
	return false
}

// checkNetworkRef checks if a networkRef resolves to an existing Network.
func checkNetworkRef(networkRef string, resolved *resolver.ResolvedData) (condStatus metav1.ConditionStatus, reason, message string) {
	if _, ok := resolved.Networks[networkRef]; !ok {
		return metav1.ConditionFalse, "NetworkNotFound", fmt.Sprintf("referenced Network %q not found", networkRef)
	}
	return metav1.ConditionTrue, reasonAllResolved, msgAllResolved
}

// effectiveInterfaceName returns the interface name the agent will create for
// the attachment: the explicit spec.interfaceName override when set, otherwise
// the default "vlan.<vlan>" derived from the referenced Network's VLAN. It
// returns "" when no override is set and the Network (or its VLAN) cannot be
// resolved. This mirrors the naming logic in intent.buildNetplanState so the
// status reflects exactly what lands on the node.
func effectiveInterfaceName(l2a *nc.Layer2Attachment, resolved *resolver.ResolvedData) string {
	if l2a.Spec.InterfaceName != nil && *l2a.Spec.InterfaceName != "" {
		return *l2a.Spec.InterfaceName
	}
	net, ok := resolved.Networks[l2a.Spec.NetworkRef]
	if !ok || net.Spec.VLAN == nil {
		return ""
	}
	return fmt.Sprintf("vlan.%d", *net.Spec.VLAN)
}

// setCondition is a helper to set a condition with observedGeneration.
func setCondition(conditions *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, message string, generation int64) {
	apimeta.SetStatusCondition(conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}
