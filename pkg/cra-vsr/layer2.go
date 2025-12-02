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
	"fmt"
	"os"
	"strconv"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/helpers/types"
)

type Layer2 struct {
	nodeCfg *v1alpha1.NodeNetworkConfigSpec
	ns      *Namespace
	mgr     *Manager
	infos   []InfoL2
}

type InfoL2 struct {
	vlanID int
	mtu    int
	vni    int
	vrf    string
	mac    string
	ips    []string
}

func NewLayer2(
	nodeCfg *v1alpha1.NodeNetworkConfigSpec,
	ns *Namespace,
	mgr *Manager,
) *Layer2 {
	return &Layer2{
		nodeCfg: nodeCfg,
		mgr:     mgr,
		ns:      ns,
		infos:   []InfoL2{},
	}
}

func (l *Layer2) setupInformations() {
	for _, l2 := range l.nodeCfg.Layer2s {
		info := InfoL2{
			vlanID: int(l2.VLAN),
			mtu:    int(l2.MTU),
			vni:    int(l2.VNI),
		}

		if l2.IRB != nil {
			info.ips = l2.IRB.IPAddresses
			info.mac = l2.IRB.MACAddress
			info.vrf = l2.IRB.VRF
		}

		l.infos = append(l.infos, info)
	}
}

func (l *Layer2) setupVXLAN(info *InfoL2, br *Bridge, intfs *Interfaces) {
	neighSuppress := os.Getenv("NWOP_NEIGH_SUPPRESSION") != "false"
	if len(info.ips) == 0 {
		neighSuppress = false
	}

	l.mgr.createVXLAN(
		fmt.Sprintf("%s%d", vxlanPrefix, info.vni),
		br, intfs, info.vni, info.mtu, false, neighSuppress)
}

func (l *Layer2) setupBridge(info *InfoL2, intfs *Interfaces) *Bridge {
	baseTimer, err := strconv.Atoi(os.Getenv("NWOP_NEIGH_BASE_REACHABLE_TIME"))
	if err != nil {
		baseTimer = 30000
	}

	br := l.mgr.createBridge(
		fmt.Sprintf("%s%d", layer2SVI, info.vlanID),
		info.mac, intfs, info.mtu, false, (len(info.ips) > 0))

	if br.NetworkStack == nil {
		br.NetworkStack = &NetworkStack{}
	}

	if br.NetworkStack.IPv6 == nil {
		br.NetworkStack.IPv6 = &NetworkStackV6{}
	}
	br.NetworkStack.IPv6.AcceptDAD = types.ToPtr(NeverDAD)

	br.NetworkStack.IPv4 = &NetworkStackV4{
		AcceptARP: types.ToPtr("always"),
	}
	br.NetworkStack.Neighbor = &NeighborNetworkStack{
		BaseReachableTimeV4: &baseTimer,
		BaseReachableTimeV6: &baseTimer,
	}

	ipv4 := &IPAddressList{}
	ipv6 := &IPAddressList{}
	for _, addr := range info.ips {
		l.mgr.createIPAddress(addr, ipv4, ipv6)
	}

	if len(ipv4.IPAddresses) > 0 {
		br.IPv4 = ipv4
	}
	if len(ipv6.IPAddresses) > 0 {
		br.IPv6 = ipv6
	}

	return br
}

func (l *Layer2) setup() error {
	l.setupInformations()

	for i := range l.infos {
		info := l.infos[i]
		intfs := l.ns.Interfaces
		if info.vrf != "" {
			vrf := lookupVRF(l.ns, info.vrf)
			if vrf == nil {
				return fmt.Errorf("vrf %s not found in netns %s",
					info.vrf, l.ns.Name)
			}
			intfs = vrf.Interfaces
		}

		if len(info.ips) > 0 && info.mac == "" {
			return fmt.Errorf("anycastGateways require anycastMAC to be set")
		}
		if len(info.ips) > 0 && info.vrf == "" {
			return fmt.Errorf("anycastGateways require VRF to be set")
		}

		br := l.setupBridge(&info, intfs)
		l.setupVXLAN(&info, br, l.ns.Interfaces)
		l.mgr.createVLAN(info.vlanID, info.mtu, br, l.ns.Interfaces)
	}

	return nil
}
