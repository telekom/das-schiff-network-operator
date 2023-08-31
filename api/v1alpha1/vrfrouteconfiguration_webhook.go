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

package v1alpha1

import (
	"fmt"
	"net"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const geLen = 8

// log is for logging in this package.
var vrfrouteconfigurationlog = logf.Log.WithName("vrfrouteconfiguration-resource")

func (r *VRFRouteConfiguration) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewWebhookManagedBy(mgr).For(r).Complete(); err != nil {
		return fmt.Errorf("error building webhook: %w", err)
	}
	return nil
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-network-schiff-telekom-de-v1alpha1-vrfrouteconfiguration,mutating=false,failurePolicy=fail,sideEffects=None,groups=network.schiff.telekom.de,resources=vrfrouteconfigurations,verbs=create;update,versions=v1alpha1,name=vvrfrouteconfiguration.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &VRFRouteConfiguration{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type.
func (r *VRFRouteConfiguration) ValidateCreate() error {
	vrfrouteconfigurationlog.Info("validate create", "name", r.Name)

	err := r.validateItems()
	if err != nil {
		return err
	}

	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type.
func (r *VRFRouteConfiguration) ValidateUpdate(_ runtime.Object) error {
	vrfrouteconfigurationlog.Info("validate update", "name", r.Name)

	err := r.validateItems()
	if err != nil {
		return err
	}

	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type.
func (r *VRFRouteConfiguration) ValidateDelete() error {
	vrfrouteconfigurationlog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil
}

func (r *VRFRouteConfiguration) validateItems() error {
	err := validateItemList(r.Spec.Export)
	if err != nil {
		return err
	}
	err = validateItemList(r.Spec.Import)
	if err != nil {
		return err
	}
	for _, item := range r.Spec.Aggregate {
		_, _, err := net.ParseCIDR(item)
		if err != nil {
			return fmt.Errorf("error parsing CIDR %s: %w", item, err)
		}
	}
	return nil
}

func findDuplicates(items []VrfRouteConfigurationPrefixItem) []string {
	counter := map[string]int{}
	for _, item := range items {
		counter[item.CIDR]++
	}

	duplicates := []string{}
	for key, value := range counter {
		if value > 1 {
			duplicates = append(duplicates, key)
		}
	}

	return duplicates
}

func validateItemList(items []VrfRouteConfigurationPrefixItem) error {
	duplicates := findDuplicates(items)
	if len(duplicates) > 0 {
		return fmt.Errorf("there are duplicated items in the VrfRouteConfiguration: %v", duplicates)
	}

	usedPriorities := map[int]struct{}{}
	for i, item := range items {
		seq := i + 1
		if item.Seq > 0 {
			seq = item.Seq
		}
		if _, inuse := usedPriorities[seq]; inuse {
			return fmt.Errorf("seq %d of list item index %d is already in use", seq, i)
		}

		err := item.validateItem()
		if err != nil {
			return fmt.Errorf("error validating configuration item: %w", err)
		}

		usedPriorities[seq] = struct{}{}
	}
	return nil
}

func (item VrfRouteConfigurationPrefixItem) validateItem() error {
	ip, network, err := net.ParseCIDR(item.CIDR)
	if err != nil {
		return fmt.Errorf("error parsing CIDR %s: %w", item.CIDR, err)
	}

	if !network.IP.Equal(ip) {
		return fmt.Errorf("CIDR %s is invalid, CIDR for this network should be %s", item.CIDR, network.String())
	}

	if ip.To4() != nil {
		ip = ip.To4()
	}
	if item.GE != nil {
		ge := *item.GE
		if ge < 0 || ge > len(ip)*geLen {
			return fmt.Errorf("ge for IPv4 addresses must be in range of 0-%d", len(ip)*geLen)
		}
	}
	if item.LE != nil {
		le := *item.LE
		if le < 0 || le > len(ip)*geLen {
			return fmt.Errorf("le for IPv4 addresses must be in range of 0-%d", len(ip)*geLen)
		}
	}
	return nil
}
