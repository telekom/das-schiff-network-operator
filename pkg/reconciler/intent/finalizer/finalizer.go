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

package finalizer

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Manager handles in-use finalizer lifecycle for intent CRDs.
type Manager struct {
	client client.Client
	logger logr.Logger
}

// NewManager creates a new finalizer Manager.
func NewManager(c client.Client, logger logr.Logger) *Manager {
	return &Manager{
		client: c,
		logger: logger.WithName("finalizer-manager"),
	}
}

// ReconcileFinalizers checks all references and adds/removes finalizers.
// Called from IntentReconciler.ReconcileDebounced after fetching all resources.
func (m *Manager) ReconcileFinalizers(ctx context.Context, fetched *resolver.FetchedResources) error {
	if err := m.reconcileVRFFinalizers(ctx, fetched); err != nil {
		return fmt.Errorf("VRF finalizers: %w", err)
	}
	if err := m.reconcileNetworkFinalizers(ctx, fetched); err != nil {
		return fmt.Errorf("Network finalizers: %w", err)
	}
	if err := m.reconcileDestinationFinalizers(ctx, fetched); err != nil {
		return fmt.Errorf("Destination finalizers: %w", err)
	}
	if err := m.reconcileCollectorFinalizers(ctx, fetched); err != nil {
		return fmt.Errorf("Collector finalizers: %w", err)
	}
	return nil
}

// reconcileVRFFinalizers adds/removes vrf-in-use finalizer based on Destination vrfRef references.
func (m *Manager) reconcileVRFFinalizers(ctx context.Context, fetched *resolver.FetchedResources) error {
	referencedVRFs := make(map[string]bool)
	for i := range fetched.Destinations {
		if fetched.Destinations[i].Spec.VRFRef != nil {
			referencedVRFs[*fetched.Destinations[i].Spec.VRFRef] = true
		}
	}

	for i := range fetched.AllVRFs {
		vrf := &fetched.AllVRFs[i]
		if referencedVRFs[vrf.Name] {
			if !controllerutil.ContainsFinalizer(vrf, nc.FinalizerVRFInUse) {
				controllerutil.AddFinalizer(vrf, nc.FinalizerVRFInUse)
				if err := m.client.Update(ctx, vrf); err != nil {
					return fmt.Errorf("adding finalizer to VRF %q: %w", vrf.Name, err)
				}
				m.logger.V(1).Info("added vrf-in-use finalizer", "vrf", vrf.Name)
			}
		} else {
			if controllerutil.ContainsFinalizer(vrf, nc.FinalizerVRFInUse) {
				controllerutil.RemoveFinalizer(vrf, nc.FinalizerVRFInUse)
				if err := m.client.Update(ctx, vrf); err != nil {
					return fmt.Errorf("removing finalizer from VRF %q: %w", vrf.Name, err)
				}
				m.logger.V(1).Info("removed vrf-in-use finalizer", "vrf", vrf.Name)
			}
		}
	}
	return nil
}

// reconcileNetworkFinalizers adds/removes network-in-use finalizer based on networkRef references
// from Layer2Attachment, Inbound, Outbound, and PodNetwork.
func (m *Manager) reconcileNetworkFinalizers(ctx context.Context, fetched *resolver.FetchedResources) error {
	referencedNetworks := make(map[string]bool)

	for i := range fetched.Layer2Attachments {
		referencedNetworks[fetched.Layer2Attachments[i].Spec.NetworkRef] = true
	}
	for i := range fetched.Inbounds {
		referencedNetworks[fetched.Inbounds[i].Spec.NetworkRef] = true
	}
	for i := range fetched.Outbounds {
		referencedNetworks[fetched.Outbounds[i].Spec.NetworkRef] = true
	}
	for i := range fetched.PodNetworks {
		referencedNetworks[fetched.PodNetworks[i].Spec.NetworkRef] = true
	}

	for i := range fetched.AllNetworks {
		net := &fetched.AllNetworks[i]
		if referencedNetworks[net.Name] {
			if !controllerutil.ContainsFinalizer(net, nc.FinalizerNetworkInUse) {
				controllerutil.AddFinalizer(net, nc.FinalizerNetworkInUse)
				if err := m.client.Update(ctx, net); err != nil {
					return fmt.Errorf("adding finalizer to Network %q: %w", net.Name, err)
				}
				m.logger.V(1).Info("added network-in-use finalizer", "network", net.Name)
			}
		} else {
			if controllerutil.ContainsFinalizer(net, nc.FinalizerNetworkInUse) {
				controllerutil.RemoveFinalizer(net, nc.FinalizerNetworkInUse)
				if err := m.client.Update(ctx, net); err != nil {
					return fmt.Errorf("removing finalizer from Network %q: %w", net.Name, err)
				}
				m.logger.V(1).Info("removed network-in-use finalizer", "network", net.Name)
			}
		}
	}
	return nil
}

