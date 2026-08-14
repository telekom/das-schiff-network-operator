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
	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni"
)

// maxVSRInterfaceNameLen is the Linux interface-name limit (IFNAMSIZ-1) the
// fast path materialises VSR interfaces under, which bounds the
// <port>.<vlan> name of a trunk member.
const maxVSRInterfaceNameLen = 15

type Layer2 struct {
	nodeCfg *v1alpha1.NodeNetworkConfigSpec
	ns      *Namespace
	vrouter *VRouter
	mgr     *Manager
	infos   []InfoL2
}

type InfoL2 struct {
	vlanID        int
	mtu           int
	vni           int
	vrf           string
	mac           string
	ips           []string
	acls          []v1alpha1.MirrorACL
	attachedPorts []v1alpha1.AttachedPort
}

func NewLayer2(
	nodeCfg *v1alpha1.NodeNetworkConfigSpec,
	ns *Namespace,
	vrouter *VRouter,
	mgr *Manager,
) *Layer2 {
	return &Layer2{
		nodeCfg: nodeCfg,
		mgr:     mgr,
		ns:      ns,
		vrouter: vrouter,
		infos:   []InfoL2{},
	}
}

func (l *Layer2) setupInformations() {
	for _, l2 := range l.nodeCfg.Layer2s {
		info := InfoL2{
			vlanID:        int(l2.VLAN),
			mtu:           int(l2.MTU),
			vni:           int(l2.VNI),
			acls:          l2.MirrorACLs,
			attachedPorts: l2.AttachedPorts,
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
	br.NetworkStack.IPv6.AcceptDuplicateAD = types.ToPtr("never")

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
			vrf := LookupVRF(l.ns, info.vrf)
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
		vlan := l.mgr.createVLAN(info.vlanID, info.mtu, br, l.ns.Interfaces)

		if err := l.attachPorts(&info, br, intfs); err != nil {
			return err
		}

		// Mirror the Layer2 access port (vlan.<id>), not the bridge master, so
		// port-to-port (east-west) traffic between the workload side and the L2VNI
		// overlay is captured. The port faces the workload, so the workload-
		// perspective direction (ingress = to-workload) is inverted to the port's
		// interface-relative direction.
		for i := range info.acls {
			direction := flipMirrorDirection(string(info.acls[i].Direction))
			l.mgr.createMirrorTraffic(l.ns, vlan.Name, direction, &info.acls[i])
		}
	}

	return nil
}

// attachPorts attaches the workload-CNI L2-attached ports (moved into the CRA
// netns) to the Layer2 bridge: each port becomes a bridge link-interface with no
// L3 addressing. veth-transport ports render as infrastructure interfaces
// (port infra-<ifname>); vhostuser-transport ports render as fpvhost interfaces
// (port fpvhost-<ifname>) plus a global fast-path fpvhost virtual-port.
//
// An access port (VLAN 0) is slaved directly and is therefore an untagged
// member of exactly one domain; its interface entry is created alongside the
// bridge, in the same interface set. A trunk member (VLAN set) is reached
// through a `vlan <ifname>.<vlan>` interface which is slaved in its place, so
// one port can carry several domains and the workload-side id may differ from
// the domain's fabric-side VLAN id. Those interfaces are declared in the
// namespace's own interface set — like the vxlan and fabric vlan interfaces —
// because a trunked port is shared by Layer2s that may live in different IRB
// VRFs and so must be declared exactly once.
func (l *Layer2) attachPorts(info *InfoL2, br *Bridge, intfs *Interfaces) error {
	for i := range info.attachedPorts {
		p := &info.attachedPorts[i]
		if p.Interface == "" {
			return fmt.Errorf("layer2 vni %d: attached port %d has no interface", info.vni, i)
		}

		slave := p.Interface
		portIntfs := intfs
		if p.VLAN != 0 {
			slave = fmt.Sprintf("%s.%d", p.Interface, p.VLAN)
			if len(slave) > maxVSRInterfaceNameLen {
				return fmt.Errorf("layer2 vni %d: trunk sub-interface %q exceeds %d characters",
					info.vni, slave, maxVSRInterfaceNameLen)
			}
			portIntfs = l.ns.Interfaces
		}

		if err := l.declarePort(portIntfs, p, info.vni); err != nil {
			return err
		}
		if p.VLAN != 0 {
			l.declareTrunkVLAN(l.ns.Interfaces, slave, p)
		}

		br.Slaves = append(br.Slaves, BridgeSlave{Name: slave})
	}
	return nil
}

// declarePort declares the workload port itself (the moved veth or the
// vhost-user virtual-port). It is idempotent: a trunked port is attached to
// several Layer2s but must only be declared once.
func (l *Layer2) declarePort(intfs *Interfaces, p *v1alpha1.AttachedPort, vni int) error {
	if p.Transport == v1alpha1.PortTransportVhostUser {
		if p.SocketPath == "" {
			return fmt.Errorf("layer2 vni %d: attached port %s: vhostuser transport requires a socket path",
				vni, p.Interface)
		}
		for i := range intfs.Fpvhosts {
			if intfs.Fpvhosts[i].Name == p.Interface {
				return nil
			}
		}
		intfs.Fpvhosts = append(intfs.Fpvhosts, Fpvhost{
			Name: p.Interface,
			Port: types.ToPtr(fpvhostPortPrefix + p.Interface),
		})
		registerFpvhostVirtualPorts(l.vrouter, []FpvhostVirtualPort{
			newFpvhostVirtualPort(p.Interface, p.SocketPath, p.SocketMode),
		})
		return nil
	}

	for i := range intfs.Infras {
		if intfs.Infras[i].Name == p.Interface {
			return nil
		}
	}
	intfs.Infras = append(intfs.Infras, Infrastructure{
		Name: p.Interface,
		Port: types.ToPtr(infraPortPrefix + p.Interface),
	})
	return nil
}

// declareTrunkVLAN declares the `vlan` interface carrying one tagged member of a
// trunk port. Its vlan-id is the workload-side id, so pairing it with the
// bridge of the domain whose fabric-side id differs is what implements VLAN
// translation. Its name matches the netdev the FRR flavor creates for the same
// member, so both flavors expose the same datapath naming.
//
// Its mtu is the one the attachment requested (the CNI configuration's), which
// the plugin also applied to the port itself, so the sub-interface is sized like
// its link-interface rather than left at the platform default. The 4 bytes the
// tag adds on the wire are the workload's to account for; the merge already
// refused the attachment if no member domain could carry the requested size.
func (*Layer2) declareTrunkVLAN(intfs *Interfaces, name string, p *v1alpha1.AttachedPort) {
	for i := range intfs.VLANs {
		if intfs.VLANs[i].Name == name {
			return
		}
	}
	mtu := int(p.MTU)
	if mtu == 0 {
		mtu = workloadcni.DefaultPortMTU
	}
	intfs.VLANs = append(intfs.VLANs, VLAN{
		Name:          name,
		VlanID:        int(p.VLAN),
		MTU:           &mtu,
		LinkInterface: p.Interface,
		NetworkStack: &NetworkStack{
			IPv6: &NetworkStackV6{
				AddrGenMode: types.ToPtr(NoLinkLocal),
			},
		},
	})
}
