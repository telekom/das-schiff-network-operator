/*
Copyright 2022.

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

package networkconnector

import (
	"context"
	"fmt"
	"net"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	bgppeeringlog         = logf.Log.WithName("bgppeering-resource")
	podnetworklog         = logf.Log.WithName("podnetwork-resource")
	collectorlog          = logf.Log.WithName("collector-resource")
	trafficmirrorlog      = logf.Log.WithName("trafficmirror-resource")
	announcementpolicylog = logf.Log.WithName("announcementpolicy-resource")
	destinationlog        = logf.Log.WithName("destination-resource")
	interfaceconfiglog    = logf.Log.WithName("interfaceconfig-resource")
)

// ===========================================================================
// BGPPeering webhook.
// ===========================================================================

func (r *BGPPeering) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, r).WithValidator(r).Complete(); err != nil {
		return fmt.Errorf("error building BGPPeering webhook: %w", err)
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-network-connector-sylvaproject-org-v1alpha1-bgppeering,mutating=false,failurePolicy=fail,sideEffects=None,groups=network-connector.sylvaproject.org,resources=bgppeerings,verbs=create;update,versions=v1alpha1,name=vbgppeering.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*BGPPeering] = &BGPPeering{}

func (*BGPPeering) ValidateCreate(_ context.Context, r *BGPPeering) (admission.Warnings, error) {
	bgppeeringlog.Info("validate create", "name", r.Name)
	return nil, r.validateBGPPeering()
}

func (*BGPPeering) ValidateUpdate(_ context.Context, old, r *BGPPeering) (admission.Warnings, error) {
	bgppeeringlog.Info("validate update", "name", r.Name)
	if err := r.validateBGPPeering(); err != nil {
		return nil, err
	}
	if old.Spec.Mode != r.Spec.Mode {
		return nil, fmt.Errorf("spec.mode is immutable")
	}
	return nil, nil
}

func (*BGPPeering) ValidateDelete(_ context.Context, r *BGPPeering) (admission.Warnings, error) {
	bgppeeringlog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *BGPPeering) validateBGPPeering() error {
	if r.Spec.Mode != BGPPeeringModeListenRange && r.Spec.Mode != BGPPeeringModeLoopbackPeer {
		return fmt.Errorf("spec.mode must be %q or %q, got %q", BGPPeeringModeListenRange, BGPPeeringModeLoopbackPeer, r.Spec.Mode)
	}
	if len(r.Spec.Ref.InboundRefs) == 0 {
		return fmt.Errorf("spec.ref.inboundRefs must not be empty")
	}
	if r.Spec.Mode == BGPPeeringModeListenRange {
		if r.Spec.Ref.AttachmentRef == nil || *r.Spec.Ref.AttachmentRef == "" {
			return fmt.Errorf("spec.ref.attachmentRef is required for listenRange mode")
		}
	}
	if r.Spec.Mode == BGPPeeringModeLoopbackPeer {
		if r.Spec.Ref.AttachmentRef != nil {
			return fmt.Errorf("spec.ref.attachmentRef must not be set for loopbackPeer mode")
		}
	}
	return nil
}

// ===========================================================================
// PodNetwork webhook
// ===========================================================================

func (r *PodNetwork) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, r).WithValidator(r).Complete(); err != nil {
		return fmt.Errorf("error building PodNetwork webhook: %w", err)
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-network-connector-sylvaproject-org-v1alpha1-podnetwork,mutating=false,failurePolicy=fail,sideEffects=None,groups=network-connector.sylvaproject.org,resources=podnetworks,verbs=create;update,versions=v1alpha1,name=vpodnetwork.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*PodNetwork] = &PodNetwork{}

func (*PodNetwork) ValidateCreate(_ context.Context, r *PodNetwork) (admission.Warnings, error) {
	podnetworklog.Info("validate create", "name", r.Name)
	return nil, r.validatePodNetwork()
}

func (*PodNetwork) ValidateUpdate(_ context.Context, old, r *PodNetwork) (admission.Warnings, error) {
	podnetworklog.Info("validate update", "name", r.Name)
	if err := r.validatePodNetwork(); err != nil {
		return nil, err
	}
	if old.Spec.NetworkRef != r.Spec.NetworkRef {
		return nil, fmt.Errorf("spec.networkRef is immutable")
	}
	return nil, nil
}

func (*PodNetwork) ValidateDelete(_ context.Context, r *PodNetwork) (admission.Warnings, error) {
	podnetworklog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *PodNetwork) validatePodNetwork() error {
	if r.Spec.NetworkRef == "" {
		return fmt.Errorf("spec.networkRef must not be empty")
	}
	return nil
}

// ===========================================================================
// Collector webhook
// ===========================================================================

func (r *Collector) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, r).WithValidator(r).Complete(); err != nil {
		return fmt.Errorf("error building Collector webhook: %w", err)
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-network-connector-sylvaproject-org-v1alpha1-collector,mutating=false,failurePolicy=fail,sideEffects=None,groups=network-connector.sylvaproject.org,resources=collectors,verbs=create;update,versions=v1alpha1,name=vcollector.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*Collector] = &Collector{}

func (*Collector) ValidateCreate(_ context.Context, r *Collector) (admission.Warnings, error) {
	collectorlog.Info("validate create", "name", r.Name)
	return nil, r.validateCollector()
}

func (*Collector) ValidateUpdate(_ context.Context, old, r *Collector) (admission.Warnings, error) {
	collectorlog.Info("validate update", "name", r.Name)
	if err := r.validateCollector(); err != nil {
		return nil, err
	}
	if old.Spec.Protocol != r.Spec.Protocol {
		return nil, fmt.Errorf("spec.protocol is immutable")
	}
	return nil, nil
}

func (*Collector) ValidateDelete(_ context.Context, r *Collector) (admission.Warnings, error) {
	collectorlog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *Collector) validateCollector() error {
	if r.Spec.Address == "" {
		return fmt.Errorf("spec.address must not be empty")
	}
	if net.ParseIP(r.Spec.Address) == nil {
		return fmt.Errorf("spec.address must be a valid IP address, got %q", r.Spec.Address)
	}
	if r.Spec.MirrorVRF.Name == "" {
		return fmt.Errorf("spec.mirrorVRF.name must not be empty")
	}
	if r.Spec.MirrorVRF.Loopback.Name == "" {
		return fmt.Errorf("spec.mirrorVRF.loopback.name must not be empty")
	}
	return nil
}

// ===========================================================================
// TrafficMirror webhook
// ===========================================================================

func (r *TrafficMirror) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, r).WithValidator(r).Complete(); err != nil {
		return fmt.Errorf("error building TrafficMirror webhook: %w", err)
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-network-connector-sylvaproject-org-v1alpha1-trafficmirror,mutating=false,failurePolicy=fail,sideEffects=None,groups=network-connector.sylvaproject.org,resources=trafficmirrors,verbs=create;update,versions=v1alpha1,name=vtrafficmirror.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*TrafficMirror] = &TrafficMirror{}

func (*TrafficMirror) ValidateCreate(_ context.Context, r *TrafficMirror) (admission.Warnings, error) {
	trafficmirrorlog.Info("validate create", "name", r.Name)
	return nil, r.validateTrafficMirror()
}

func (*TrafficMirror) ValidateUpdate(_ context.Context, _, r *TrafficMirror) (admission.Warnings, error) {
	trafficmirrorlog.Info("validate update", "name", r.Name)
	return nil, r.validateTrafficMirror()
}

func (*TrafficMirror) ValidateDelete(_ context.Context, r *TrafficMirror) (admission.Warnings, error) {
	trafficmirrorlog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *TrafficMirror) validateTrafficMirror() error {
	if r.Spec.Source.Name == "" {
		return fmt.Errorf("spec.source.name must not be empty")
	}
	if r.Spec.Collector == "" {
		return fmt.Errorf("spec.collector must not be empty")
	}
	if r.Spec.TrafficMatch != nil {
		if r.Spec.TrafficMatch.SrcPrefix != nil {
			if _, _, err := net.ParseCIDR(*r.Spec.TrafficMatch.SrcPrefix); err != nil {
				return fmt.Errorf("spec.trafficMatch.srcPrefix is not a valid CIDR: %w", err)
			}
		}
		if r.Spec.TrafficMatch.DstPrefix != nil {
			if _, _, err := net.ParseCIDR(*r.Spec.TrafficMatch.DstPrefix); err != nil {
				return fmt.Errorf("spec.trafficMatch.dstPrefix is not a valid CIDR: %w", err)
			}
		}
	}
	return nil
}

// ===========================================================================
// AnnouncementPolicy webhook
// ===========================================================================

func (r *AnnouncementPolicy) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, r).WithValidator(r).Complete(); err != nil {
		return fmt.Errorf("error building AnnouncementPolicy webhook: %w", err)
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-network-connector-sylvaproject-org-v1alpha1-announcementpolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=network-connector.sylvaproject.org,resources=announcementpolicies,verbs=create;update,versions=v1alpha1,name=vannouncementpolicy.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*AnnouncementPolicy] = &AnnouncementPolicy{}

func (*AnnouncementPolicy) ValidateCreate(_ context.Context, r *AnnouncementPolicy) (admission.Warnings, error) {
	announcementpolicylog.Info("validate create", "name", r.Name)
	return nil, r.validateAnnouncementPolicy()
}

func (*AnnouncementPolicy) ValidateUpdate(_ context.Context, _, r *AnnouncementPolicy) (admission.Warnings, error) {
	announcementpolicylog.Info("validate update", "name", r.Name)
	return nil, r.validateAnnouncementPolicy()
}

func (*AnnouncementPolicy) ValidateDelete(_ context.Context, r *AnnouncementPolicy) (admission.Warnings, error) {
	announcementpolicylog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *AnnouncementPolicy) validateAnnouncementPolicy() error {
	if r.Spec.VRFRef == "" {
		return fmt.Errorf("spec.vrfRef must not be empty")
	}
	if r.Spec.Aggregate != nil {
		if r.Spec.Aggregate.PrefixLengthV4 != nil {
			v := *r.Spec.Aggregate.PrefixLengthV4
			if v < 1 || v > 32 {
				return fmt.Errorf("spec.aggregate.prefixLengthV4 must be between 1 and 32, got %d", v)
			}
		}
		if r.Spec.Aggregate.PrefixLengthV6 != nil {
			v := *r.Spec.Aggregate.PrefixLengthV6
			if v < 1 || v > 128 {
				return fmt.Errorf("spec.aggregate.prefixLengthV6 must be between 1 and 128, got %d", v)
			}
		}
	}
	return nil
}

// ===========================================================================
// Destination webhook
// ===========================================================================

func (r *Destination) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, r).WithValidator(r).Complete(); err != nil {
		return fmt.Errorf("error building Destination webhook: %w", err)
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-network-connector-sylvaproject-org-v1alpha1-destination,mutating=false,failurePolicy=fail,sideEffects=None,groups=network-connector.sylvaproject.org,resources=destinations,verbs=create;update,versions=v1alpha1,name=vdestination.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*Destination] = &Destination{}

func (*Destination) ValidateCreate(_ context.Context, r *Destination) (admission.Warnings, error) {
	destinationlog.Info("validate create", "name", r.Name)
	return nil, r.validateDestination()
}

func (*Destination) ValidateUpdate(_ context.Context, _, r *Destination) (admission.Warnings, error) {
	destinationlog.Info("validate update", "name", r.Name)
	return nil, r.validateDestination()
}

func (*Destination) ValidateDelete(_ context.Context, r *Destination) (admission.Warnings, error) {
	destinationlog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *Destination) validateDestination() error {
	for i, p := range r.Spec.Prefixes {
		if _, _, err := net.ParseCIDR(p); err != nil {
			return fmt.Errorf("spec.prefixes[%d] is not a valid CIDR %q: %w", i, p, err)
		}
	}
	if r.Spec.NextHop != nil {
		if r.Spec.NextHop.IPv4 != nil {
			if net.ParseIP(*r.Spec.NextHop.IPv4) == nil {
				return fmt.Errorf("spec.nextHop.ipv4 must be a valid IP address, got %q", *r.Spec.NextHop.IPv4)
			}
		}
		if r.Spec.NextHop.IPv6 != nil {
			if net.ParseIP(*r.Spec.NextHop.IPv6) == nil {
				return fmt.Errorf("spec.nextHop.ipv6 must be a valid IP address, got %q", *r.Spec.NextHop.IPv6)
			}
		}
	}
	return nil
}

// ===========================================================================
// InterfaceConfig webhook
// ===========================================================================

func (r *InterfaceConfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, r).WithValidator(r).Complete(); err != nil {
		return fmt.Errorf("error building InterfaceConfig webhook: %w", err)
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-network-connector-sylvaproject-org-v1alpha1-interfaceconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=network-connector.sylvaproject.org,resources=interfaceconfigs,verbs=create;update,versions=v1alpha1,name=vinterfaceconfig.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*InterfaceConfig] = &InterfaceConfig{}

func (*InterfaceConfig) ValidateCreate(_ context.Context, r *InterfaceConfig) (admission.Warnings, error) {
	interfaceconfiglog.Info("validate create", "name", r.Name)
	return nil, r.validateInterfaceConfig()
}

func (*InterfaceConfig) ValidateUpdate(_ context.Context, _, r *InterfaceConfig) (admission.Warnings, error) {
	interfaceconfiglog.Info("validate update", "name", r.Name)
	return nil, r.validateInterfaceConfig()
}

func (*InterfaceConfig) ValidateDelete(_ context.Context, r *InterfaceConfig) (admission.Warnings, error) {
	interfaceconfiglog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *InterfaceConfig) validateInterfaceConfig() error {
	if len(r.Spec.Ethernets) == 0 && len(r.Spec.Bonds) == 0 {
		return fmt.Errorf("at least one of spec.ethernets or spec.bonds must be provided")
	}
	for name, eth := range r.Spec.Ethernets {
		if eth.Mtu != nil && (*eth.Mtu < 1000 || *eth.Mtu > 9000) {
			return fmt.Errorf("spec.ethernets[%s].mtu must be in range [1000, 9000], got %d", name, *eth.Mtu)
		}
	}
	for name, bond := range r.Spec.Bonds {
		for i, member := range bond.Interfaces {
			if member == "" {
				return fmt.Errorf("spec.bonds[%s].interfaces[%d] must not be empty", name, i)
			}
		}
		if bond.Mtu != nil && (*bond.Mtu < 1000 || *bond.Mtu > 9000) {
			return fmt.Errorf("spec.bonds[%s].mtu must be in range [1000, 9000], got %d", name, *bond.Mtu)
		}
	}
	return nil
}
