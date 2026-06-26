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

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// NetplanNodeIP holds per-VLAN netplan device info that is rendered into the
// NodeNetplanConfig but does not belong on the NodeNetworkConfig API surface.
// It carries the optional interface name/parent overrides and, when NodeIPConfig
// is enabled on an L2A, the per-node IP addresses and IRB anycast gateways.
type NetplanNodeIP struct {
	// Addresses are per-node IPs with prefix length (e.g., "10.0.1.10/24").
	Addresses []string
	// Gateways are the IRB anycast gateway IPs (e.g., "10.0.1.1").
	Gateways []string
	// InterfaceName overrides the default VLAN sub-interface name (vlan.<ID>).
	InterfaceName string
	// InterfaceRef is the parent/master interface for the VLAN sub-interface.
	// Defaults to the HBN trunk interface if empty.
	InterfaceRef string
}

// NodeContribution is what each builder produces for a single node.
// The assembler merges contributions from all builders into a final NNC spec.
type NodeContribution struct {
	Layer2s    map[string]networkv1alpha1.Layer2
	FabricVRFs map[string]networkv1alpha1.FabricVRF
	LocalVRFs  map[string]networkv1alpha1.VRF
	ClusterVRF *networkv1alpha1.VRF
	// NetplanNodeIPs maps Layer2 keys to per-node IP info for netplan config.
	// Populated by the L2A builder when nodeIPs.enabled is set.
	NetplanNodeIPs map[string]NetplanNodeIP
	// Origins maps NNC section keys to their source intent CRDs
	// (e.g., "layer2s/prod-vlan100" → "Layer2Attachment/my-l2a").
	Origins map[string]string
}

// NewNodeContribution creates an initialized NodeContribution.
func NewNodeContribution() *NodeContribution {
	return &NodeContribution{
		Layer2s:        make(map[string]networkv1alpha1.Layer2),
		FabricVRFs:     make(map[string]networkv1alpha1.FabricVRF),
		LocalVRFs:      make(map[string]networkv1alpha1.VRF),
		NetplanNodeIPs: make(map[string]NetplanNodeIP),
		Origins:        make(map[string]string),
	}
}

// SetOrigin records the source CRD for an NNC section key.
func (nc *NodeContribution) SetOrigin(sectionKey, source string) {
	if nc.Origins == nil {
		nc.Origins = make(map[string]string)
	}
	nc.Origins[sectionKey] = source
}

// ensureContrib returns the existing contribution for a node or creates a new one.
func ensureContrib(result map[string]*NodeContribution, nodeName string) *NodeContribution {
	contrib, ok := result[nodeName]
	if !ok {
		contrib = NewNodeContribution()
		result[nodeName] = contrib
	}
	return contrib
}

// Builder is the interface for concern-area builders.
// Each builder transforms a subset of intent CRDs into per-node NNC contributions.
type Builder interface {
	// Build produces per-node contributions from resolved intent data.
	// Returns a map of node name → contribution.
	Build(ctx context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error)

	// Name returns the builder name for logging and metrics.
	Name() string
}
