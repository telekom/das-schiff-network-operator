# cni-routed — routed, no-shared-L2 CNI for KubeVirt VMs (and routed pods)

`cni-routed` gives a workload (a KubeVirt VM, or later a routed pod) a **fully
routed** secondary interface with **no shared L2**: the workload gets a real
routable IPv4 `/32` + IPv6 `/128`, and the CRA-side veth end is moved into the
CRA network namespace where the routing daemon (FRR / 6WIND VSR) advertises
on-link host routes to it via BGP.

## How it works

Multus invokes the plugin for a secondary network. On `ADD` the plugin (for the
default `veth` + `routed` path):

1. Delegates to the configured **IPAM** (`static` or `host-local`) to obtain the
   workload's `/32` + `/128`.
2. Creates a **veth pair** in the pod netns. The pod-side end keeps the IPAM
   addresses and is the Multus interface (KubeVirt's built-in `bridge` binding
   enslaves it to a private, per-pod 2-port bridge together with the qemu tap —
   this is the only L2 and it is **not** a shared broadcast domain).
3. Moves the **peer end** into the CRA network namespace (see *netns discovery*),
   names it `cra<sha256(containerID)[:12]>` and brings it up.
4. Hands the attachment to the **node-local CRA agent** over gRPC. The plugin is
   flavor-agnostic; the agent programs the CRA-side datapath its own way per
   flavor (netlink via `frr-cra` for FRR, NETCONF for VSR): the on-link
   **link-local gateway** addresses the workload uses as its next-hop
   (`169.254.1.1/32`, `fe80::1/128` by default) and the **on-link host routes**
   (`<ip>/32`, `<ip>/128`) for the workload's addresses.

`DEL` reverses everything (removing the veth removes both ends; the agent drops
the attachment).

The `l2` attach mode and the `vhostuser` transport vary steps 2–4 — see *Attach
modes and transports* below.

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
| `ipam`              | for `veth` | —     | delegated IPAM block (`static` or `host-local`); optional for `vhostuser` (guest-side addressing) |
| `attachMode`        | no       | `routed`| `routed` (VRF/underlay + on-link gateway + host routes) or `l2` (bridge-slave to an existing L2 domain) |
| `transport`         | no       | `veth`  | `veth` (a veth pair moved into the CRA netns), `vhostuser` (DPDK/virtio-user fast-path socket, **VSR/grout only**), or `grouttap` (grout creates a `net_tap` in the CRA netns and the CNI moves it into the pod, **grout only**) |
| `vrf`               | no       | *(underlay)* | CRA VRF device name; omit/`default`/`main` for the underlay/default table. Only for `attachMode: routed` (must be unset for `l2`) |
| `layer2AttachmentRef` | for `l2` | —     | `{name, namespace}` of the originating `Layer2Attachment`; the agent binds the port to the NNC `Layer2` whose stamped `attachmentRef` matches |
| `socketPath`        | for `vhostuser` | — | vhost-user unix socket path shared with the workload |
| `socketMode`        | for `vhostuser` | — | `client` or `server` from the workload's perspective (VSR inverts it) |
| `agentSocket`       | no       | `/run/das-schiff/routed-cni.sock` | unix socket of the node-local CRA agent that programs the CRA-side datapath |
| `craNetns`          | no       | `auto`  | `auto` (discover by trunk), a named netns under `/var/run/netns/<name>`, or an absolute path (e.g. `/proc/<pid>/ns/net`) |
| `trunkInterface`    | no       | `hbn`   | interface that identifies the CRA netns during auto-discovery |
| `linkLocalGateways` | no       | `169.254.1.1` / `fe80::1` | on-link next-hop addresses configured on the CRA-side port (`routed` only) |
| `mtu`               | no       | `1500`  | veth MTU |

Example (underlay, static IPAM) — see
[`e2e/kubevirt/manifests/networkattachmentdefinition.yaml`](../../e2e/kubevirt/manifests/networkattachmentdefinition.yaml),
plus the L2-attach and vhost-user variants alongside it.

## Attach modes and transports (two orthogonal axes)

The attachment is described by two independent axes:

- **transport** — how the CRA-side port is wired:
  - `veth` (default): a veth pair whose CRA-side end is moved into the CRA netns.
  - `vhostuser`: a DPDK/virtio-user vhost socket (VM attach). There is no veth
    and no netns port move; the fast path (VSR `fpvhost` / grout `net_vhost`)
    terminates the socket. **VSR/grout only** — the FRR agent rejects it.
  - `grouttap`: the **grout** fast path creates a `net_tap` in the CRA netns and
    the CNI moves it into the pod netns (grout cannot adopt a moved-in kernel
    veth). The handoff is *inverted* relative to `veth`: the agent creates the
    tap and the CNI polls the CRA netns for it, then moves/renames/addresses it.
    **grout only.**