// reconcileDestinationFinalizers adds/removes destination-in-use finalizer based on label selector
// matches from Layer2Attachment, Inbound, Outbound, and PodNetwork.
func (m *Manager) reconcileDestinationFinalizers(ctx context.Context, fetched *resolver.FetchedResources) error {
	selectors := collectDestinationSelectors(fetched)

	for i := range fetched.AllDestinations {
		dest := &fetched.AllDestinations[i]
		selected := isSelectedByAny(dest.Labels, selectors)

		if selected {
			if !controllerutil.ContainsFinalizer(dest, nc.FinalizerDestinationInUse) {
				controllerutil.AddFinalizer(dest, nc.FinalizerDestinationInUse)
				if err := m.client.Update(ctx, dest); err != nil {
					return fmt.Errorf("adding finalizer to Destination %q: %w", dest.Name, err)
				}
				m.logger.V(1).Info("added destination-in-use finalizer", "destination", dest.Name)
			}
		} else {
			if controllerutil.ContainsFinalizer(dest, nc.FinalizerDestinationInUse) {
				controllerutil.RemoveFinalizer(dest, nc.FinalizerDestinationInUse)
				if err := m.client.Update(ctx, dest); err != nil {
					return fmt.Errorf("removing finalizer from Destination %q: %w", dest.Name, err)
				}
				m.logger.V(1).Info("removed destination-in-use finalizer", "destination", dest.Name)
			}
		}
	}
	return nil
}

// reconcileCollectorFinalizers adds/removes collector-in-use finalizer based on TrafficMirror references.
func (m *Manager) reconcileCollectorFinalizers(ctx context.Context, fetched *resolver.FetchedResources) error {
	referencedCollectors := make(map[string]bool)
	for i := range fetched.TrafficMirrors {
		referencedCollectors[fetched.TrafficMirrors[i].Spec.Collector] = true
	}

	for i := range fetched.Collectors {
		col := &fetched.Collectors[i]
		if referencedCollectors[col.Name] {
			if !controllerutil.ContainsFinalizer(col, nc.FinalizerCollectorInUse) {
				controllerutil.AddFinalizer(col, nc.FinalizerCollectorInUse)
				if err := m.client.Update(ctx, col); err != nil {
					return fmt.Errorf("adding finalizer to Collector %q: %w", col.Name, err)
				}
				m.logger.V(1).Info("added collector-in-use finalizer", "collector", col.Name)
			}
		} else {
			if controllerutil.ContainsFinalizer(col, nc.FinalizerCollectorInUse) {
				controllerutil.RemoveFinalizer(col, nc.FinalizerCollectorInUse)
				if err := m.client.Update(ctx, col); err != nil {
					return fmt.Errorf("removing finalizer from Collector %q: %w", col.Name, err)
				}
				m.logger.V(1).Info("removed collector-in-use finalizer", "collector", col.Name)
			}
		}
	}
	return nil
}

// collectDestinationSelectors gathers all label selectors that target Destinations.
func collectDestinationSelectors(fetched *resolver.FetchedResources) []*metav1.LabelSelector {
	var selectors []*metav1.LabelSelector
	for i := range fetched.Layer2Attachments {
		if fetched.Layer2Attachments[i].Spec.Destinations != nil {
			selectors = append(selectors, fetched.Layer2Attachments[i].Spec.Destinations)
		}
	}
	for i := range fetched.Inbounds {
		if fetched.Inbounds[i].Spec.Destinations != nil {
			selectors = append(selectors, fetched.Inbounds[i].Spec.Destinations)
		}
	}
	for i := range fetched.Outbounds {
		if fetched.Outbounds[i].Spec.Destinations != nil {
			selectors = append(selectors, fetched.Outbounds[i].Spec.Destinations)
		}
	}
	for i := range fetched.PodNetworks {
		if fetched.PodNetworks[i].Spec.Destinations != nil {
			selectors = append(selectors, fetched.PodNetworks[i].Spec.Destinations)
		}
	}
	return selectors
}

// isSelectedByAny returns true if the given resource labels match any of the selectors.
func isSelectedByAny(resourceLabels map[string]string, selectors []*metav1.LabelSelector) bool {
	for _, sel := range selectors {
		selector, err := metav1.LabelSelectorAsSelector(sel)
		if err != nil {
			continue
		}
		if selector.Matches(labels.Set(resourceLabels)) {
			return true
		}
	}
	return false
}
