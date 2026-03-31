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

// NodeContribution is what each builder produces for a single node.
// The assembler merges contributions from all builders into a final NNC spec.
type NodeContribution struct {
	Layer2s    map[string]networkv1alpha1.Layer2
	FabricVRFs map[string]networkv1alpha1.FabricVRF
	LocalVRFs  map[string]networkv1alpha1.VRF
	ClusterVRF *networkv1alpha1.VRF
}

// NewNodeContribution creates an initialized NodeContribution.
func NewNodeContribution() *NodeContribution {
	return &NodeContribution{
		Layer2s:    make(map[string]networkv1alpha1.Layer2),
		FabricVRFs: make(map[string]networkv1alpha1.FabricVRF),
		LocalVRFs:  make(map[string]networkv1alpha1.VRF),
	}
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
