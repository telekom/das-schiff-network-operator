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
)

// infraPortPrefix is prepended to the in-netns interface name to derive the
// VSR "port" reference for a moved interface (e.g. a routed CNI veth end).
// VSR references an interface that was moved into the CRA network namespace as
// infra-<ifname>.
const infraPortPrefix = "infra-"

// RoutedPort describes a single routed workload attachment whose CRA-side
// interface has been moved into the CRA network namespace by the routed CNI.
// The VSR flavor cannot program the datapath via raw netlink (the fast path
// owns it), so the on-link gateway addresses and the workload's host routes
// are rendered as NETCONF and pushed instead.
type RoutedPort struct {
	// IfName is the interface name inside the CRA network namespace (the moved
	// veth end, e.g. "cra0123456789ab"). Referenced by VSR as infra-<IfName>.
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

// BuildRoutedVRF renders the l3vrf config that binds a routed CNI port into the
// given VRF within the CRA network namespace: it declares the infrastructure
// interface (port infra-<ifname> + on-link gateway addresses) and installs the
// workload's host routes as interface-static routes pointing at the port.
//
// The returned VRF is meant to be attached to the working Namespace's VRFs and
// pushed via NETCONF. Passing vrfTable <= 0 omits the table-id leaf (VSR keeps
// its own allocation).
func BuildRoutedVRF(vrfName string, vrfTable int, ports ...RoutedPort) (*VRF, error) {
	if vrfName == "" {
		return nil, fmt.Errorf("vrf name is required")
	}

	vrf := &VRF{
		Name:       vrfName,
		Interfaces: &Interfaces{},
		Routing: &Routing{
			NCOperation: Merge,
			Static:      &StaticRouting{},
		},
	}
	if err := applyRoutedPorts(vrf, ports...); err != nil {
		return nil, err
	}
	if vrfTable > 0 {
		vrf.TableID = vrfTable
	}
	return vrf, nil
}

// applyRoutedPorts merges the given routed ports into an already-composed VRF:
// each port adds an infrastructure interface (port infra-<ifname> + on-link
// gateway addresses) and interface-static routes for the workload host routes.
// It is used both by BuildRoutedVRF and by the NNC reconcile path, which layers
// routed ports onto the cluster/fabric/local VRFs assembled from the spec.
func applyRoutedPorts(vrf *VRF, ports ...RoutedPort) error {
	if len(ports) == 0 {
		return nil
	}
	if vrf.Interfaces == nil {
		vrf.Interfaces = &Interfaces{}
	}
	if vrf.Routing == nil {
		vrf.Routing = &Routing{}
	}
	if vrf.Routing.Static == nil {
		vrf.Routing.Static = &StaticRouting{}
	}

	for i := range ports {
		p := ports[i]
		if p.IfName == "" {
			return fmt.Errorf("routed port %d: ifname is required", i)
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
		vrf.Interfaces.Infras = append(vrf.Interfaces.Infras, infra)

		for _, dst := range p.HostRoutes {
			route := StaticRoute{
				Destination: dst,
				NextHops:    []NextHop{{NextHop: p.IfName}},
			}
			if isIPv4(dst) {
				vrf.Routing.Static.IPv4 = append(vrf.Routing.Static.IPv4, route)
			} else {
				vrf.Routing.Static.IPv6 = append(vrf.Routing.Static.IPv6, route)
			}
		}
	}
	return nil
}
