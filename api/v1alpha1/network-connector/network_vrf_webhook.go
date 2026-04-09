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
	"regexp"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	networklog      = logf.Log.WithName("network-resource")
	vrflog          = logf.Log.WithName("vrf-resource")
	routeTargetExpr = regexp.MustCompile(`^\d+:\d+$`)
)

// ---------------------------------------------------------------------------
// Network webhook.
// ---------------------------------------------------------------------------

func (r *Network) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, r).WithValidator(r).Complete(); err != nil {
		return fmt.Errorf("error building Network webhook: %w", err)
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-network-connector-sylvaproject-org-v1alpha1-network,mutating=false,failurePolicy=fail,sideEffects=None,groups=network-connector.sylvaproject.org,resources=networks,verbs=create;update,versions=v1alpha1,name=vnetwork.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*Network] = &Network{}

func (*Network) ValidateCreate(_ context.Context, r *Network) (admission.Warnings, error) {
	networklog.Info("validate create", "name", r.Name)
	return nil, r.validateNetwork()
}

func (*Network) ValidateUpdate(_ context.Context, _, r *Network) (admission.Warnings, error) {
	networklog.Info("validate update", "name", r.Name)
	return nil, r.validateNetwork()
}

func (*Network) ValidateDelete(_ context.Context, r *Network) (admission.Warnings, error) {
	networklog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *Network) validateNetwork() error {
	if r.Spec.IPv4 == nil && r.Spec.IPv6 == nil && r.Spec.VLAN == nil {
		return fmt.Errorf("at least one of ipv4, ipv6 or vlan must be set")
	}
	if r.Spec.IPv4 != nil {
		ip, _, err := net.ParseCIDR(r.Spec.IPv4.CIDR)
		if err != nil {
			return fmt.Errorf("invalid ipv4 CIDR %q: %w", r.Spec.IPv4.CIDR, err)
		}
		if ip.To4() == nil {
			return fmt.Errorf("invalid ipv4 CIDR %q: must be an IPv4 CIDR", r.Spec.IPv4.CIDR)
		}
	}
	if r.Spec.IPv6 != nil {
		ip, _, err := net.ParseCIDR(r.Spec.IPv6.CIDR)
		if err != nil {
			return fmt.Errorf("invalid ipv6 CIDR %q: %w", r.Spec.IPv6.CIDR, err)
		}
		if ip.To4() != nil {
			return fmt.Errorf("invalid ipv6 CIDR %q: must be an IPv6 CIDR", r.Spec.IPv6.CIDR)
		}
	}
	if r.Spec.VNI != nil && *r.Spec.VNI <= 0 {
		return fmt.Errorf("vni must be > 0, got %d", *r.Spec.VNI)
	}
	if r.Spec.VLAN != nil && (*r.Spec.VLAN < 1 || *r.Spec.VLAN > 4094) {
		return fmt.Errorf("vlan must be in range 1-4094, got %d", *r.Spec.VLAN)
	}
	return nil
}

// ---------------------------------------------------------------------------
// VRF webhook.
// ---------------------------------------------------------------------------

func (r *VRF) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, r).WithValidator(r).Complete(); err != nil {
		return fmt.Errorf("error building VRF webhook: %w", err)
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-network-connector-sylvaproject-org-v1alpha1-vrf,mutating=false,failurePolicy=fail,sideEffects=None,groups=network-connector.sylvaproject.org,resources=vrfs,verbs=create;update,versions=v1alpha1,name=vvrf.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*VRF] = &VRF{}

func (*VRF) ValidateCreate(_ context.Context, r *VRF) (admission.Warnings, error) {
	vrflog.Info("validate create", "name", r.Name)
	return nil, r.validateVRF()
}

func (*VRF) ValidateUpdate(_ context.Context, _, r *VRF) (admission.Warnings, error) {
	vrflog.Info("validate update", "name", r.Name)
	return nil, r.validateVRF()
}

func (*VRF) ValidateDelete(_ context.Context, r *VRF) (admission.Warnings, error) {
	vrflog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *VRF) validateVRF() error {
	if r.Spec.VRF == "" {
		return fmt.Errorf("spec.vrf must not be empty")
	}
	if r.Spec.VNI != nil && *r.Spec.VNI <= 0 {
		return fmt.Errorf("vni must be > 0, got %d", *r.Spec.VNI)
	}
	if r.Spec.RouteTarget != nil && !routeTargetExpr.MatchString(*r.Spec.RouteTarget) {
		return fmt.Errorf("routeTarget %q must match ASN:value format (e.g. 65000:100)", *r.Spec.RouteTarget)
	}
	return nil
}
