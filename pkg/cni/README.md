# cni-workload — routed, no-shared-L2 CNI for KubeVirt VMs (and routed pods)

`cni-workload` gives a workload (a KubeVirt VM, or later a routed pod) a **fully
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
   names it `cra<sha256(containerID + "/" + ifName)[:6]>`, sets its ifalias to
   `infra-<portname>` (required by VSR, ignored by FRR) and brings it up.
4. Hands the attachment to the **node-local CRA agent** over the gRPC unix
   socket. The agent records it in the node's `NodeWorkloadPorts` object and does
   *all* L3 programming — the on-link **link-local gateway** addresses the
   workload uses as its next-hop (`169.254.1.1/32`, `fe80::1/128` by default)
   and the **on-link host routes** (`<ip>/32`, `<ip>/128`) for the workload's
   addresses.

### IPv4 over the IPv6 next-hop (`grouttap` only)

Every routed port asks for the *same* on-link gateway pair, which the kernel and
the VSR are both happy to hold on many interfaces at once. grout is not: it
keeps one node-global IPv4 address table with no per-interface scope for
link-local space, so `169.254.1.1/32` fits on exactly one port and the second
routed attachment on a node is rejected with `EADDRINUSE`. IPv6 link-local *is*
scoped per interface, so `fe80::1/128` is accepted on all of them.

On the `grouttap` transport the plugin therefore never asks the agent to program
an IPv4 gateway, and gives the pod an IPv4 default route whose next-hop is the
IPv6 gateway instead (`RTA_VIA`, the RFC 5549 forwarding case — the equivalent
of `ip -4 route add default via inet6 fe80::1 dev net1`). Nothing then needs an
IPv4 address on the CRA side, and a node can carry as many routed attachments as
any other flavour. The other transports are unchanged.

