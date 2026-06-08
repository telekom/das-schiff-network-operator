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

	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
)

// AnnouncementBuilder is kept for interface compliance. Community tagging and
// aggregate control are now converged into the usage builders (L2A, Inbound,
// Outbound, PodNetwork) via findMatchingAP + cidrFilterItems/addressFilterItems.
type AnnouncementBuilder struct{}

// NewAnnouncementBuilder creates a new AnnouncementBuilder.
func NewAnnouncementBuilder() *AnnouncementBuilder {
	return &AnnouncementBuilder{}
}

// Name returns the builder name.
func (*AnnouncementBuilder) Name() string {
	return "announcement"
}

// Build is a no-op. AnnouncementPolicy logic is converged into usage builders.
func (*AnnouncementBuilder) Build(_ context.Context, _ *resolver.ResolvedData) (map[string]*NodeContribution, error) {
	return nil, nil
}
