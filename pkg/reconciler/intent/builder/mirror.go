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

package builder

import (
	"context"
	"fmt"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/log"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// MirrorBuilder transforms TrafficMirror intent CRDs into MirrorACL entries.
type MirrorBuilder struct{}

// TrafficMirror source kinds.
const (
	mirrorSourceLayer2Attachment = "Layer2Attachment"
	mirrorSourceInbound          = "Inbound"
	mirrorSourceOutbound         = "Outbound"
)

// NewMirrorBuilder creates a new MirrorBuilder.
func NewMirrorBuilder() *MirrorBuilder {
	return &MirrorBuilder{}
}

// Name returns the builder name.
func (*MirrorBuilder) Name() string {
	return "mirror"
}

// Build produces per-node MirrorACL contributions from TrafficMirror resources.
//
// The MirrorACL references the GRE tunnel created by the CollectorBuilder
// (by interface name via MirrorDestination), matching the NodeNetworkConfig
// southbound shape used by the legacy (MirrorSelector/MirrorTarget) pipeline.
// A "both"-direction TrafficMirror is expanded into one ingress and one egress
// MirrorACL, because the southbound MirrorACL only models a single direction.
func (b *MirrorBuilder) Build(ctx context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	logger := log.FromContext(ctx).WithName("mirror-builder")
	result := make(map[string]*NodeContribution)

	for i := range data.TrafficMirrors {
		tm := &data.TrafficMirrors[i]

		// Resolve collector.
		col, err := b.resolveCollector(tm.Spec.Collector, data)
		if err != nil {
			logger.Info("skipping TrafficMirror with unresolvable collector",
				"trafficmirror", tm.Name, "error", err.Error())
			reportSkip(ctx, "TrafficMirror", tm.Namespace, tm.Name, "CollectorResolutionFailed", err.Error())
			continue
		}

		if !validMirrorSourceKind(tm.Spec.Source.Kind) {
			logger.Info("skipping TrafficMirror with unknown source kind",
				"trafficmirror", tm.Name, "kind", tm.Spec.Source.Kind)
			reportSkip(ctx, "TrafficMirror", tm.Namespace, tm.Name, "UnknownSourceKind",
				fmt.Sprintf("unknown source kind %q", tm.Spec.Source.Kind))
			continue
		}

		// The GRE tunnel is created by the CollectorBuilder; reference it by name.
		greName := mirrorGREName(col)
		var match networkv1alpha1.TrafficMatch
		if tm.Spec.TrafficMatch != nil {
			match = b.convertTrafficMatch(tm.Spec.TrafficMatch)
		}

		var srcErr error
		for _, direction := range mirrorDirections(tm.Spec.Direction) {
			acl := networkv1alpha1.MirrorACL{
				TrafficMatch:      match,
				MirrorDestination: greName,
				Direction:         direction,
			}
			if srcErr = b.attachToSource(tm, &acl, data, result); srcErr != nil {
				break
			}
		}
		if srcErr != nil {
			logger.Info("skipping TrafficMirror with unresolvable source",
				"trafficmirror", tm.Name, "error", srcErr.Error())
			reportSkip(ctx, "TrafficMirror", tm.Namespace, tm.Name, "SourceResolutionFailed", srcErr.Error())
			continue
		}
	}

	return result, nil
}

// validMirrorSourceKind reports whether the TrafficMirror source kind is supported.
func validMirrorSourceKind(kind string) bool {
	switch kind {
	case mirrorSourceLayer2Attachment, mirrorSourceInbound, mirrorSourceOutbound:
		return true
	default:
		return false
	}
}

// mirrorDirections expands a TrafficMirror direction ("ingress", "egress", "both"
// or empty) into the concrete per-ACL directions. The southbound MirrorACL models
// a single direction only, so "both" (the default) yields ingress and egress.
func mirrorDirections(direction string) []networkv1alpha1.MirrorDirection {
	switch direction {
	case string(networkv1alpha1.MirrorDirectionIngress):
		return []networkv1alpha1.MirrorDirection{networkv1alpha1.MirrorDirectionIngress}
	case string(networkv1alpha1.MirrorDirectionEgress):
		return []networkv1alpha1.MirrorDirection{networkv1alpha1.MirrorDirectionEgress}
	default:
		return []networkv1alpha1.MirrorDirection{
			networkv1alpha1.MirrorDirectionIngress,
			networkv1alpha1.MirrorDirectionEgress,
		}
	}
}

// attachToSource places the MirrorACL on the TrafficMirror's source (a Layer2
// attachment, an Inbound's VRFs or an Outbound's VRFs) across all nodes.
func (b *MirrorBuilder) attachToSource(tm *nc.TrafficMirror, acl *networkv1alpha1.MirrorACL, data *resolver.ResolvedData, result map[string]*NodeContribution) error {
	switch tm.Spec.Source.Kind {
	case mirrorSourceLayer2Attachment:
		return b.addToLayer2(tm.Spec.Source.Name, acl, data, result)
	case mirrorSourceInbound:
		return b.addToInboundVRF(tm.Spec.Source.Name, acl, data, result)
	case mirrorSourceOutbound:
		return b.addToOutboundVRF(tm.Spec.Source.Name, acl, data, result)
	default:
		return fmt.Errorf("unknown source kind %q", tm.Spec.Source.Kind)
	}
}

// resolveCollector finds a Collector by name.
func (*MirrorBuilder) resolveCollector(name string, data *resolver.ResolvedData) (*nc.Collector, error) {
	for i := range data.Collectors {
		if data.Collectors[i].Name == name {
			return &data.Collectors[i], nil
		}
	}
	return nil, fmt.Errorf("collector %q not found", name)
}

// convertTrafficMatch converts a nc.TrafficMatch to a networkv1alpha1.TrafficMatch.
func (*MirrorBuilder) convertTrafficMatch(tm *nc.TrafficMatch) networkv1alpha1.TrafficMatch {
	result := networkv1alpha1.TrafficMatch{
		Protocol: tm.Protocol,
	}
	if tm.SrcPrefix != nil {
		result.SrcPrefix = tm.SrcPrefix
	}
	if tm.DstPrefix != nil {
		result.DstPrefix = tm.DstPrefix
	}
	if tm.SrcPort != nil {
		port := uint16(*tm.SrcPort) //nolint:gosec // value validated by CRD schema (positive integer)
		result.SrcPort = &port
	}
	if tm.DstPort != nil {
		port := uint16(*tm.DstPort) //nolint:gosec // value validated by CRD schema (positive integer)
		result.DstPort = &port
	}
	return result
}

// addToLayer2 adds MirrorACL to a Layer2 entry identified by L2A name on all nodes.
func (*MirrorBuilder) addToLayer2(l2aName string, acl *networkv1alpha1.MirrorACL, data *resolver.ResolvedData, result map[string]*NodeContribution) error {
	// Find the L2A.
	var l2a *nc.Layer2Attachment
	for j := range data.Layer2Attachments {
		if data.Layer2Attachments[j].Name == l2aName {
			l2a = &data.Layer2Attachments[j]
			break
		}
	}
	if l2a == nil {
		return fmt.Errorf("Layer2Attachment %q not found", l2aName)
	}

	// Resolve Network to get the VLAN for the map key.
	net, ok := data.Networks[l2a.Spec.NetworkRef]
	if !ok {
		return fmt.Errorf("Layer2Attachment %q references unknown Network %q", l2a.Name, l2a.Spec.NetworkRef)
	}
	// MirrorACLs are programmed in the CRA (HBN) datapath on the L2VNI. A source
	// that is not a valid HBN L2 segment (needs both a VNI and a VLAN) is applied
	// by the netplan agent and cannot carry mirror rules. Creating a bare Layer2
	// entry here (VLAN/VNI/MTU = 0) would also make the NodeNetworkConfig invalid.
	if net.Spec.VNI == nil || net.Spec.VLAN == nil {
		return fmt.Errorf("Layer2Attachment %q references Network %q that is not a valid HBN L2 segment (needs both VNI and VLAN); traffic mirroring is only supported on HBN segments",
			l2a.Name, l2a.Spec.NetworkRef)
	}
	var vlan int32
	if net.Spec.VLAN != nil {
		vlan = *net.Spec.VLAN
	}
	mapKey := fmt.Sprintf("%d", vlan)

	// Apply to all nodes.
	for i := range data.Nodes {
		node := &data.Nodes[i]
		contrib, ok := result[node.Name]
		if !ok {
			contrib = NewNodeContribution()
			result[node.Name] = contrib
		}

		layer2, exists := contrib.Layer2s[mapKey]
		if !exists {
			layer2 = networkv1alpha1.Layer2{}
		}
		layer2.MirrorACLs = append(layer2.MirrorACLs, *acl)
		contrib.Layer2s[mapKey] = layer2
	}

	return nil
}

// addToInboundVRF adds MirrorACL to every VRF associated with an Inbound's
// Destinations selector, on all nodes. When the selector matches multiple
// Destinations across different VRFs, the ACL is fanned out to each VRF.
func (b *MirrorBuilder) addToInboundVRF(ibName string, acl *networkv1alpha1.MirrorACL, data *resolver.ResolvedData, result map[string]*NodeContribution) error {
	vrfs, err := b.resolveInboundVRFs(ibName, data)
	if err != nil {
		return err
	}
	if len(vrfs) == 0 {
		return fmt.Errorf("inbound %q has no VRF", ibName)
	}
	b.applyMirrorACLToVRFs(vrfs, acl, data, result)
	return nil
}

// addToOutboundVRF adds MirrorACL to every VRF associated with an Outbound's
// Destinations selector, on all nodes.
func (b *MirrorBuilder) addToOutboundVRF(obName string, acl *networkv1alpha1.MirrorACL, data *resolver.ResolvedData, result map[string]*NodeContribution) error {
	vrfs, err := b.resolveOutboundVRFs(obName, data)
	if err != nil {
		return err
	}
	if len(vrfs) == 0 {
		return fmt.Errorf("outbound %q has no VRF", obName)
	}
	b.applyMirrorACLToVRFs(vrfs, acl, data, result)
	return nil
}

// applyMirrorACLToVRFs writes the ACL into each named VRF on every node,
// creating the FabricVRF entry on demand.
func (*MirrorBuilder) applyMirrorACLToVRFs(vrfs map[string]*nc.VRFSpec, acl *networkv1alpha1.MirrorACL, data *resolver.ResolvedData, result map[string]*NodeContribution) {
	// Sorted iteration for deterministic output.
	names := make([]string, 0, len(vrfs))
	for n := range vrfs {
		names = append(names, n)
	}
	sort.Strings(names)

	for i := range data.Nodes {
		node := &data.Nodes[i]
		contrib, ok := result[node.Name]
		if !ok {
			contrib = NewNodeContribution()
			result[node.Name] = contrib
		}
		for _, vrfName := range names {
			fvrf, exists := contrib.FabricVRFs[vrfName]
			if !exists {
				fvrf = buildFabricVRF(vrfs[vrfName])
			}
			fvrf.MirrorACLs = append(fvrf.MirrorACLs, *acl)
			contrib.FabricVRFs[vrfName] = fvrf
		}
	}
}

// resolveInboundVRFs returns every VRF (name → spec) matched by the Inbound's
// Destinations selector. An empty map means the Inbound has no Destinations,
// which is valid (consumer is purely informational).
func (*MirrorBuilder) resolveInboundVRFs(name string, data *resolver.ResolvedData) (map[string]*nc.VRFSpec, error) {
	for i := range data.Inbounds {
		if data.Inbounds[i].Name != name {
			continue
		}
		ib := &data.Inbounds[i]
		if ib.Spec.Destinations == nil {
			return nil, nil
		}
		return resolveSelectorVRFs(ib.Spec.Destinations, data), nil
	}
	return nil, fmt.Errorf("inbound %q not found", name)
}

// resolveOutboundVRFs returns every VRF (name → spec) matched by the Outbound's
// Destinations selector.
func (*MirrorBuilder) resolveOutboundVRFs(name string, data *resolver.ResolvedData) (map[string]*nc.VRFSpec, error) {
	for i := range data.Outbounds {
		if data.Outbounds[i].Name != name {
			continue
		}
		ob := &data.Outbounds[i]
		if ob.Spec.Destinations == nil {
			return nil, nil
		}
		return resolveSelectorVRFs(ob.Spec.Destinations, data), nil
	}
	return nil, fmt.Errorf("outbound %q not found", name)
}
