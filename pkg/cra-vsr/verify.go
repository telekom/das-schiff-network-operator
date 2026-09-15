/*
Copyright 2025.

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

package cra

import (
	"context"
	"errors"
	"fmt"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

// verifyLayer2Applied reads back the VSR's Running datastore after a commit and
// confirms that every Layer2NetworkConfiguration entry from nodeCfg was actually
// instantiated (bridge + vxlan + vlan, correctly enslaved).
//
// This closes a gap where a NETCONF edit-data+commit can return "ok" (no
// transport/RPC error) while the VSR only partially applies the requested
// tree - e.g. due to an internal ordering/race on the device - silently
// leaving individual bridges/vxlans unconfigured. Without this check,
// ApplyConfiguration would report success and the reconciler would mark the
// NodeNetworkConfig as "provisioned" even though part of the config never
// landed on the device.
func (m *Manager) verifyLayer2Applied(ctx context.Context, nodeCfg *v1alpha1.NodeNetworkConfigSpec) error {
	if len(nodeCfg.Layer2s) == 0 {
		return nil
	}

	running := &VRouter{}
	if err := m.nc.GetUnmarshal(ctx, Running, "/config", running); err != nil {
		return fmt.Errorf("failed to read back running config for verification: %w", err)
	}

	ns := LookupNS(running, m.WorkNSName)
	if ns == nil {
		return fmt.Errorf("verification failed: working netns %q not found in running config", m.WorkNSName)
	}

	var errs []error
	for name := range nodeCfg.Layer2s {
		l2 := nodeCfg.Layer2s[name]
		if err := verifyLayer2Entry(ns, &l2); err != nil {
			errs = append(errs, fmt.Errorf("layer2 %q: %w", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("VSR did not fully apply %d/%d layer2 configuration(s): %w",
			len(errs), len(nodeCfg.Layer2s), errors.Join(errs...))
	}

	return nil
}

func verifyLayer2Entry(ns *Namespace, l2 *v1alpha1.Layer2) error {
	bridgeName := fmt.Sprintf("%s%d", layer2SVI, l2.VLAN)
	vxlanName := fmt.Sprintf("%s%d", vxlanPrefix, l2.VNI)
	vlanName := fmt.Sprintf("%s%d", vlanPrefix, l2.VLAN)

	// Bridge lives in the target VRF's interfaces if an IRB/VRF is configured,
	// otherwise directly under the working netns - mirrors Layer2.setup().
	bridgeIntfs := ns.Interfaces
	if l2.IRB != nil && l2.IRB.VRF != "" {
		vrf := LookupVRF(ns, l2.IRB.VRF)
		if vrf == nil {
			return fmt.Errorf("vlan %d (vni %d): vrf %q not found in running config", l2.VLAN, l2.VNI, l2.IRB.VRF)
		}
		bridgeIntfs = vrf.Interfaces
	}

	br := LookupBridge(bridgeIntfs, bridgeName)
	if br == nil {
		return fmt.Errorf("vlan %d (vni %d): bridge %q missing from running config", l2.VLAN, l2.VNI, bridgeName)
	}

	// VXLAN and VLAN interfaces are always created at the working netns level.
	if vx := LookupVXLAN(ns.Interfaces, vxlanName); vx == nil {
		return fmt.Errorf("vlan %d (vni %d): vxlan %q missing from running config", l2.VLAN, l2.VNI, vxlanName)
	}
	if vlan := LookupVLAN(ns.Interfaces, vlanName); vlan == nil {
		return fmt.Errorf("vlan %d (vni %d): vlan interface %q missing from running config", l2.VLAN, l2.VNI, vlanName)
	}

	if !br.hasSlave(vxlanName) {
		return fmt.Errorf("vlan %d (vni %d): vxlan %q exists but is not enslaved to bridge %q", l2.VLAN, l2.VNI, vxlanName, bridgeName)
	}
	if !br.hasSlave(vlanName) {
		return fmt.Errorf("vlan %d (vni %d): vlan %q exists but is not enslaved to bridge %q", l2.VLAN, l2.VNI, vlanName, bridgeName)
	}

	return nil
}
