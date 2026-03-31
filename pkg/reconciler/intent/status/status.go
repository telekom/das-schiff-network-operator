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

	"github.com/go-logr/logr"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

// UpdateConditions sets Ready/Resolved conditions on intent CRDs.
func (u *Updater) UpdateConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData) error {
	if err := u.updateVRFConditions(ctx, fetched); err != nil {
		return fmt.Errorf("VRF conditions: %w", err)
	}
	if err := u.updateNetworkConditions(ctx, fetched); err != nil {
		return fmt.Errorf("Network conditions: %w", err)
	}
	if err := u.updateDestinationConditions(ctx, fetched, resolved); err != nil {
		return fmt.Errorf("Destination conditions: %w", err)
	}
	if err := u.updateInboundConditions(ctx, fetched, resolved); err != nil {
		return fmt.Errorf("Inbound conditions: %w", err)
	}
	if err := u.updateOutboundConditions(ctx, fetched, resolved); err != nil {
		return fmt.Errorf("Outbound conditions: %w", err)
	}
	if err := u.updateLayer2AttachmentConditions(ctx, fetched, resolved); err != nil {
		return fmt.Errorf("Layer2Attachment conditions: %w", err)
	}
	if err := u.updatePodNetworkConditions(ctx, fetched, resolved); err != nil {
		return fmt.Errorf("PodNetwork conditions: %w", err)
	}
	if err := u.updateCollectorConditions(ctx, fetched); err != nil {
		return fmt.Errorf("Collector conditions: %w", err)
	}
	if err := u.updateTrafficMirrorConditions(ctx, fetched, resolved); err != nil {
		return fmt.Errorf("TrafficMirror conditions: %w", err)
	}
	return nil
}

