# cni-routed — routed, no-shared-L2 CNI for KubeVirt VMs (and routed pods)

`cni-routed` gives a workload (a KubeVirt VM, or later a routed pod) a **fully
routed** secondary interface with **no shared L2**: the workload gets a real
routable IPv4 `/32` + IPv6 `/128`, and the CRA-side veth end is moved into the
CRA network namespace where the routing daemon (FRR / 6WIND VSR) advertises
on-link host routes to it via BGP.

## How it works

Multus invokes the plugin for a secondary network. On `ADD` the plugin:

1. Delegates to the configured **IPAM** (`static` or `host-local`) to obtain the
   workload's `/32` + `/128`.
2. Creates a **veth pair** in the pod netns. The pod-side end keeps the IPAM
   addresses and is the Multus interface (KubeVirt's built-in `bridge` binding
   enslaves it to a private, per-pod 2-port bridge together with the qemu tap —
   this is the only L2 and it is **not** a shared broadcast domain).
3. Moves the **peer end** into the CRA network namespace (see *netns discovery*),
   names it `cra<sha256(containerID)[:12]>`, brings it up and:
   - configures the on-link **link-local gateway** addresses the workload uses as
     its next-hop (`169.254.1.1/32`, `fe80::1/128` by default);
   - installs **on-link host routes** (`<ip>/32`, `<ip>/128`, scope link) for the
     workload's addresses.

`DEL` reverses everything (removing the veth removes both ends).

### VRF vs underlay

- **`vrf` omitted / `default` / `main` → UNDERLAY.** The CRA-side port is left in
  the CRA netns default routing table (`RT_TABLE_MAIN`). The fabric/underlay BGP
  session redistributes the on-link `/32` + `/128` toward the leaf/DCGW. This is
  the datapath test target: reach the VM/pod IP from a leaf/DCGW **via the
  underlay**, not the EVPN overlay.
- **`vrf: <name>` → tenant VRF.** The port is enslaved to that VRF device and the
  routes are programmed in the VRF's table (exported e.g. as EVPN type-5).

## Configuration

Delivered per secondary network via a `NetworkAttachmentDefinition`
(`spec.config`). Fields:

| field               | required | default | description |
| ------------------- | -------- | ------- | ----------- |
| `type`              | yes      | —       | must be `cni-routed` |
| `ipam`              | yes      | —       | delegated IPAM block (`static` or `host-local`) |
| `vrf`               | no       | *(underlay)* | CRA VRF device name; omit/`default`/`main` for the underlay/default table |
| `flavor`            | no       | `frr`   | CRA flavor: `frr` (plugin programs the CRA-side FIB with netlink) or `vsr` (plugin only moves the port and hands the attachment to the node-local agent over gRPC, which renders it via NETCONF) |
| `agentSocket`       | no       | `/run/das-schiff/routed-cni.sock` | unix socket of the node-local CRA agent; only used for `flavor: vsr` |
| `craNetns`          | no       | `auto`  | `auto` (discover by trunk), a named netns under `/var/run/netns/<name>`, or an absolute path (e.g. `/proc/<pid>/ns/net`) |
| `trunkInterface`    | no       | `hbn`   | interface that identifies the CRA netns during auto-discovery |
| `linkLocalGateways` | no       | `169.254.1.1` / `fe80::1` | on-link next-hop addresses configured on the CRA-side port |
| `mtu`               | no       | `1500`  | veth MTU |

Example (underlay, static IPAM) — see
[`e2e/kubevirt/manifests/networkattachmentdefinition.yaml`](../../e2e/kubevirt/manifests/networkattachmentdefinition.yaml).

## netns discovery

The CRA netns is resolved with this precedence
(`pkg/cni/discovery.go`):

1. an absolute `craNetns` path;
2. a named `craNetns` under `/var/run/netns/<name>`;
3. `auto`: the named namespace that owns `trunkInterface` (mirrors the cra-vsr
   `findWorkNSName` heuristic, so a single value drives both CRA flavors).

`BaseConfig.CRANetns` / `BaseConfig.TrunkInterfaceName` are the natural sources
for generating this per node.

## CRA flavor notes

- **cra-frr:** the plugin writes netlink directly in the CRA-FRR netns. FRR
  redistributes connected/kernel/static, so the on-link `/32` + `/128` are
  advertised. For the **underlay** path the FRR *default* instance must
  redistribute connected/kernel toward the fabric neighbors (and gain an IPv6
  unicast address-family); see the plan for the exact template change.
- **cra-vsr:** the VSR fast path owns the FIB, so the moved port cannot be
  programmed via raw netlink. With `flavor: vsr` the plugin only creates the
  veth and moves the CRA-side port into the CRA netns, then calls the node-local
  agent over the gRPC socket (`agentSocket`). The agent records the attachment in
  the node's `NodeRoutedPorts` object (durable, aggregate per-node state) and
  merges it into the `NodeNetworkConfig` before rendering it as NETCONF: an
  `interface infrastructure <ifname>` with `port infra-<ifname>` + the on-link
  gateway addresses, plus interface-static routes
  (`ipv4-route/ipv6-route <ip> next-hop <ifname>`). See
  `pkg/cra-vsr/routed.go` (`BuildRoutedVRF`) and `pkg/routedcni` (transport).

### Transport (vsr)

```
CNI ADD/DEL --gRPC(unix)--> agent --> NodeRoutedPorts CR (durable)
                                   \-> merge into NodeNetworkConfig --> NETCONF
```

The agent serves the socket at `/run/das-schiff/routed-cni.sock` (a hostPath
shared with the plugin, which runs in the host mount namespace). A change to
`NodeRoutedPorts` triggers a reconcile even when the `NodeNetworkConfig`
revision is unchanged (the merged routed ports are tracked by a content hash),
so attachments are (de)provisioned promptly.

## Build & install

```sh
make build-cni-routed          # host build of bin/cni-routed (Linux)
make docker-build              # also builds das-schiff-cni-routed:latest
make kind-load                 # loads images into the kind cluster
kubectl apply -f e2e/kubevirt/install/daemonset.yaml   # install plugin on nodes
```

The installer DaemonSet copies the binary to `/opt/cni/bin`. The per-network CNI
config travels with the NAD, so no standalone conflist is required.
