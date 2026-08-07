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

	"github.com/telekom/das-schiff-network-operator/pkg/helpers/types"
	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni"
)

// infraPortPrefix is prepended to the in-netns interface name to derive the
// VSR "port" reference for a moved interface (e.g. a workload CNI veth end).
// VSR references an interface that was moved into the CRA network namespace as
// infra-<ifname>, matched via the interface's ifalias (which the workload CNI
// sets to the same value). Shared with pkg/cni via workloadcni so both agree.
const infraPortPrefix = workloadcni.InfraPortPrefix

// WorkloadPort describes a single routed workload attachment whose CRA-side
// interface has been moved into the CRA network namespace by the workload CNI.
// The VSR flavor cannot program the datapath via raw netlink (the fast path
// owns it), so the on-link gateway addresses and the workload's host routes
// are rendered as NETCONF and pushed instead.
type WorkloadPort struct {
	// IfName is the interface name inside the CRA network namespace (the moved
	// veth end, e.g. "cra012345"). Referenced by VSR as infra-<IfName>.
	IfName string
	// GatewayV4 is the on-link IPv4 gateway address (with prefix length, e.g.
	// "169.254.100.100/32") configured on the infrastructure interface.
	GatewayV4 string
	// GatewayV6 is the on-link IPv6 gateway address (with prefix length, e.g.
	// "fd00:7:caa5:1::/128") configured on the infrastructure interface.
	GatewayV6 string
	// HostRoutes are the workload's routable host addresses (e.g. "10.0.0.5/32",
	// "fd00:200::5/128") installed as interface-static routes via IfName so VSR
	// redistributes them into BGP.
	HostRoutes []string
}

// applyWorkloadPorts merges the given workload ports into an already-composed VRF:
// each port adds an infrastructure interface (port infra-<ifname> + on-link
// gateway addresses) and interface-static routes for the workload host routes.
//
// It never creates a VRF. The VSR reconcile path looks up the existing
// cluster/fabric/local L3VRF (LookupVRF) assembled from the NodeNetworkConfig
// spec and layers the workload ports onto it, so a routed attachment is bound into
// the VRF that already exists rather than a duplicate one.
func applyWorkloadPorts(vrf *VRF, ports ...WorkloadPort) error {
	if len(ports) == 0 {
		return nil
	}
	if vrf.Interfaces == nil {
		vrf.Interfaces = &Interfaces{}
	}
	if vrf.Routing == nil {
		vrf.Routing = &Routing{}
	}
	return addWorkloadPorts(vrf.Interfaces, vrf.Routing, ports)
}

// applyGlobalWorkloadPorts renders workload ports that were requested without a
// target VRF into the namespace's default (no-l3vrf) table: an infrastructure
// interface (port infra-<ifname> + on-link gateway addresses) plus
// interface-static routes for the workload host routes, exactly as for an L3VRF.
// Unlike an L3VRF (which redistributes static/connected into its own BGP), the
// default table's BGP is the underlay session, so each host route is instead
// advertised via an explicit BGP network statement (see advertiseHostRoutes).
func applyGlobalWorkloadPorts(ns *Namespace, ports ...WorkloadPort) error {
	if len(ports) == 0 {
		return nil
	}
	if ns.Interfaces == nil {
		ns.Interfaces = &Interfaces{}
	}
	if ns.Routing == nil {
		ns.Routing = &Routing{}
	}
	if err := addWorkloadPorts(ns.Interfaces, ns.Routing, ports); err != nil {
		return err
	}
	advertiseHostRoutes(ns.Routing.BGP, ports)
	return nil
}

// addWorkloadPorts renders each workload port as an infrastructure interface (port
// infra-<ifname> + on-link gateway addresses) plus interface-static routes for
// the workload host routes into the given interface and routing containers.
func addWorkloadPorts(ifaces *Interfaces, routing *Routing, ports []WorkloadPort) error {
	if routing.Static == nil {
		routing.Static = &StaticRouting{}
	}

	for i := range ports {
		p := ports[i]
		if p.IfName == "" {
			return fmt.Errorf("workload port %d: ifname is required", i)
		}

		infra := Infrastructure{
			Name: p.IfName,
			Port: types.ToPtr(infraPortPrefix + p.IfName),
		}
		if p.GatewayV4 != "" {
			infra.IPv4 = &IPAddressList{IPAddresses: []IPAddress{{IP: p.GatewayV4}}}
		}
		if p.GatewayV6 != "" {
			infra.IPv6 = &IPAddressList{IPAddresses: []IPAddress{{IP: p.GatewayV6}}}
		}
		ifaces.Infras = append(ifaces.Infras, infra)

		for _, dst := range p.HostRoutes {
			route := StaticRoute{
				Destination: dst,
				NextHops:    []NextHop{{NextHop: p.IfName}},
			}
			if isIPv4(dst) {
				routing.Static.IPv4 = append(routing.Static.IPv4, route)
			} else {
				routing.Static.IPv6 = append(routing.Static.IPv6, route)
			}
		}
	}
	return nil
}

// advertiseHostRoutes adds a BGP network statement for each workload-port host
// route so the default-table underlay session advertises the workload /32,/128
// prefixes (which exist in the RIB as the interface-static routes). It is a
// no-op when bgp is nil (no underlay session composed).
func advertiseHostRoutes(bgp *BGP, ports []WorkloadPort) {
	if bgp == nil {
		return
	}
	if bgp.AF == nil {
		bgp.AF = &BGPAddrFamily{}
	}
	for i := range ports {
		for _, dst := range ports[i].HostRoutes {
			if isIPv4(dst) {
				if bgp.AF.UcastV4 == nil {
					bgp.AF.UcastV4 = &BGPUcast{}
				}
				bgp.AF.UcastV4.Network = append(bgp.AF.UcastV4.Network, BGPUcastNetwork{Prefix: dst})
			} else {
				if bgp.AF.UcastV6 == nil {
					bgp.AF.UcastV6 = &BGPUcast{}
				}
				bgp.AF.UcastV6.Network = append(bgp.AF.UcastV6.Network, BGPUcastNetwork{Prefix: dst})
			}
		}
	}
}
