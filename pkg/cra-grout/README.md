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
 │  routed-cni gRPC server   │        │                                         │
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
  wired with `RoutedPortsSource` and `RestoreOnReconcileFailure: false` (grout
  owns its FIB, like VSR), a controller that `Watches(NodeRoutedPorts)`, and a
  `main` that starts the routed-cni gRPC server as a manager runnable.
- **`das-schiff-cra-grout.Dockerfile`** — the CRA image: grout + FRR (with
  `dplane_grout.so`) + grcli + grout-cra, systemd-init (mirrors
  `das-schiff-cra-frr.Dockerfile`).
- **`das-schiff-nwop-agent-cra-grout.Dockerfile`**, **`config/agent-cra-grout/`**
  — the agent image + DaemonSet overlay (selectable flavor, like `agent-cra-vsr`;
  not part of `config/default`).

## grcli grammar used

Validated live (see the grout PoC results); rendered by `pkg/cra-grout`:

```
interface add vrf <name>
interface add port <name> devargs net_tap0,iface=<kif> vrf <vrf>       # pod / uplink
interface add port <name> devargs net_vhost0,iface=<sock>,client=1 vrf <vrf>  # VM
address add <cidr> iface <name>
nexthop add l3 iface <name> id <id> address <nh-ip>
route add <prefix> via id <id> vrf <vrf>
# EVPN L3VNI:
interface add vxlan <n> vni <N> local <vtep> vrf <t>
# EVPN L2VNI:
interface add bridge <br> vrf <t>
interface add vxlan <n> vni <N> local <vtep> domain <br>
interface add port <name> devargs ... domain <br>                      # L2 attach
```

## Datapath decisions

- **API transport enum is unchanged** (`RoutedPort.Transport ∈ {veth, vhostuser}`).
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
veth, so the routed-cni `Add` now returns the tap netdev name (`AddResponse.
tap_name`): the CNI records the attachment (→ `NodeRoutedPorts` → reconcile →
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
- grout is young (v0.x): the FRR-programmed EVPN IRB path needs live validation
  before any production commitment.

