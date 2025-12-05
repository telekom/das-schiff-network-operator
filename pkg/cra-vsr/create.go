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
	"net"

	"github.com/telekom/das-schiff-network-operator/pkg/helpers/types"
)

const (
	hwAddrByteSize = 6
)

var macPrefix = []byte("\x02\x54")

func isIPv4(addr string) bool {
	parsedIP, _, err := net.ParseCIDR(addr)
	if err != nil {
		parsedIP = net.ParseIP(addr)
		if parsedIP == nil {
			return false
		}
	}

	return parsedIP.To4() != nil
}

func getIPvX(addr string) IPvX {
	if isIPv4(addr) {
		return IPv4
	}

	return IPv6
}

func generateMAC(ip net.IP) (string, error) {
	if ip.To4() == nil {
		return "", fmt.Errorf("generateMAC is only working with IPv4 addresses")
	}
	hwaddr := make([]byte, hwAddrByteSize)
	copy(hwaddr, macPrefix)
	copy(hwaddr[2:], ip.To4())

	return net.HardwareAddr(hwaddr).String(), nil
}

func (m *Manager) generateUnderlayMAC() string {
	macAddr, _ := generateMAC(net.ParseIP(m.baseConfig.VTEPLoopbackIP))
	return macAddr
}

func (Manager) createIPAddress(addr string, ipv4, ipv6 *IPAddressList) *IPAddress {
	ipList := ipv6
	if isIPv4(addr) {
		ipList = ipv4
	}
	ipList.IPAddresses = append(ipList.IPAddresses, IPAddress{
		IP: addr,
	})
	return &ipList.IPAddresses[len(ipList.IPAddresses)-1]
}

func (Manager) createVRF(name string, table int, ns *Namespace) *VRF {
	ns.VRFs = append(ns.VRFs, VRF{
		Name:       name,
		TableID:    table,
		Interfaces: &Interfaces{},
		Routing: &Routing{
			NCOperation: Replace,
			Static:      &StaticRouting{},
			BGP:         &BGP{},
		},
	})
	return &ns.VRFs[len(ns.VRFs)-1]
}

func (m *Manager) createBridge(
	name string, macAddr string, intfs *Interfaces,
	mtu int, underlayRMAC, assignEUI bool,
) *Bridge {
	br := Bridge{
		Name: name,
		MTU:  &mtu,
	}

	if macAddr == "" && underlayRMAC {
		macAddr = m.generateUnderlayMAC()
	}
	if macAddr != "" {
		br.Ethernet = &Ethernet{
			MacAddress: macAddr,
		}
	}
	if !assignEUI {
		br.NetworkStack = &NetworkStack{
			IPv6: &NetworkStackV6{
				AddrGenMode: types.ToPtr(NoLinkLocal),
			},
		}
	}

	intfs.Bridges = append(intfs.Bridges, br)

	return &intfs.Bridges[len(intfs.Bridges)-1]
}

func (m *Manager) createVXLAN(
	name string, br *Bridge, intfs *Interfaces,
	vni, mtu int, hairpin, neighSuppress bool,
) {
	vxlan := VXLAN{
		Name:          name,
		VNI:           vni,
		MTU:           &mtu,
		Port:          types.ToPtr(vxlanPort),
		Local:         types.ToPtr(m.baseConfig.VTEPLoopbackIP),
		Learning:      types.ToPtr(false),
		LinkInterface: types.ToPtr(underlayInterfaceName),
		Ethernet: &Ethernet{
			MacAddress: m.generateUnderlayMAC(),
		},
		NetworkStack: &NetworkStack{
			IPv6: &NetworkStackV6{
				AddrGenMode: types.ToPtr(NoLinkLocal),
			},
		},
	}
	intfs.VXLANs = append(intfs.VXLANs, vxlan)

	slave := BridgeSlave{
		Name:             name,
		Learning:         types.ToPtr(false),
		NeighborSuppress: &neighSuppress,
		Hairpin:          &hairpin,
	}
	br.Slaves = append(br.Slaves, slave)
}

func (m *Manager) createVLAN(vlanID, mtu int, br *Bridge, intfs *Interfaces) *VLAN {
	vlan := VLAN{
		Name:          fmt.Sprintf("%s%d", vlanPrefix, vlanID),
		MTU:           &mtu,
		VlanID:        vlanID,
		LinkInterface: m.baseConfig.TrunkInterfaceName,
		NetworkStack: &NetworkStack{
			IPv6: &NetworkStackV6{
				AddrGenMode: types.ToPtr(NoLinkLocal),
			},
		},
	}
	intfs.VLANs = append(intfs.VLANs, vlan)

	slave := BridgeSlave{
		Name: vlan.Name,
	}
	br.Slaves = append(br.Slaves, slave)

	return &intfs.VLANs[len(intfs.VLANs)-1]
}
