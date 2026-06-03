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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// NodeAttachmentBuilder transforms NodeAttachment intent CRDs into FabricVRF contributions.
// It exports node IPs as host routes into the referenced VRF and imports the VRF's
// destination prefixes back into the cluster VRF via a direct VRFImport.
type NodeAttachmentBuilder struct{}

// NewNodeAttachmentBuilder creates a new NodeAttachmentBuilder.
func NewNodeAttachmentBuilder() *NodeAttachmentBuilder {
	return &NodeAttachmentBuilder{}
}

// Name returns the builder name.
func (*NodeAttachmentBuilder) Name() string {
	return "nodeattachment"
}

// Build produces per-node FabricVRF contributions from NodeAttachment resources.
func (b *NodeAttachmentBuilder) Build(ctx context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	logger := log.FromContext(ctx).WithName("nodeattachment-builder")
	result := make(map[string]*NodeContribution)

	for i := range data.NodeAttachments {
		na := &data.NodeAttachments[i]

		// Resolve VRF spec via destination selector (same pattern as other builders).
		grouped := groupDestinationsByVRF(na.Spec.Destinations, data)
		if len(grouped) == 0 {
			continue
		}

		// Filter nodes by selector.
		matchedNodes, err := matchNodes(data.Nodes, na.Spec.NodeSelector)
		if err != nil {
			logger.Info("skipping NodeAttachment with invalid node selector",
				"nodeattachment", na.Name, "error", err.Error())
			reportSkip(ctx, "NodeAttachment", na.Name, "InvalidNodeSelector", err.Error())
			continue
		}
		if len(matchedNodes) == 0 {
			continue
		}

		// For each VRF matched by the Destinations, build FabricVRF contributions.
		for vrfName, dests := range grouped {
			vrfSpec := b.resolveVRFSpec(vrfName, grouped, data)
			if vrfSpec == nil {
				continue
			}

			// Collect all destination prefixes for this VRF.
			destPrefixes := make([]string, 0, len(dests))
			for di := range dests {
				destPrefixes = append(destPrefixes, dests[di].Spec.Prefixes...)
			}

			b.applyToNodes(vrfName, vrfSpec, destPrefixes, matchedNodes, result)
		}
	}

	return result, nil
}

// applyToNodes creates per-node contributions:
//   - FabricVRF: exports node IPs into the remote VRF (EVPN + cluster import)
//   - ClusterVRF: imports destination prefixes from the remote VRF back into cluster
func (*NodeAttachmentBuilder) applyToNodes(
	vrfName string,
	vrfSpec *nc.VRFSpec,
	destPrefixes []string,
	nodes []corev1.Node,
	result map[string]*NodeContribution,
) {
	// Build filter items for destination prefixes (accept all).
	destFilterItems := prefixFilterItems(destPrefixes)

	for i := range nodes {
		node := &nodes[i]
		nodeIPs := collectNodeIPs(node)
		if len(nodeIPs) == 0 {
			continue
		}

		contrib := ensureContrib(result, node.Name)

		// --- FabricVRF: export node IPs into the remote VRF ---
		fvrf, exists := contrib.FabricVRFs[vrfName]
		if !exists {
			fvrf = buildFabricVRF(vrfSpec)
		}

		// Add node IPs as host routes to EVPN export filter (no AP — simple accept).
		evpnItems := addressFilterItems(nodeIPs, nil)
		if fvrf.EVPNExportFilter != nil {
			fvrf.EVPNExportFilter.Items = append(fvrf.EVPNExportFilter.Items, evpnItems...)
		}

		// Add node IPs to the FabricVRF's cluster VRF import filter
		// (so node IPs are leaked from cluster into the fabric VRF).
		if len(fvrf.VRFImports) > 0 {
			fvrf.VRFImports[0].Filter.Items = append(fvrf.VRFImports[0].Filter.Items, evpnItems...)
		}

		contrib.FabricVRFs[vrfName] = fvrf

		// --- ClusterVRF: import destination prefixes from the remote VRF ---
		if len(destFilterItems) > 0 {
			if contrib.ClusterVRF == nil {
				contrib.ClusterVRF = &networkv1alpha1.VRF{}
			}
			contrib.ClusterVRF.VRFImports = appendVRFImport(contrib.ClusterVRF.VRFImports, vrfName, destFilterItems)
		}
	}
}

// appendVRFImport adds filter items to an existing VRFImport for the given VRF,
// or creates a new VRFImport if one doesn't exist yet.
func appendVRFImport(imports []networkv1alpha1.VRFImport, fromVRF string, items []networkv1alpha1.FilterItem) []networkv1alpha1.VRFImport {
	for i := range imports {
		if imports[i].FromVRF == fromVRF {
			imports[i].Filter.Items = append(imports[i].Filter.Items, items...)
			return imports
		}
	}
	return append(imports, networkv1alpha1.VRFImport{
		FromVRF: fromVRF,
		Filter: networkv1alpha1.Filter{
			Items:         items,
			DefaultAction: networkv1alpha1.Action{Type: networkv1alpha1.Reject},
		},
	})
}

// prefixFilterItems creates accept FilterItems for a list of prefixes.
func prefixFilterItems(prefixes []string) []networkv1alpha1.FilterItem {
	items := make([]networkv1alpha1.FilterItem, 0, len(prefixes))
	for _, prefix := range prefixes {
		le := ipv4MaxPrefixLen
		if strings.Contains(prefix, ":") {
			le = ipv6MaxPrefixLen
		}
		items = append(items, networkv1alpha1.FilterItem{
			Action: networkv1alpha1.Action{Type: networkv1alpha1.Accept},
			Matcher: networkv1alpha1.Matcher{
				Prefix: &networkv1alpha1.PrefixMatcher{Prefix: prefix, Le: &le},
			},
		})
	}
	return items
}

// resolveVRFSpec finds the VRFSpec for a given VRF name from grouped destinations.
func (*NodeAttachmentBuilder) resolveVRFSpec(vrfName string, grouped map[string][]nc.Destination, data *resolver.ResolvedData) *nc.VRFSpec {
	dests := grouped[vrfName]
	if len(dests) == 0 {
		return nil
	}
	resolved, ok := data.Destinations[dests[0].Name]
	if !ok || resolved.VRFSpec == nil {
		return nil
	}
	return resolved.VRFSpec
}

// collectNodeIPs extracts InternalIP addresses from a node's status as CIDR host routes.
func collectNodeIPs(node *corev1.Node) []string {
	var ips []string
	for _, addr := range node.Status.Addresses {
		if addr.Type != corev1.NodeInternalIP {
			continue
		}
		if addr.Address == "" {
			continue
		}
		suffix := "/32"
		if strings.Contains(addr.Address, ":") {
			suffix = "/128"
		}
		ips = append(ips, addr.Address+suffix)
	}
	return ips
}
