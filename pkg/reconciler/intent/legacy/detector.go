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

package legacy

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Detector checks for conflicts between intent and legacy CRDs.
type Detector struct {
	client client.Client
	logger logr.Logger
}

// NewDetector creates a new legacy conflict Detector.
func NewDetector(c client.Client, logger logr.Logger) *Detector {
	return &Detector{
		client: c,
		logger: logger.WithName("legacy-detector"),
	}
}

// DetectConflicts lists any legacy CRDs that exist and logs warnings.
// Returns the list of conflict descriptions. Does not block reconciliation.
func (d *Detector) DetectConflicts(ctx context.Context) ([]string, error) {
	var conflicts []string

	// Check Layer2NetworkConfiguration resources.
	l2ncList := &networkv1alpha1.Layer2NetworkConfigurationList{}
	if err := d.client.List(ctx, l2ncList); err != nil {
		return nil, fmt.Errorf("listing legacy Layer2NetworkConfigurations: %w", err)
	}
	for i := range l2ncList.Items {
		msg := fmt.Sprintf("legacy Layer2NetworkConfiguration %q exists", l2ncList.Items[i].Name)
		conflicts = append(conflicts, msg)
	}

	// Check VRFRouteConfiguration resources.
	vrcList := &networkv1alpha1.VRFRouteConfigurationList{}
	if err := d.client.List(ctx, vrcList); err != nil {
		return nil, fmt.Errorf("listing legacy VRFRouteConfigurations: %w", err)
	}
	for i := range vrcList.Items {
		msg := fmt.Sprintf("legacy VRFRouteConfiguration %q exists", vrcList.Items[i].Name)
		conflicts = append(conflicts, msg)
	}

	// Check legacy BGPPeering resources (network.t-caas.telekom.com group).
	bgpList := &networkv1alpha1.BGPPeeringList{}
	if err := d.client.List(ctx, bgpList); err != nil {
		return nil, fmt.Errorf("listing legacy BGPPeerings: %w", err)
	}
	for i := range bgpList.Items {
		msg := fmt.Sprintf("legacy BGPPeering %q exists", bgpList.Items[i].Name)
		conflicts = append(conflicts, msg)
	}

	if len(conflicts) > 0 {
		d.logger.Info("legacy CRD conflicts detected — consider migrating to intent CRDs",
			"count", len(conflicts), "conflicts", conflicts)
	}

	return conflicts, nil
}