The plugin is therefore **flavor-agnostic**: it only wires the veth. `DEL`
reverses everything (removing the veth removes both ends) and tells the agent to
drop the attachment.

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
| `type`              | yes      | —       | must be `cni-workload` |
| `ipam`              | for routed `veth` | — | delegated IPAM block (`static` or `host-local`); optional for `vhostuser` (guest-side addressing) and for `attachMode: l2` (the workload is addressed inside the L2 domain). Always delegated and applied when present |
| `attachMode`        | no       | `routed`| `routed` (VRF/underlay + on-link gateway + host routes) or `l2` (attachment to existing L2 domains) |
| `transport`         | no       | `veth`  | `veth` (a veth pair moved into the CRA netns), `vhostuser` (DPDK/virtio-user fast-path socket, **VSR/grout only**), or `grouttap` (grout creates a `net_tap` in the CRA netns and the CNI moves it into the pod, **grout only**) |
| `vrf`               | no       | *(underlay)* | CRA VRF device name; omit/`default`/`main` for the underlay/default table. Only for `attachMode: routed` (must be unset for `l2`) |
| `layer2AttachmentRef` | for `l2` | —     | `{name}` of the originating `Layer2Attachment`; the port becomes an **untagged access port** of it. Mutually exclusive with `layer2Trunk` |
| `layer2Trunk`       | for `l2` | —       | list of `{name, vlan}` members carried on the port as an **802.1Q trunk**; `vlan` is optional and defaults to the domain's own VLAN id. Mutually exclusive with `layer2AttachmentRef` |
| `socketPath`        | no       | *(derived)* | vhost-user socket path override; normally derived from the device-plugin `deviceID` (see *vhost-user and the 6WIND device plugin*) |
| `socketMode`        | no       | *(from device info)* | `client` or `server` from the workload's perspective (VSR inverts it); normally taken from the staged device-info file |
| `deviceResource`    | no       | `nc-k8s-plugin.6wind.com/virtio-user` | device-plugin resource the attachment was allocated from; selects which socket tree is host- vs pod-side (see *vhost-user and the 6WIND device plugin*) |
| `agentSocket`       | no       | `/run/das-schiff/workload-cni.sock` | unix socket of the node-local CRA agent |
| `craNetns`          | no       | `auto`  | `auto` (discover by trunk), a named netns under `/var/run/netns/<name>`, or an absolute path (e.g. `/proc/<pid>/ns/net`) |
| `trunkInterface`    | no       | `hbn`   | interface that identifies the CRA netns during auto-discovery |
| `linkLocalGateways` | no       | `169.254.1.1` / `fe80::1` | on-link next-hop addresses the agent configures on the CRA-side port (`routed` only; on `grouttap` only the IPv6 one, see below) |
| `mtu`               | no       | `1500`  | veth MTU; in L2 mode the domain must be able to carry it (see [L2 trunking](#l2-trunking)) |

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
  - `l2`: the port is attached to one or more **existing** L2 bridges with no L3
    addressing — either as an untagged access port (`layer2AttachmentRef`) or as
    an 802.1Q trunk (`layer2Trunk`). The bridge/L2VNI is assumed to already exist
    on the node (from the `Layer2Attachment` / `Layer2NetworkConfiguration`
    pipeline).

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
port entry carries the name(s) of the `Layer2Attachment`(s) it wants; the
node-local agent matches them against the stamped `Layer2.AttachmentRef` and
attaches the port to those Layer2s' bridges (FRR `l2.<vlanID>`, VSR
`l2.<vlanID>` link-interface). No VNI or VLAN id is needed in the CNI config, and
the node-local server does no extra API lookups.

**References are name-only.** `Layer2Attachment` is namespaced, but the whole
intent pipeline is already scoped to one namespace by the operator's
`--intent-namespace` flag, so repeating it in every NAD is pure boilerplate. The
CRA agents take the same `--intent-namespace` flag (default `default`) and stamp
it onto every reference they record, so `NodeWorkloadPorts` and
`NodeNetworkConfig` stay fully qualified.

**Missing domains are all-or-nothing.** If any referenced `Layer2Attachment` is
not (yet) configured on the node, the *whole* attachment is skipped and logged
rather than half-wired; it is applied by a later reconcile once the L2A pipeline
created the bridge.

### L2 trunking

`layer2Trunk` carries several L2 domains on one port. Every member is **tagged**:

```jsonc
{
  "attachMode": "l2",
  "layer2Trunk": [
    { "name": "green" },             // pod-side tag == the domain's own VLAN id
    { "name": "red", "vlan": 200 }   // translated to pod-side VLAN 200
  ]
}
```

A member without `vlan` is carried under the domain's own VLAN id, which the
agent resolves from the `NodeNetworkConfig` (the plugin never reads
`Layer2Attachment`s). A member *with* `vlan` translates: the port-side id is the
one configured here, the fabric-side one stays the domain's. Ids must be
1..4094, and no two members may reference the same domain or land on the same
port-side id — including after inheritance, which only the agent can check.

Each member is realised as a VLAN sub-interface `<port>.<podVlan>` enslaved to
its domain's bridge — a netlink `vlan` link on FRR, an `interface vlan` on VSR,
identically named on both.

**Access and trunk are mutually exclusive.** A native (untagged) member would
require the raw port itself to be a bridge slave while sub-interfaces demux the
tagged members off the same port. Linux happens to allow that, but any VLAN id
*without* a member then falls through to the raw port and floods the untagged
domain — tag and all. Only VLAN-aware bridging can filter that, and the VSR 3.11
`interface bridge` model has no VLAN filtering (nor any VLAN match in its
firewall), so there is no VSR counterpart to a Linux `tc` guard. Forbidding the
mix gives identical, leak-free semantics on both flavors, at the cost that
**untagged frames and frames with an unlisted VLAN id on a trunk port are not
forwarded anywhere**.

**MTU.** `mtu` is what the attachment requests, and the CRA sizes the
sub-interfaces with it, so the workload sees the same MTU on every member. It
has to fit the domain, or frames would be black-holed above the bridge's own
MTU: an **access** port requires its Layer2 to carry at least the requested MTU,
a **trunk** requires at least one of its members to (the port is sized for its
largest domain; smaller ones are simply used below it). Routed attachments are
not constrained this way. An attachment that asks for more than its domains can
carry is refused like any other unresolvable one — the whole entry is dropped
and the reason logged. A tag costs 4 bytes on the wire on top of this, which the
fabric has to carry.

## vhost-user and the 6WIND device plugin

`transport: vhostuser` is driven end-to-end by the 6WIND HNA device plugin
(`nc-k8s-plugin.6wind.com/virtio-user`). See
[`docs/proposals/03-vhost-user/DEVICE-PLUGIN.md`](../../docs/proposals/03-vhost-user/DEVICE-PLUGIN.md)
for the full contract. Summary of what the plugin does for us:

1. The pod requests the resource in `resources.limits`; kubelet calls the
   plugin's `Allocate()`, which returns a randomly generated 10-character hex
   **device id** per allocated device and bind-mounts the corresponding socket
   directories into the container.
2. Multus matches the NAD's `k8s.v1.cni.cncf.io/resourceName` annotation against
   that allocation and injects `deviceID` into our CNI config — top-level, or in
   `runtimeConfig.deviceID` when the `deviceID` capability is enabled. We accept
   either, preferring whichever is non-empty.
3. The socket paths are **derived from the device id**, not configured:

   | side | path |
   | ---- | ---- |
   | host / CRA (VSR fast-path) | `/run/vsr-vhost-user/<deviceID>/socket` |
   | pod / workload             | `/run/vsr-virtio-user/<index>/socket` |

   The two trees are deliberately crossed and swap when the requested resource
   is `.../vhost-user` instead of `.../virtio-user`. The pod-side `<index>` is a
   per-`Allocate()` counter starting at `0`, which we cannot recompute, so we
   default to `0` and let the plugin's own device-info file correct it.
4. Multus best-effort copies the plugin's device-info file
   (`/var/run/k8s.cni.cncf.io/devinfo/dp/<resource>-<deviceID>-device.json`) to
   our `CNIDeviceInfoFile`. We read the staged copy, falling back to the `dp/`
   path, and take the pod-side socket path and the socket **mode** from it when
   present — the plugin is authoritative over anything the NAD claims. A missing
   or malformed file is tolerated and the derived defaults are used.

The plugin creates no network state: allocating a device only reserves an id and
a socket directory. Creating the `fpvhost` virtual-port on the VSR side is done
by this plugin plus the agent.

**No device id, no attachment.** If the transport is `vhostuser` and no device
id arrives by any of the above means, the ADD **fails**. Multus silently hands
out an empty `deviceID` once a pod has more attachments to a resource than it
requested devices, and a static socket path would silently produce a port that
is wired to nothing. `socketPath` therefore only *overrides the host-side path*
of an attachment that already has a device id; it can never substitute for one.

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

Both flavors use the same transport: the plugin calls the node-local agent, the
agent records the attachment on `NodeWorkloadPorts` and merges it into the
`NodeNetworkConfig` before rendering. Only the rendering differs.

- **cra-frr:** the agent programs the CRA-FRR netns via netlink
  (`pkg/nl/workloadports.go`): VRF enslavement, the on-link gateway addresses and
  the scope-link host routes; `l2` ports are enslaved to the `l2.<vlanID>`
  bridge. FRR redistributes connected/kernel/static, so the `/32` + `/128` are
  advertised. For the **underlay** path the FRR *default* instance must
  redistribute connected/kernel toward the fabric neighbors (and gain an IPv6
  unicast address-family). FRR **rejects** the `vhostuser` transport.
- **cra-vsr:** the VSR fast path owns the FIB, so the moved port cannot be
  programmed via raw netlink. The agent renders it as NETCONF instead: an
  `interface infrastructure <ifname>` with `port infra-<ifname>` + the on-link
  gateway addresses, plus interface-static routes
  (`ipv4-route/ipv6-route <ip> next-hop <ifname>`); a bridge `link-interface` for
  `l2`. Underlay (no-VRF) ports also get an explicit BGP `network` statement,
  since the default table's session has no VRF redistribution. The `vhostuser`
  transport renders an `fpvhost` fast-path virtual-port (`system fast-path
  virtual-port fpvhost fpvhost-<ifname> sockpath <host socket> sockmode
  <inverted>`) + `interface fpvhost <ifname> port fpvhost-<ifname>`. See
  `pkg/cra-vsr/workloadports.go` / `layer2.go` and `pkg/workloadcni` (transport).
- **cra-grout:** FRR (control plane) + the [grout](https://github.com/DPDK/grout)
  DPDK graph router (fast path). The agent renders a `grcli` batch applied by the
  `grout-cra` sidecar. A routed/L2 pod attach uses the `grouttap` transport
  (grout creates a `net_tap` in the CRA netns and the CNI moves it into the pod);
  a VM attach uses `vhostuser` (grout `net_vhost` port). The sidecar applies the
  desired-state batch **line-by-line, tolerating "exists" errors**, so a second
  pod's reconcile re-applying the first pod's ports is idempotent and still
  creates the new tap. See `pkg/cra-grout/` and `cmd/grout-cra`.

### Transport

```
CNI ADD/DEL --gRPC(unix)--> agent --> NodeWorkloadPorts CR (durable)
                                   \-> merge into NodeNetworkConfig
                                       --> netlink (FRR) | NETCONF (VSR)
```

The agent serves the socket at `/run/das-schiff/workload-cni.sock` (a hostPath
shared with the plugin, which runs in the host mount namespace). Both the socket
and its directory are root-only (`0600` / `0700`) so no unprivileged local
process can alter routing state. A change to
`NodeWorkloadPorts` triggers a reconcile even when the `NodeNetworkConfig`
revision is unchanged (the merged workload ports are tracked by a content hash),
so attachments are (de)provisioned promptly.

## Build & install

```sh
make build-cni-workload          # host build of bin/cni-workload (Linux)
make docker-build              # also builds das-schiff-nwop-cni-workload:latest
make kind-load                 # loads images into the kind cluster
kubectl apply -k config/cni-workload                     # install plugin on nodes
```

The installer DaemonSet copies the binary to `/opt/cni/bin`. The per-network CNI
config travels with the NAD, so no standalone conflist is required.

## E2E coverage

The lab installs the plugin on every node (`PhaseWorkloadCNI`), so the datapath
is exercised from plain pods as well as from VMs:

| Test | Label | Covers |
| --- | --- | --- |
| `e2etests/tests/intent_workload_cni.go` | `intent`, `workloadcni` | L2 access into VLAN 501, an 802.1Q trunk carrying VLAN 501 plus VLAN 502 translated to id 200, and host-local IPAM allocation/release |
| `e2etests/tests/routed_kubevirt.go` | `kubevirt`, `routed` | routed mode end to end: the VM /32 and /128 in the underlay BGP and reachability from the fabric |

The pod tests are intent-labelled because an L2 attachment resolves a
`Layer2Attachment` by name: the domain it binds to only exists once the intent
pipeline has stamped it onto the `NodeNetworkConfig`. Run them with
`make e2e-test-intent` and the VM test with `make e2e-test-kubevirt`.
