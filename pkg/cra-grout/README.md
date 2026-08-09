# cra-grout — DPDK graph-router CRA flavor

`cra-grout` is a third CRA (Container Routing Appliance) datapath flavor for the
`das-schiff-network-operator`, alongside:

| Flavor      | Control plane | Fast path / FIB owner        | Per-VM attach                | Pod attach            |
| ----------- | ------------- | ---------------------------- | ---------------------------- | --------------------- |
| `cra-frr`   | FRR (BGP/EVPN)| Linux kernel (netlink)       | veth (bridge binding)        | veth moved into CRA   |
| `cra-vsr`   | FRR (BGP/EVPN)| 6WIND VSR (closed, NETCONF)  | `fpvhost` virtual-port       | `infrastructure` port |
| **`cra-grout`** | **FRR (BGP/EVPN)** | **grout DPDK graph router** ([github.com/DPDK/grout](https://github.com/DPDK/grout)) | **`net_vhost` port** | **`net_tap` moved into pod** |

grout is an open-source DPDK fast-path router. It ships an **FRR zebra dataplane
plugin** (`dplane_grout.so`), so FRR owns the control plane (BGP, EVPN
`advertise-all-vni`, AS-path prepend for live migration) exactly as in `cra-frr`,
while grout owns the forwarding FIB exactly as VSR does in `cra-vsr`. cra-grout is
therefore the **open-source DPDK analog of cra-vsr**.

## Components

```
 k8s agent pod (hostNetwork)          CRA container (in the `cra` netns)
 ┌───────────────────────────┐        ┌───────────────────────────────────────┐
 │ agent-cra-grout           │  mTLS  │ grout-cra sidecar                       │
 │  reconciler (ConfigApplier)├───────▶│  POST /grout/configuration             │
 │  renders:                 │  HTTP  │   ├─ writes /etc/frr/frr.conf + reload  │
 │   • FRR config (crafrr)   │        │   └─ grcli -ef <batch>  ──▶ grout        │
 │   • grcli batch (cra-grout)│        │  POST /grout/command  (ad-hoc grcli)    │
 │  workload-cni gRPC server   │        │                                         │
 └──────────▲────────────────┘        │  FRR (bgpd/zebra -M dplane_grout) ──▶ grout FIB
            │ gRPC (unix sock)         │  grout daemon (DPDK, `-t` test-mode)    │
   routed CNI plugin (host)            └───────────────────────────────────────┘
```

- **`pkg/cra-grout`** — the flavor library:
  - `render.go` `RenderGrcli(baseConfig, spec)` renders the node's desired grout
    fast-path state (VRFs, EVPN L3VNI/L2VNI VXLANs, L2 bridge domains + attached
    ports, routed workload ports + on-link host routes) as a **grcli batch**.
  - `grcli.go` `Batch` — the grcli batch builder (allocates `net_tapN`/`net_vhostN`
    PMD indices and nexthop ids).
  - `manager.go` `Manager` — an mTLS HTTP client that POSTs the FRR config +
    grcli batch to the grout-cra sidecar (mirrors `pkg/cra-frr/manager.go`).
  - FRR config is rendered by reusing `pkg/cra-frr` (`FRRTemplate`) — cra-grout
    does not fork the FRR template.
- **`cmd/grout-cra`** — the CRA-netns sidecar (mirrors `cmd/frr-cra`): mTLS HTTP
  endpoints `/grout/configuration` (apply grcli batch via `grcli -ef` + reload
  FRR), `/grout/command`, `/grout/metrics`.
- **`cmd/agent-cra-grout`**, **`controllers/agent-cra-grout`**,
  **`pkg/reconciler/agent-cra-grout`** — the node agent: a `common.ConfigApplier`
  wired with `WorkloadPortsSource` and `RestoreOnReconcileFailure: false` (grout
  owns its FIB, like VSR), a controller that `Watches(NodeWorkloadPorts)`, and a
  `main` that starts the workload-cni gRPC server as a manager runnable.
- **`das-schiff-cra-grout.Dockerfile`** — the CRA image: grout + FRR (with
  `dplane_grout.so`) + grcli + grout-cra, systemd-init (mirrors
  `das-schiff-cra-frr.Dockerfile`).
- **`das-schiff-nwop-agent-cra-grout.Dockerfile`**, **`config/agent-cra-grout/`**
  — the agent image + DaemonSet overlay (selectable flavor, like `agent-cra-vsr`;
  not part of `config/default`).

## grcli grammar used

Validated live (see the grout PoC results); rendered by `pkg/cra-grout`:

```
interface add vrf <name> rib4-routes <n> fib4-tbl8 <n> rib6-routes <n> fib6-tbl8 <n>
interface add port <name> devargs net_tap0,iface=<kif> vrf <vrf>       # pod / uplink
interface add port <name> devargs net_vhost0,iface=<sock>,client=1 vrf <vrf>  # VM
address add <cidr> iface <name>
nexthop add l3 iface <name> id <id> address <nh-ip>
route add <prefix> via id <id> vrf <vrf>
# EVPN L3VNI:
interface add vxlan <n> vni <N> local <vtep> vrf <t> mtu <mtu>
# EVPN L2VNI:
interface add bridge <br> vrf <t> mtu <mtu>
interface add vxlan <n> vni <N> local <vtep> domain <br> mtu <mtu>
interface add port <name> devargs ... domain <br>                      # L2 attach
# 802.1Q trunk attach:
interface add port <name> devargs ... mtu <mtu>                       # unbound parent
interface add vlan <name>.<vid> parent <name> vlan_id <vid> domain <br> mtu <mtu>
```

### 802.1Q trunks

A trunked workload port carries several bridge domains over one netdev, tagged.
grout models that as one **unbound** parent port plus one `vlan` sub-interface
per member, each bound to its own bridge domain.

The parent is deliberately left in VRF mode rather than bound to a domain: grout
only demuxes a VLAN tag when the receiving interface is in VRF mode
(`iface_input.c` gates the sub-interface lookup on `mode == GR_IFACE_MODE_VRF`).
Binding the parent to a `domain` would put it in bridge mode, and every tagged
frame would land in that single domain with the sub-interfaces never receiving
anything.

Two consequences worth knowing:

- **Deletion order matters.** `interface del` does *not* cascade: destroying a
  port that still has sub-interfaces fails with `EBUSY`. Pruning therefore
  removes VLAN sub-interfaces before their parent port, otherwise the failure is
  permanent — every later reconcile would retry the same impossible delete.
- **The parent's MTU is the maximum of its members'**, since a sub-interface
  cannot carry more than its parent. Members keep their own requested MTU.

Tag translation is supported: a member whose VLAN id differs from the bridge
domain's own id is simply a `vlan` sub-interface with the member's id attached to
that domain, so grout rewrites the tag on the way through.

## Datapath decisions

- **API transport enum is unchanged** (`WorkloadPort.Transport ∈ {veth, vhostuser}`).
  The grout agent maps `veth → net_tap` and `vhostuser → net_vhost`; the 6WIND
  socket-mode inversion (pod `server` ⇒ backend `client`) is preserved
  (`groutIsClient`).
- **VM attach = `net_vhost`** (reuse `pkg/cni/vhostuser.go` + device-info + the
  KubeVirt hook, unchanged).
- **Pod attach = grout `net_tap` moved into the pod netns.** grout cannot adopt a
  moved-in kernel veth (`net_af_packet`/`af_xdp`/`memif` are not compiled in the
  edge image), so the CNI, when `flavor=grout`, moves the grout tap netdev into
  the pod netns instead of creating a veth pair. This is the same tap-move proven
  for the `hbn` uplink, with destination = pod netns.
- **grout must run in its own netns.** The edge image's
  `GROUT_OVERRIDE_DEFAULT_ROUTE` clobbers the host default route when run with
  `--network host`; the CRA container therefore runs in the dedicated `cra` netns.

### Synchronous tap handoff (implemented — `transport: grouttap`)

For a grout pod the tap must exist in the CRA netns *before* CNI `ADD` returns
(so KubeVirt/the pod sees its interface). grout cannot adopt a moved-in kernel
veth, so the workload-cni `Add` now returns the tap netdev name (`AddResponse.
tap_name`): the CNI records the attachment (→ `NodeWorkloadPorts` → reconcile →
grout `net_tap` created in the CRA netns), then **polls the CRA netns until the
tap appears**, moves the netdev CRA→pod, renames it, addresses it and installs
the on-link default (unless L2). `Del` removes the grout port. The grout-cra
sidecar applies its grcli batch **line-by-line, tolerating "exists"**, so a
second pod's reconcile re-applying existing ports stays idempotent and still
creates the new tap. See `pkg/cni/grouttap.go`, `pkg/cni/config.go`
(`transport: grouttap`), and `cmd/grout-cra/main.go`.

## Live migration + prepend

Identical to `cra-frr`/`cra-vsr`: convergence and AS-path prepend are FRR/BGP
concerns. The per-VM `/32`(+`/128`) is added to grout and redistributed by FRR
into the **underlay** (not the EVPN overlay). During migration the old node
prepends its own AS so traffic still arriving there is de-preferred while the new
node advertises; the route is withdrawn on `DEL`. grout only forwards.

## Node prep

- **Lab / MVP:** `grout -t` (test-mode, no hugepages) + `net_tap` ports — runs on
  a plain node / inside the `cra` netns with no host DPDK tuning
  (`docker/grout.env` defaults to `ARGS="-t"`).
- **Production:** prepare hugepages, `vfio-pci` uplink binding, IOMMU and isolated
  CPUs, then clear grout's `-t` flag. The `hbn` fabric uplink becomes a `vfio-pci`
  PCIe NIC bound as a grout `port`. Use `docker/grout-node-prep.sh` (idempotent):

  ```sh
  grout-node-prep.sh hugepages --size 1G --count 8   # reserve + mount hugetlbfs
  grout-node-prep.sh iommu                            # GRUB intel/amd_iommu (reboot)
  grout-node-prep.sh isolate 4-15                     # isolcpus for PMD (reboot)
  grout-node-prep.sh bind 0000:03:00.0 0000:03:00.1   # uplinks -> vfio-pci
  # then clear -t: edit /etc/default/grout -> ARGS=""
  ```

  `bind` also records the PCI addresses to `/etc/cra/uplinks` and sets
  `/etc/cra/uplink-mode=vfio`, which the grout node-setup `cra-start.sh`
  (`e2e/images/kind-node-grout/`) reads to bind each uplink as a grout DPDK port.
  The lab path keeps `uplink-mode=tap` (net_tap uplinks) and needs none of this.

## Not supported

grout is a DPDK fast path, and the other CRA flavors implement several features
by putting rules on **kernel** interfaces inside the CRA netns. On grout those
packets never reach the kernel, so unless grout has an equivalent of its own the
feature is simply unavailable. The gaps below are known and deliberate; the e2e
suites that cover them are skipped on this flavor
(`framework.SkipIfGrout`) rather than left failing.

| Feature | State on `cra-grout` | Why |
| ------- | -------------------- | --- |
| **Traffic mirroring** (`MirrorSelector`/`MirrorTarget`, NNC `MirrorACLs`) | **Not supported** — `mirror` and `intent`+`mirror` e2e suites skipped | `cra-frr` mirrors with `tc` filters and a GRE tunnel on the CRA's kernel interfaces (`pkg/nl/mirror.go`), `cra-vsr` uses the VSR's own mirroring. grout has no mirroring, ACL or classifier module at all, so there is nothing to render a `MirrorACL` into. |
| **ARP / ND (neighbor) suppression** on stretched L2VNI segments | **Not supported** — segments work, but ARP/ND floods the fabric | `cra-frr` suppresses on the VXLAN device and answers locally from the EVPN MAC/IP table (`pkg/neighborsync`). grout's bridge has no suppression knob and does not consume EVPN type-2 MAC/IP bindings for local reply, so every ARP/ND request is flooded as BUM traffic instead. Correct, just noisier — and the noise grows with the number of endpoints on a segment. |
| **CRA-side IPv6 NAT** | **Not supported** | grout's policy module implements `snat44`/`dnat44` only; there is no IPv6 counterpart. This costs nothing today because egress NAT is done node-side, but a CRA-side NAT66 has no backend. |
| **Packet filtering / ACLs / QoS** | **Not supported** | grout has no firewall, policer or shaper module, so there is no backend for any CRA-side filtering or rate limiting. |
| **Kernel-visible datapath state** | **By design** | `dplane_grout` disables zebra's kernel namespace: the node's kernel FIB carries no BGP routes and `ip`/`tcpdump` on the CRA show nothing about forwarded traffic. Use `grcli` (`route show`, `nexthop show`, `interface show`) instead. Anything that inspects or manipulates the CRA's kernel networking has to be rewritten against `grcli`. |
| **Remote VTEPs advertising only IPv6 prefixes** | **Fixed upstream** — needs a grout image carrying the fix (`quay.io/grout/grout:edge`, not the v0.16.3 tag) | `dplane_grout` cached an EVPN next-hop's RMAC keyed on (vrf, address) *including the address type*, storing it exactly as zebra handed it over, but normalised an IPv4-mapped gateway (`::ffff:a.b.c.d`) back to plain IPv4 before looking it up when building the L3 next-hop. A VTEP zebra only ever presents in the mapped form -- which is what a device advertising nothing but IPv6 prefixes produces -- was therefore cached under a key the lookup never used: its next-hop stayed unresolved on the SVI instead of becoming `flags=static remote` on the VXLAN port, and its share of an ECMP pair silently blackholed. Found while bringing this flavor up against grout 0.16.3 with FRR 10.6.1, fixed upstream by normalising both sides of the lookup, and no longer reproducible on `:edge`. Listed here because it is the reason this flavor tracks `:edge` rather than a release tag: on v0.16.3 an IPv6-only VTEP still blackholes, and the only workaround is to make every VTEP advertise at least one IPv4 prefix into the VRF. |
| **Restart without traffic loss** | **Not supported** | restarting grout empties the FIB *and* the interface table: ports, uplinks and VRFs are gone, and nothing rebuilds them, so the node has to re-run its CRA bootstrap. FRR reprograms only what it owns. There is no graceful-restart equivalent for the fast path. Restarting *FRR* is fine — verified against grout 0.16.3 with FRR 10.6.1, it neither drops the L3VNIs nor removes grout FIB entries programmed out-of-band with `grcli`, so the sidecar hot-reloads FRR on a config change and never restarts it. |
| **More than one *routed* workload-CNI attachment per node** | **Supported** — IPv4 is routed over the IPv6 next-hop instead | The routed workload-CNI design gives *every* workload port the same on-link gateway pair, `169.254.1.1/32` plus `fe80::1/128`, and reaches the workload over host routes. grout keeps a single node-global IPv4 address table with no per-interface scope for link-local space, so the second port to ask for `169.254.1.1/32` was rejected with `EADDRINUSE` — while the IPv6 half is accepted on every port, since link-local IPv6 *is* scoped per interface. That capped a node at one routed attachment, and not gracefully: a rejected `grcli` line aborts the rest of the batch and invalidates the revision cluster-wide (see below). This flavor therefore never configures the IPv4 gateway at all. Only `fe80::1/128` is programmed, and the CNI gives the pod an IPv4 default route with that address as its next-hop (`RTA_VIA`, the RFC 5549 forwarding case — `ip -4 route add default via inet6 fe80::1 dev net1`), which needs no IPv4 address on the CRA side. `cra-frr` and `cra-vsr` keep the IPv4 gateway; the kernel and the VSR both allow the duplicate. |

A failed `grcli` command is also more expensive than it looks, which is worth
knowing when a limitation above is hit at runtime: grout applies a batch
transactionally-ish but aborts at the first error, so everything *after* the
failing command is silently skipped. The agent then marks the whole
NodeNetworkConfig invalid, which invalidates the NetworkConfigRevision — and
because a revision is named after a hash of its content, regenerating the same
config reproduces the same already-invalid revision. Config deployment for the
**whole cluster** stops there, nothing prunes any more, and retries keep leaking
grout ports that hold on to the very resource that caused the conflict. Recovery
is manual. Not a grout bug as such, but the blast radius of one rejected address
is a cluster, not a port.

Beyond the datapath, grout needs hugepages, a DPDK-capable interface and a
privileged CRA, so the flavor is not a drop-in for clusters that cannot give the
CRA those. And grout is a v0.x project: the FRR-programmed EVPN IRB path needs
live validation before any production commitment.

## Status / caveats

- Control plane, manager, agent wiring, images, manifests and RBAC (Phase A) are
  implemented; the grcli renderer (routed + EVPN L3VNI/L2VNI + vhost-user) is unit
  tested (`render_test.go`).
- **Live-validated (grout 0.16.2, GCP, bare `grcli` harness):** every line the
  renderer emits is accepted by grout — VRFs, L3VNI VXLAN (no SVI address; pure L3
  transit), L2VNI bridge + VXLAN, L2-attach member `net_tap`, routed `net_tap`
  port, `net_vhost` ports (both socket modes), IPv4+IPv6 addresses/nexthops/routes
  (the `/32`+`/128` routed FIB is programmed). The **grout→pod tap handoff** was
  proven: a grout-created `net_tap` netdev was moved CRA-netns→pod-netns, renamed,
  addressed and routed.
- The IRB anycast-gateway IP is placed on the **L2VNI bridge SVI** (bound to the
  tenant VRF); the **L3VNI carries no address**. In the bare harness (no FRR, no
  real underlay/VTEP), addressing the SVI on a bridge in a tenant VRF returned
  `ENONET` while it succeeded in the underlay VRF — most likely a harness artifact
  (the tenant VRF came up with a degraded route table), not a grout blocker. To be
  re-confirmed with FRR (`dplane_grout`) driving the IRB over a real underlay.
- The CRA image assembly (exact `dplane_grout.so` path, patched-FRR build), the
  grout-tap CNI datapath + synchronous handoff, node-setup scripts, and the e2e
  hook still require live validation on a real DPDK-capable host.
- **BGP unnumbered works** (`neighbor <iface> interface remote-as ...`), so the
  grout flavor peers with the fabric exactly like `cra-frr` and needs no numbered
  eBGP fallback. It required two upstream fixes, both merged in
  [DPDK/grout#658](https://github.com/DPDK/grout/pull/658) and therefore present
  in `quay.io/grout/grout:edge` but **not** in the v0.16.3 tag: grout now punts
  received IPv6 Router Advertisements to the control plane (it dropped them as
  unsupported before, so FRR never learned the peer link-local next-hop and the
  session stayed `Idle` -- [DPDK/grout#657](https://github.com/DPDK/grout/issues/657)),
  and grout's FRR build carries a patch for the zebra `kernel_neigh_update` path,
  which bypasses the dataplane plugin and went straight to netlink. The CRA image
  must therefore stay on a grout image newer than v0.16.3.
- grout is young (v0.x): the FRR-programmed EVPN IRB path needs live validation
  before any production commitment.

