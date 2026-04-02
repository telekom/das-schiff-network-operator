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
	l2alog      = logf.Log.WithName("layer2attachment-resource")
	inboundlog  = logf.Log.WithName("inbound-resource")
	outboundlog = logf.Log.WithName("outbound-resource")
)

// ---------------------------------------------------------------------------
// Layer2Attachment webhook
// ---------------------------------------------------------------------------

func (r *Layer2Attachment) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, r).WithValidator(r).Complete(); err != nil {
		return fmt.Errorf("error building Layer2Attachment webhook: %w", err)
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-network-connector-sylvaproject-org-v1alpha1-layer2attachment,mutating=false,failurePolicy=fail,sideEffects=None,groups=network-connector.sylvaproject.org,resources=layer2attachments,verbs=create;update,versions=v1alpha1,name=vlayer2attachment.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*Layer2Attachment] = &Layer2Attachment{}

func (*Layer2Attachment) ValidateCreate(_ context.Context, r *Layer2Attachment) (admission.Warnings, error) {
	l2alog.Info("validate create", "name", r.Name)
	return nil, r.validateLayer2Attachment()
}

func (*Layer2Attachment) ValidateUpdate(_ context.Context, old, r *Layer2Attachment) (admission.Warnings, error) {
	l2alog.Info("validate update", "name", r.Name)
	if err := r.validateLayer2Attachment(); err != nil {
		return nil, err
	}
	if old.Spec.InterfaceName != nil && (r.Spec.InterfaceName == nil || *r.Spec.InterfaceName != *old.Spec.InterfaceName) {
		return nil, fmt.Errorf("spec.interfaceName is immutable once set")
	}
	return nil, nil
}

func (*Layer2Attachment) ValidateDelete(_ context.Context, r *Layer2Attachment) (admission.Warnings, error) {
	l2alog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *Layer2Attachment) validateLayer2Attachment() error {
	if r.Spec.NetworkRef == "" {
		return fmt.Errorf("spec.networkRef must not be empty")
	}
	if r.Spec.MTU != nil && (*r.Spec.MTU < 1000 || *r.Spec.MTU > 9000) {
		return fmt.Errorf("spec.mtu must be in range [1000, 9000], got %d", *r.Spec.MTU)
	}
	if r.Spec.InterfaceName != nil && len(*r.Spec.InterfaceName) > 15 {
		return fmt.Errorf("spec.interfaceName must not exceed 15 characters, got %d", len(*r.Spec.InterfaceName))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Inbound webhook
// ---------------------------------------------------------------------------

func (r *Inbound) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, r).WithValidator(r).Complete(); err != nil {
		return fmt.Errorf("error building Inbound webhook: %w", err)
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-network-connector-sylvaproject-org-v1alpha1-inbound,mutating=false,failurePolicy=fail,sideEffects=None,groups=network-connector.sylvaproject.org,resources=inbounds,verbs=create;update,versions=v1alpha1,name=vinbound.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*Inbound] = &Inbound{}

func (*Inbound) ValidateCreate(_ context.Context, r *Inbound) (admission.Warnings, error) {
	inboundlog.Info("validate create", "name", r.Name)
	return nil, r.validateInbound()
}

func (*Inbound) ValidateUpdate(_ context.Context, _, r *Inbound) (admission.Warnings, error) {
	inboundlog.Info("validate update", "name", r.Name)
	return nil, r.validateInbound()
}

func (*Inbound) ValidateDelete(_ context.Context, r *Inbound) (admission.Warnings, error) {
	inboundlog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *Inbound) validateInbound() error {
	if r.Spec.NetworkRef == "" {
		return fmt.Errorf("spec.networkRef must not be empty")
	}
	if r.Spec.Count != nil && r.Spec.Addresses != nil {
		return fmt.Errorf("spec.count and spec.addresses are mutually exclusive")
	}
	if r.Spec.Addresses != nil {
		if err := validateAddressAllocation(r.Spec.Addresses); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Outbound webhook
// ---------------------------------------------------------------------------

func (r *Outbound) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, r).WithValidator(r).Complete(); err != nil {
		return fmt.Errorf("error building Outbound webhook: %w", err)
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-network-connector-sylvaproject-org-v1alpha1-outbound,mutating=false,failurePolicy=fail,sideEffects=None,groups=network-connector.sylvaproject.org,resources=outbounds,verbs=create;update,versions=v1alpha1,name=voutbound.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*Outbound] = &Outbound{}

func (*Outbound) ValidateCreate(_ context.Context, r *Outbound) (admission.Warnings, error) {
	outboundlog.Info("validate create", "name", r.Name)
	return nil, r.validateOutbound()
}

func (*Outbound) ValidateUpdate(_ context.Context, _, r *Outbound) (admission.Warnings, error) {
	outboundlog.Info("validate update", "name", r.Name)
	return nil, r.validateOutbound()
}

func (*Outbound) ValidateDelete(_ context.Context, r *Outbound) (admission.Warnings, error) {
	outboundlog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *Outbound) validateOutbound() error {
	if r.Spec.NetworkRef == "" {
		return fmt.Errorf("spec.networkRef must not be empty")
	}
	if r.Spec.Count != nil && r.Spec.Addresses != nil {
		return fmt.Errorf("spec.count and spec.addresses are mutually exclusive")
	}
	if r.Spec.Addresses != nil {
		if err := validateAddressAllocation(r.Spec.Addresses); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func validateAddressAllocation(a *AddressAllocation) error {
	for _, v4 := range a.IPv4 {
		if _, _, err := net.ParseCIDR(v4); err != nil {
			return fmt.Errorf("invalid IPv4 CIDR %q: %w", v4, err)
		}
	}
	for _, v6 := range a.IPv6 {
		if _, _, err := net.ParseCIDR(v6); err != nil {
			return fmt.Errorf("invalid IPv6 CIDR %q: %w", v6, err)
		}
	}
	return nil
}
