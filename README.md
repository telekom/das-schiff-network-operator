# network-operator

This operator configures netlink interfaces (VRFs, Bridges, VXLANs), attach some simple **eBPF** tc filters and template FRR configuration.

**NOTE:** This project is **not production ready**! Please use with caution.

## Overview

![Peerings](docs/vrfs.png)

## Configuration

![Config Flow](docs/config.png)

After a change to the FRR config is detected a systemd reload for unit frr.service is issued via the dbus socket.

The reconcile process is runs at start time and everytime a change on the cluster is detected. However there is a debounce mechanism in place which only applies changes every 30s. From the first change "request" there is a gap of 30s until the execution of all changes that happened in that gap.

This tool is configured from multiple places:

### Configfile

The configfile is located at `/opt/network-operator/config.yaml` and contains the mapping from VRFs names to **VNIs**, interfaces the operator should attach its **BPF** code to and VRFs for which it should only configure **route-maps** and **prefix-lists**.

```yaml
vnimap: # VRF name to VNI mapping for the site
  vrf1: 100
  vrf2: 101
  vrf3: 102
skipVRFConfig: # Only configure route-maps / prefix-lists for VRFs from the provisioning process
- mgmt_vrf
bpfInterfaces: # Attach eBPF program to the interfaces from the provisioning process
- "br.mgmt_vrf"
- "mgmt_vrf_def"
- "vx.200"
```

### CustomResource VRFRouteConfiguration

The peerings and route leaking are configured from this custom resource. It contains a VRF to peer to and the routes that should be imported & exported.

There can be multiple CustomResources per VRF but it needs to be ensured that sequence numbers of them are not conflicting. In general each CustomResource is templated as one route-map entry for each direction and one prefix-list for each direction.

### FRR Template

For the network-operator to work some config hints need to be placed in the FRR configuration the node boots with. These hints should be comments and mark the place where we template our configuration. The following hints need to be placed:

| **Hint**                      | **Position**                                                    |
|-------------------------------|-----------------------------------------------------------------|
| `#++{{.VRFs}}`        | Usually at the beginning, after the configuration of other VRFs |
| `#++{{.Neighbors}}`   | At the router-bgp config for the default VRF                    |
| `#++{{.NeighborsV4}}` | At the router-bgp-af-ipv4-uni config for the default VRF        |
| `#++{{.BGP}}`         | After all router-bgp for every VRF are configured               |
| `#++{{.PrefixLists}}` | Can be placed anywhere, somewhere at the end                    |
| `#++{{.RouteMaps}}`   | Can be placed anywhere, somewhere at the end                    |

Also some special config should be in place:

1. A route-map for the standard management peering (to be able to dynamically configure additional exports at runtime), e.g. for mgmt_vrf:
    ```
    route-map rm_import_cluster_to_oam permit 20
        call rm_mgmt_vrf_export
    exit
    route-map rm_mgmt_vrf_export deny 65535
    exit
    ```
    Due to internal parsing of FRR one route-map entry should already be there (default deny). Also mgmt_vrf needs to be part of the list `skipVRFConfig` in the configuration file.
2. The default VRF `router-bgp` config must be marked as `router bgp <ASN> vrf default`. Otherwise a reload will delete it and replace it completely. This will break and crash FRR in the process.

## eBPF

The current design has evolved from a simple idea but there were some issues with the first iterations. This ultimately required to use eBPF in the **tc filter** chain.

1. We **can not** do route-leaking. There are some issues in the Linux Kernel that prevent us from doing this. The biggest issue was the issue for the Kernel to match a packet to an already established TCP connection.

2. VRFs with peer-veth (Virtual Ethernet) links work but have unforeseen consequences on netfilter/conntrack. With the current design of the kube-proxy and calico iptables rules packets are processed 2-3 times. This leads to conflichts in the conntrack tables.

3. There is a possibility to insert NOTRACK rules for the interfaces but we still need to process the whole stack when we actually do not need to do this.

4. XDP works on Intel cards (basically every card that does not do VXLAN / RX checksum offloading) but not on Mellanox. Because we are already working at the sk_buff level in the Kernel (after VXLAN decap) and we most probably process the packet in the local Linux Kernel anyway (with a need for that sk_buff) we would have needed to use **xdgeneric** anyway.

5. Our final decision was to use **eBPF** attached to **tc filter**. This simple **eBPF** program (from `pkg/bpf/router.c`) just routes packets from one interface to the other.

The following eBPF program is attached to all VXLAN, Bridge and VETH sides inside a VRF for VRFs. This is not applied to the cluster traffic VXLAN/Bridge.

An internal loop is tracking these interfaces and reapplies the eBPF code when needed.

![eBPF Flow](docs/ebpf-flow.png)

## Development Setup

