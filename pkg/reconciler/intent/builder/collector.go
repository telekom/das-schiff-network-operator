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
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// CollectorBuilder transforms Collector intent CRDs into FabricVRF loopback entries.
type CollectorBuilder struct{}

// NewCollectorBuilder creates a new CollectorBuilder.
func NewCollectorBuilder() *CollectorBuilder {
	return &CollectorBuilder{}
}

// Name returns the builder name.
func (b *CollectorBuilder) Name() string {
	return "collector"
}

// Build produces per-node FabricVRF loopback contributions from Collector resources.
func (b *CollectorBuilder) Build(_ context.Context, data *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	result := make(map[string]*NodeContribution)

	for i := range data.Collectors {
		col := &data.Collectors[i]

		// Resolve mirror VRF.
		vrfName := col.Spec.MirrorVRF.Name
		resolvedVRF, ok := data.VRFs[vrfName]
		if !ok {
			return nil, fmt.Errorf("Collector %q references unknown VRF %q", col.Name, vrfName)
		}

		// Build loopback entry from the MirrorVRF config.
		loopbackName := col.Spec.MirrorVRF.Loopback.Name
		loopback := networkv1alpha1.Loopback{
			IPAddresses: []string{col.Spec.Address},
		}

		// Apply to all nodes.
		for _, node := range data.Nodes {
			contrib, ok := result[node.Name]
			if !ok {
				contrib = NewNodeContribution()
				result[node.Name] = contrib
			}

			fvrf, exists := contrib.FabricVRFs[vrfName]
			if !exists {
				fvrf = buildFabricVRF(&resolvedVRF.Spec)
			}

			if fvrf.Loopbacks == nil {
				fvrf.Loopbacks = make(map[string]networkv1alpha1.Loopback)
			}
			fvrf.Loopbacks[loopbackName] = loopback

			contrib.FabricVRFs[vrfName] = fvrf
		}
	}

	return result, nil
}