func (u *Updater) updateVRFConditions(ctx context.Context, fetched *resolver.FetchedResources) error {
	for i := range fetched.VRFs {
		vrf := &fetched.VRFs[i]
		setCondition(&vrf.Status.Conditions, nc.ConditionTypeResolved, metav1.ConditionTrue, "AllResolved", "VRF has no external references to resolve", vrf.Generation)
		setCondition(&vrf.Status.Conditions, nc.ConditionTypeReady, metav1.ConditionTrue, "Ready", "VRF is ready", vrf.Generation)
		vrf.Status.ObservedGeneration = vrf.Generation
		if err := u.client.Status().Update(ctx, vrf); err != nil {
			return fmt.Errorf("updating VRF %q status: %w", vrf.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateNetworkConditions(ctx context.Context, fetched *resolver.FetchedResources) error {
	for i := range fetched.Networks {
		net := &fetched.Networks[i]
		setCondition(&net.Status.Conditions, nc.ConditionTypeResolved, metav1.ConditionTrue, "AllResolved", "Network has no external references to resolve", net.Generation)
		setCondition(&net.Status.Conditions, nc.ConditionTypeReady, metav1.ConditionTrue, "Ready", "Network is ready", net.Generation)
		net.Status.ObservedGeneration = net.Generation
		if err := u.client.Status().Update(ctx, net); err != nil {
			return fmt.Errorf("updating Network %q status: %w", net.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateDestinationConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData) error {
	for i := range fetched.Destinations {
		dest := &fetched.Destinations[i]
		resolvedStatus := metav1.ConditionTrue
		resolvedReason := "AllResolved"
		resolvedMsg := "All references resolved"

		if dest.Spec.VRFRef != nil {
			if _, ok := resolved.VRFs[*dest.Spec.VRFRef]; !ok {
				resolvedStatus = metav1.ConditionFalse
				resolvedReason = "VRFNotFound"
				resolvedMsg = fmt.Sprintf("referenced VRF %q not found", *dest.Spec.VRFRef)
			}
		}

		setCondition(&dest.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, dest.Generation)
		readyStatus := resolvedStatus
		readyMsg := "Destination is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		setCondition(&dest.Status.Conditions, nc.ConditionTypeReady, readyStatus, resolvedReason, readyMsg, dest.Generation)
		dest.Status.ObservedGeneration = dest.Generation
		if err := u.client.Status().Update(ctx, dest); err != nil {
			return fmt.Errorf("updating Destination %q status: %w", dest.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateInboundConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData) error {
	for i := range fetched.Inbounds {
		inb := &fetched.Inbounds[i]
		resolvedStatus, resolvedReason, resolvedMsg := checkNetworkRef(inb.Spec.NetworkRef, resolved)

		setCondition(&inb.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, inb.Generation)
		readyStatus := resolvedStatus
		readyMsg := "Inbound is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		setCondition(&inb.Status.Conditions, nc.ConditionTypeReady, readyStatus, resolvedReason, readyMsg, inb.Generation)
		inb.Status.ObservedGeneration = inb.Generation
		if err := u.client.Status().Update(ctx, inb); err != nil {
			return fmt.Errorf("updating Inbound %q status: %w", inb.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateOutboundConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData) error {
	for i := range fetched.Outbounds {
		outb := &fetched.Outbounds[i]
		resolvedStatus, resolvedReason, resolvedMsg := checkNetworkRef(outb.Spec.NetworkRef, resolved)

		setCondition(&outb.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, outb.Generation)
		readyStatus := resolvedStatus
		readyMsg := "Outbound is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		setCondition(&outb.Status.Conditions, nc.ConditionTypeReady, readyStatus, resolvedReason, readyMsg, outb.Generation)
		outb.Status.ObservedGeneration = outb.Generation
		if err := u.client.Status().Update(ctx, outb); err != nil {
			return fmt.Errorf("updating Outbound %q status: %w", outb.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateLayer2AttachmentConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData) error {
	for i := range fetched.Layer2Attachments {
		l2a := &fetched.Layer2Attachments[i]
		resolvedStatus, resolvedReason, resolvedMsg := checkNetworkRef(l2a.Spec.NetworkRef, resolved)

		setCondition(&l2a.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, l2a.Generation)
		readyStatus := resolvedStatus
		readyMsg := "Layer2Attachment is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		setCondition(&l2a.Status.Conditions, nc.ConditionTypeReady, readyStatus, resolvedReason, readyMsg, l2a.Generation)
		l2a.Status.ObservedGeneration = l2a.Generation
		if err := u.client.Status().Update(ctx, l2a); err != nil {
			return fmt.Errorf("updating Layer2Attachment %q status: %w", l2a.Name, err)
		}
	}
	return nil
}

func (u *Updater) updatePodNetworkConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData) error {
	for i := range fetched.PodNetworks {
		pn := &fetched.PodNetworks[i]
		resolvedStatus, resolvedReason, resolvedMsg := checkNetworkRef(pn.Spec.NetworkRef, resolved)

		setCondition(&pn.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, pn.Generation)
		readyStatus := resolvedStatus
		readyMsg := "PodNetwork is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		setCondition(&pn.Status.Conditions, nc.ConditionTypeReady, readyStatus, resolvedReason, readyMsg, pn.Generation)
		pn.Status.ObservedGeneration = pn.Generation
		if err := u.client.Status().Update(ctx, pn); err != nil {
			return fmt.Errorf("updating PodNetwork %q status: %w", pn.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateCollectorConditions(ctx context.Context, fetched *resolver.FetchedResources) error {
	for i := range fetched.Collectors {
		col := &fetched.Collectors[i]
		setCondition(&col.Status.Conditions, nc.ConditionTypeResolved, metav1.ConditionTrue, "AllResolved", "Collector references resolved", col.Generation)
		setCondition(&col.Status.Conditions, nc.ConditionTypeReady, metav1.ConditionTrue, "Ready", "Collector is ready", col.Generation)
		col.Status.ObservedGeneration = col.Generation
		if err := u.client.Status().Update(ctx, col); err != nil {
			return fmt.Errorf("updating Collector %q status: %w", col.Name, err)
		}
	}
	return nil
}

func (u *Updater) updateTrafficMirrorConditions(ctx context.Context, fetched *resolver.FetchedResources, resolved *resolver.ResolvedData) error {
	collectorNames := make(map[string]bool, len(resolved.Collectors))
	for i := range resolved.Collectors {
		collectorNames[resolved.Collectors[i].Name] = true
	}

	for i := range fetched.TrafficMirrors {
		tm := &fetched.TrafficMirrors[i]
		resolvedStatus := metav1.ConditionTrue
		resolvedReason := "AllResolved"
		resolvedMsg := "All references resolved"

		if !collectorNames[tm.Spec.Collector] {
			resolvedStatus = metav1.ConditionFalse
			resolvedReason = "CollectorNotFound"
			resolvedMsg = fmt.Sprintf("referenced Collector %q not found", tm.Spec.Collector)
		}

		setCondition(&tm.Status.Conditions, nc.ConditionTypeResolved, resolvedStatus, resolvedReason, resolvedMsg, tm.Generation)
		readyStatus := resolvedStatus
		readyMsg := "TrafficMirror is ready"
		if resolvedStatus != metav1.ConditionTrue {
			readyMsg = resolvedMsg
		}
		setCondition(&tm.Status.Conditions, nc.ConditionTypeReady, readyStatus, resolvedReason, readyMsg, tm.Generation)
		tm.Status.ObservedGeneration = tm.Generation
		if err := u.client.Status().Update(ctx, tm); err != nil {
			return fmt.Errorf("updating TrafficMirror %q status: %w", tm.Name, err)
		}
	}
	return nil
}

// checkNetworkRef checks if a networkRef resolves to an existing Network.
func checkNetworkRef(networkRef string, resolved *resolver.ResolvedData) (metav1.ConditionStatus, string, string) {
	if _, ok := resolved.Networks[networkRef]; !ok {
		return metav1.ConditionFalse, "NetworkNotFound", fmt.Sprintf("referenced Network %q not found", networkRef)
	}
	return metav1.ConditionTrue, "AllResolved", "All references resolved"
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