- **attach mode** — what is done with that port:
  - `routed` (default): VRF/underlay + on-link gateway + workload host routes.
  - `l2`: the port is enslaved to an **existing** L2 bridge (referenced by
    `layer2AttachmentRef`) as a bridge slave, with no L3 addressing. The
    bridge/L2VNI is assumed to already exist on the node (from the
    `Layer2Attachment` / `Layer2NetworkConfiguration` pipeline).

All four combinations are valid except `vhostuser` + FRR. The `veth` + `routed`
combination is the original behaviour and is unchanged.

**grout tap handoff (`grouttap`).** grout owns a DPDK fast path and cannot adopt
a moved-in kernel veth, so a routed pod attach is inverted: the CNI hands the
attachment to the agent (which persists it and triggers the grout reconcile so
the grout-cra sidecar's `grcli` creates a `net_tap` named after the port in the
CRA netns), then **polls the CRA netns until that tap appears**, moves it into
the pod netns, renames it to the requested interface, addresses it from IPAM, and
installs the on-link default (unless `l2`). This keeps the attach synchronous
from the pod/KubeVirt point of view (`eth0` exists before `ADD` returns). The
agent's `Add` reply carries the tap name for the CNI to wait on. grout keeps the
tap's DPDK fd bound after the netdev leaves the CRA netns, so forwarding
survives the move. VM attach with grout uses `vhostuser` (a `net_vhost` port).

**L2 binding by attachment ref.** The intent builder stamps the originating
`Layer2Attachment` identity (`AttachmentRef`) onto each NNC `Layer2`. An `l2`
port entry carries a `layer2AttachmentRef`; the node-local agent matches it
against the stamped `Layer2.AttachmentRef` and enslaves the port to that
Layer2's bridge (FRR `l2.<vlanID>`, VSR `l2.<vlanID>` link-interface). No VNI or
VLAN id is needed in the CNI config, and the node-local server does no extra API
lookups.

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

The plugin is **flavor-agnostic**: for the `veth` transport it only creates the
veth and moves the CRA-side port into the CRA netns, then hands the attachment to
the node-local agent over the gRPC socket (`agentSocket`). The agent records it
in the node's `NodeRoutedPorts` object (durable, aggregate per-node state) and
merges it into the `NodeNetworkConfig` before programming it per flavor:

- **cra-frr:** the agent writes netlink in the CRA-FRR netns. Routed ports get
  the on-link `/32` + `/128` (redistributed via connected/kernel/static); `l2`
  ports are enslaved to the `l2.<vlanID>` bridge. For the **underlay** path the
  FRR *default* instance must redistribute connected/kernel toward the fabric
  neighbors (and gain an IPv6 unicast address-family); see the plan for the exact
  template change. FRR **rejects** the `vhostuser` transport.
- **cra-vsr:** the VSR fast path owns the FIB, so the port is programmed via
  NETCONF: an `interface infrastructure <ifname>` with `port infra-<ifname>` +
  the on-link gateway addresses, plus interface-static routes
  (`ipv4-route/ipv6-route <ip> next-hop <ifname>`) for `routed`; a bridge
  `link-interface` for `l2`. The `vhostuser` transport renders an `fpvhost`
  fast-path virtual-port (`system fast-path virtual-port fpvhost fpvhost-<net>
  socket-mode <inverted>`) + `interface fpvhost <ifname> port fpvhost-<net>`.
  See `pkg/cra-vsr/routed.go` / `layer2.go` and `pkg/routedcni` (transport).
- **cra-grout:** FRR (control plane) + grout (DPDK fast path). The agent renders
  an `grcli` batch applied by the `grout-cra` sidecar. A routed pod attach uses
  the `grouttap` transport (grout creates a `net_tap`, the CNI moves it into the
  pod); a VM attach uses `vhostuser` (grout `net_vhost` port). The sidecar
  applies the desired-state batch **line-by-line, tolerating "exists" errors**,
  so a second pod's reconcile re-applying the first pod's ports is idempotent and
  still creates the new tap. See `pkg/cra-grout/` and `cmd/grout-cra`.

```
CNI ADD/DEL --gRPC(unix)--> agent --> NodeRoutedPorts CR (durable)
                                   \-> merge into NodeNetworkConfig
                                       --> netlink (FRR) | NETCONF (VSR)
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
