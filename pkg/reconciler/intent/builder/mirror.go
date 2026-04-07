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

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// MirrorBuilder transforms TrafficMirror intent CRDs into MirrorACL entries.
type MirrorBuilder struct{}

// NewMirrorBuilder creates a new MirrorBuilder.
func NewMirrorBuilder() *MirrorBuilder {
	return &MirrorBuilder{}
}

// Name returns the builder name.
func (*MirrorBuilder) Name() string {
	return "mirror"
}

// Build produces per-node MirrorACL contributions from TrafficMirror resources.
func (b *MirrorBuilder) Build(_ context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	result := make(map[string]*NodeContribution)

	for i := range data.TrafficMirrors {
		tm := &data.TrafficMirrors[i]

		// Resolve collector.
		col, err := b.resolveCollector(tm.Spec.Collector, data)
		if err != nil {
			return nil, fmt.Errorf("TrafficMirror %q collector resolution failed: %w", tm.Name, err)
		}

		// Build MirrorACL.
		mirrorACL := b.buildMirrorACL(tm, col)

		// Resolve the source attachment and determine where to place the ACL.
		switch tm.Spec.Source.Kind {
		case "Layer2Attachment":
			if err := b.addToLayer2(tm.Spec.Source.Name, &mirrorACL, data, result); err != nil {
				return nil, fmt.Errorf("TrafficMirror %q source resolution failed: %w", tm.Name, err)
			}
		case "Inbound":
			if err := b.addToInboundVRF(tm.Spec.Source.Name, &mirrorACL, data, result); err != nil {
				return nil, fmt.Errorf("TrafficMirror %q source resolution failed: %w", tm.Name, err)
			}
		case "Outbound":
			if err := b.addToOutboundVRF(tm.Spec.Source.Name, &mirrorACL, data, result); err != nil {
				return nil, fmt.Errorf("TrafficMirror %q source resolution failed: %w", tm.Name, err)
			}
		default:
			return nil, fmt.Errorf("TrafficMirror %q has unknown source kind %q", tm.Name, tm.Spec.Source.Kind)
		}
	}

	return result, nil
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

// buildMirrorACL creates a MirrorACL from a TrafficMirror and its resolved Collector.
func (b *MirrorBuilder) buildMirrorACL(tm *nc.TrafficMirror, col *nc.Collector) networkv1alpha1.MirrorACL {
	acl := networkv1alpha1.MirrorACL{
		DestinationAddress: col.Spec.Address,
		DestinationVrf:     col.Spec.MirrorVRF.Name,
		EncapsulationType:  networkv1alpha1.EncapsulationTypeGRE,
		Direction:          tm.Spec.Direction,
	}

	if tm.Spec.TrafficMatch != nil {
		acl.TrafficMatch = b.convertTrafficMatch(tm.Spec.TrafficMatch)
	}

	return acl
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

// addToInboundVRF adds MirrorACL to the VRF associated with an Inbound on all nodes.
func (b *MirrorBuilder) addToInboundVRF(ibName string, acl *networkv1alpha1.MirrorACL, data *resolver.ResolvedData, result map[string]*NodeContribution) error {
	vrfName, vrfSpec, err := b.resolveInboundVRF(ibName, data)
	if err != nil {
		return err
	}
	if vrfName == "" {
		return fmt.Errorf("inbound %q has no VRF", ibName)
	}

	for i := range data.Nodes {
		node := &data.Nodes[i]
		contrib, ok := result[node.Name]
		if !ok {
			contrib = NewNodeContribution()
			result[node.Name] = contrib
		}

		fvrf, exists := contrib.FabricVRFs[vrfName]
		if !exists {
			fvrf = buildFabricVRF(vrfSpec)
		}
		fvrf.MirrorACLs = append(fvrf.MirrorACLs, *acl)
		contrib.FabricVRFs[vrfName] = fvrf
	}

	return nil
}

// addToOutboundVRF adds MirrorACL to the VRF associated with an Outbound on all nodes.
func (b *MirrorBuilder) addToOutboundVRF(obName string, acl *networkv1alpha1.MirrorACL, data *resolver.ResolvedData, result map[string]*NodeContribution) error {
	vrfName, vrfSpec, err := b.resolveOutboundVRF(obName, data)
	if err != nil {
		return err
	}
	if vrfName == "" {
		return fmt.Errorf("outbound %q has no VRF", obName)
	}

	for i := range data.Nodes {
		node := &data.Nodes[i]
		contrib, ok := result[node.Name]
		if !ok {
			contrib = NewNodeContribution()
			result[node.Name] = contrib
		}

		fvrf, exists := contrib.FabricVRFs[vrfName]
		if !exists {
			fvrf = buildFabricVRF(vrfSpec)
		}
		fvrf.MirrorACLs = append(fvrf.MirrorACLs, *acl)
		contrib.FabricVRFs[vrfName] = fvrf
	}

	return nil
}

// resolveInboundVRF finds the VRF name and spec for a named Inbound.
func (*MirrorBuilder) resolveInboundVRF(name string, data *resolver.ResolvedData) (string, *nc.VRFSpec, error) {
	for i := range data.Inbounds {
		if data.Inbounds[i].Name != name {
			continue
		}
		ib := &data.Inbounds[i]
		if ib.Spec.Destinations == nil {
			return "", nil, nil
		}
		for _, resolved := range data.Destinations {
			if resolved.VRFSpec != nil && resolved.Spec.VRFRef != nil {
				return *resolved.Spec.VRFRef, resolved.VRFSpec, nil
			}
		}
		return "", nil, nil
	}
	return "", nil, fmt.Errorf("inbound %q not found", name)
}

// resolveOutboundVRF finds the VRF name and spec for a named Outbound.
func (*MirrorBuilder) resolveOutboundVRF(name string, data *resolver.ResolvedData) (string, *nc.VRFSpec, error) {
	for i := range data.Outbounds {
		if data.Outbounds[i].Name != name {
			continue
		}
		ob := &data.Outbounds[i]
		if ob.Spec.Destinations == nil {
			return "", nil, nil
		}
		for _, resolved := range data.Destinations {
			if resolved.VRFSpec != nil && resolved.Spec.VRFRef != nil {
				return *resolved.Spec.VRFRef, resolved.VRFSpec, nil
			}
		}
		return "", nil, nil
	}
	return "", nil, fmt.Errorf("outbound %q not found", name)
}
