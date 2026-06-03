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

	"sigs.k8s.io/controller-runtime/pkg/log"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// CollectorBuilder transforms Collector intent CRDs into FabricVRF loopback entries.
type CollectorBuilder struct{}

// NewCollectorBuilder creates a new CollectorBuilder.
func NewCollectorBuilder() *CollectorBuilder {
	return &CollectorBuilder{}
}

// Name returns the builder name.
func (*CollectorBuilder) Name() string {
	return "collector"
}

// Build produces per-node FabricVRF loopback contributions from Collector resources.
// Each Collector's loopback source IP is taken from Collector.status.nodeAddresses
// (allocated by the intent reconciler from spec.mirrorVRF.loopback.subnet).
// Nodes without an allocation are skipped silently — the reconciler raises a
// degraded AddressesAllocated condition in that case.
func (*CollectorBuilder) Build(ctx context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	logger := log.FromContext(ctx).WithName("collector-builder")
	result := make(map[string]*NodeContribution)

	for i := range data.Collectors {
		col := &data.Collectors[i]

		// Resolve mirror VRF.
		vrfName := col.Spec.MirrorVRF.Name
		resolvedVRF, ok := data.VRFs[vrfName]
		if !ok {
			logger.Info("skipping Collector with unknown VRF reference",
				"collector", col.Name, "vrf", vrfName)
			continue
		}
		// Use the backbone VRF name (spec.vrf) for the FabricVRF map key,
		// matching the convention used by L2A and SBR builders.
		backboneVRF := resolvedVRF.Spec.VRF

		loopbackName := col.Spec.MirrorVRF.Loopback.Name

		// Apply to all nodes that have an allocated loopback address.
		for i := range data.Nodes {
			nodeName := data.Nodes[i].Name
			addr, ok := col.Status.NodeAddresses[nodeName]
			if !ok || addr == "" {
				// No allocation yet — skip; the reconciler reports
				// the degraded condition on the Collector itself.
				continue
			}

			contrib, ok := result[nodeName]
			if !ok {
				contrib = NewNodeContribution()
				result[nodeName] = contrib
			}

			fvrf, exists := contrib.FabricVRFs[backboneVRF]
			if !exists {
				fvrf = buildFabricVRF(&resolvedVRF.Spec)
			}

			if fvrf.Loopbacks == nil {
				fvrf.Loopbacks = make(map[string]networkv1alpha1.Loopback)
			}
			fvrf.Loopbacks[loopbackName] = networkv1alpha1.Loopback{
				IPAddresses: []string{addr},
			}

			contrib.FabricVRFs[backboneVRF] = fvrf
		}
	}

	return result, nil
}