Used Networks:
```python
## AS Numbers
# 64496-64511
as_numbers = range(64496,64512)
# [64496, 64497, 64498, 64499, 64500, 64501, 64502, 64503, 64504, 64505, 64506, 64507, 64508, 64509, 64510, 64511]
## AS Reservations
gobgp_as_number = 64496
node_as_number = 64510
node_local_as = 64511

## Fabric AS Numbers
fabric_as_numbers = range(64497,64510)
# [64497, 64498, 64499, 64500, 64501, 64502, 64503, 64504, 64505, 64506, 64507, 64508, 64509]

## Underlay Networks
underlay_network_fabric_ipv4 = "192.0.2.0/25"
underlay_network_node_ipv4 = "192.0.2.128/25"

## Overlay Networks
overlay_cluster_network_k8s_node_ipv4 = "198.51.100.0/24"
overlay_cluster_network_k8s_node_ipv6 = "2001:db8:ffff:ffff::/64"

## Networks which are used for the Route-Dicer
# Route AS Numbers
# 65536-65551
routes_as_numbers = range(65536,65552)
# [65536, 65537, 65538, 65539, 65540, 65541, 65542, 65543, 65544, 65545, 65546, 65547, 65548, 65549, 65550, 65551]

routes_ipv4 = "203.0.113.0/24"
routes_ipv6 =  "2001:db8::/48"
```

![Development Setup](docs/test-env-setup.drawio.png)


First you need to have multiple tools installed to get it working.

* `kind`
  * `yay -S kind`
* `podman` or `docker`
  * `pacman -S docker`
  * `pacman -S podman`
* `frr`
  * `yay -S frr-git`
* `iproute2`
  * `pacman -S iproute2`
* optional `containerlab`
  * `yay -S containerlab-bin`

When using podman follow this guide to setup proper rootless container environment:

* https://kind.sigs.k8s.io/docs/user/rootless/
* https://wiki.archlinux.org/title/Podman




Starting a kind cluster with `podman` rootless:
```bash
KIND_EXPERIMENTAL_PROVIDER=podman systemd-run --scope --user kind create cluster
```

Configure the surrounding kind cluster which should be used to host a kube-apiserver for the local development.

```bash
# Load the ip6_tables kernel module
sudo modprobe ip6_tables
# Start docker
sudo systemctl start docker
# create a kind cluster if you already have a cluster
# just start the docker container of the kind cluster
kind create cluster
kind get kubeconfig > ~/.kube/kind
# start the kind cluster container
docker start kind-control-plane
export KUBECONFIG=~/.kube/kind
# Load the image for network-operator into the kind cluster
kind load docker-image network-operator:latest
# Build the gobgp container image with docker
docker build -t gobgp-fabric:latest -t docker.io/library/gobgp-fabric:latest testdata/gobgp
# setup the bridges on the local host
sudo bash testdata/containerlab-bridge-setup.sh
# Install the containerlab
sudo containerlab deploy --reconfigure -t testdata/containerlab.yaml
# TODO: install at least the crds inside the cluster.
make install
# create a tls folder locally.
mkdir -p $(pwd)/tls
# TODO: some setup for tls I just stole it from the cluster.
<missing commands here>
```


Configure the frr service for the netns you want to run in in this case we will use the network namespace called `test` and save the configuration.

```bash
sudo systemctl edit frr.service
# /etc/systemd/system/frr.service.d/override.conf
[Service]
NetworkNamespacePath=/var/run/netns/test
##
## Now we need to have the config and the template available. Just copy it inplace
cp testdata/frr.conf* /etc/frr/
cp testdata/daemons /etc/frr/
```

Now we can setup the network namespace for frr and network-operator to run in it.

```bash
## This creates the netns and needed interfaces
## as well as forwards.
## the -E is important to forward your local environment
sudo -E bash testdata/test-netns-setup.sh

## This finally starts the Operator for development testing in the network namespace called test.
OPERATOR_CONFIG=$(pwd)/testdata/config.yaml sudo -E ip netns exec test go run cmd/manager/main.go --config $(pwd)/testdata/manager-config.yaml
```
### Networking healthcheck

After deployment basic networking connectivity can be tested to ensure that the node is fully operable. If all checks will pass, taint `node.cloudprovider.kubernetes.io/uninitialized` will be removed from the node if applied.

There are 3 types of checks implemented currently:
 - FRR check - will check if FRR's daemon is up and running. This check will be only used in pods that DO NOT work in bpf-only mode.
 - interfaces - will check if all network interfaces that are expected to are up
 - reachability - will check if TCP connection to provided hosts can be achieved. Each host will be queried `retries` times before reporting failure.

 Interfaces and reachability checks can be configured through a config file located in `/opt/network-operator/net-healthcheck-config.yaml` on the node.

 The syntax of the config file is as follow:

 ```
interfaces: # list of interfaces that should have UP state
  - eth0
reachability: # list of hosts and ports to query in reachability check
- host: "host.domain.com"
  port: 80
- host: "192.168.1.100"
  port: 8400
timeout: 5s # timeout for reachability test (if not provided, default 3s will be used)
retries: 10 # indicates how many times each host provided in reachibility list will be queried before failure will be reported, default 3
 ```

The location of the networking healthcheck config file can be altered by setting the `OPERATOR_NETHEALTHCHECK_CONFIG` environment variable on the node to the desired path, e.g.:

```bash
OPERATOR_NETHEALTHCHECK_CONFIG=/etc/network-operator/my-healtcheck-config.yaml
```

> NOTE: Please be aware that the custom config file location has to be mounted into the network-operator pod.

## License

This project is licensed under Apache License Version 2.0, with the **exception of the code in [`./bpf/](./bpf/)** which falls under the [GPLv2 license](./bpf/LICENSE).

Copyright (c) 2022 Deutsche Telekom AG.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the License.

You may obtain a copy of the License at https://www.apache.org/licenses/LICENSE-2.0.

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the LICENSE for the specific language governing permissions and limitations under the License.
