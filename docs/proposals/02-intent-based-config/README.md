# Proposal 02 — Cluster-Local Intent-Driven Network Configuration

- **Status:** Draft
- **Date:** 2026-02-09
- **Authors:** das-schiff network-operator team

## 1. Summary

This proposal describes how to introduce **intent-driven custom resources** for cluster networking in the das-schiff network operator. Instead of requiring operators to understand and manually compose low-level HBR configuration (`Layer2NetworkConfiguration`, `VRFRouteConfiguration`, `BGPPeering`, `MirrorSelector`, `MirrorTarget`), tenants will express their desired network state through high-level intent resources. A set of new controllers translates these intents into the operator's configuration pipeline and manages ancillary platform components (load balancers, egress NAT, CNI pools, traffic mirroring).

Ingress and egress connectivity are modeled as separate CRDs (`Inbound` and `Outbound`), mirroring the natural separation already established in the SchiffCluster API and the schiff CLI. Network pool definitions are separated from pool usage via a `Network` CRD — usage CRDs reference it by name via `networkRef`, keeping pool definition and consumption cleanly decoupled. The intent CRDs support both **HBN** (Host-Based Networking — VXLAN/VRF) and **non-HBN** (physical interfaces, SR-IOV, MetalLB-only) deployment modes.

In this first iteration, all network parameters (VLAN IDs, VNIs, CIDRs) are **specified directly** by the user in `Network` CRDs. Automatic network allocation via a management cluster controller is explicitly out of scope and can be added later as a transparent enhancement.

The goal is to let T-CaaS tenants — with support from service integration — configure networking for their clusters independently, reducing ops overhead while keeping the proven revision-based, node-by-node rollout model intact.

## 2. Current State

### 2.1 Existing CRDs (Low-Level Configuration)

The operator currently provides three cluster-scoped configuration CRDs that directly describe *how* the network stack is configured:

| Resource | Scope | File | Purpose |
|---|---|---|---|
| `Layer2NetworkConfiguration` | Cluster | `api/v1alpha1/layer2networkconfiguration_types.go` | L2/VLAN bridge, VXLAN, IRB, anycast gateway |
| `VRFRouteConfiguration` | Cluster | `api/v1alpha1/vrfrouteconfiguration_types.go` | VRF import/export, route targets, VNI, SBR |
| `BGPPeering` | Cluster | `api/v1alpha1/bgppeering_types.go` | BGP sessions, filters, BFD |
| `NetworkConfigRevision` | Cluster | `api/v1alpha1/networkconfigrevision_types.go` | Snapshot of L2 + VRF + BGP config per revision |
| `NodeNetworkConfig` | Cluster | `api/v1alpha1/nodenetworkconfig_types.go` | Per-node resolved network config consumed by agents |
| `MirrorTarget` | Cluster | `api/v1alpha1/mirrortarget_types.go` | GRE collector endpoint for mirrored traffic (see [Proposal 01](../01-traffic-mirroring/README.md)) |
| `MirrorSelector` | Cluster | `api/v1alpha1/mirrorselector_types.go` | Traffic match rules bound to a source L2/VRF and a MirrorTarget |

### 2.2 Existing Reconciliation Pipeline

```
 Layer2NetworkConfiguration ─┐
 VRFRouteConfiguration ──────┼──▶ ConfigReconciler ──▶ NetworkConfigRevision
 BGPPeering ─────────────────┘                              │
                                                            ▼
                                              RevisionReconciler
                                                            │
                                                            ▼
                                              NodeNetworkConfig (per node)
                                                            │
                                                            ▼
                                              CRA Agent (per node)
```

The `ConfigReconciler` watches all three CRDs, snapshots them into a `NetworkConfigRevision`, and the `RevisionReconciler` builds per-node `NodeNetworkConfig` objects that are consumed by the CRA agents (CRA-FRR, CRA-VSR, etc.).

### 2.3 Gaps

1. **No intent-based interface** — users must understand the full low-level config model (VNIs, route targets, prefix lists, etc.) to configure even simple use cases.
2. **No integrated load balancer or egress configuration** — VRF connectivity requires separate manual setup of MetalLB, Coil Egress, Calico pools, etc.
3. **No SR-IOV orchestration** — SR-IOV / VTEP_LEAF configuration on BM4X requires manual coordination.
4. **No simplified traffic mirroring** — configuring mirroring requires coordinating `MirrorSelector`, `MirrorTarget`, a dedicated mirror VRF (`VRFRouteConfiguration` with loopbacks + IPAM pool), and GRE tunnel parameters across multiple resources (see [Proposal 01](../01-traffic-mirroring/README.md)).

> **Explicitly out of scope for this iteration:** automatic network allocation (BM4X / OpenStack / vSphere), DNS integration, and management-cluster cross-cluster reconciliation. Users specify all network parameters (VLAN IDs, VNIs, CIDRs) directly in `Network` resources.

> **Infrastructure-level provisioning is out of scope.** The operator **consumes** pre-existing host infrastructure — it does not create or manage it:
> - **Bond creation** (e.g., creating `bond2` from `ens5f0np0` + `ens5f1np1`) is handled by external node-level tooling (e.g., `NetworkConfiguration` CRDs from the CaaS platform, cloud-init, netplan, or systemd-networkd). The operator references existing bonds via `Layer2Attachment.interfaceRef`.
> - **SR-IOV VF provisioning** (e.g., setting VF count on physical NICs) is handled by the SR-IOV Network Device Plugin, the node's BIOS/OS configuration, or platform-specific tooling. The operator **uses** VFs via `Layer2Attachment.sriov.enabled`.
> - This separation is intentional: bond and VF lifecycle is a node-infrastructure concern that varies per platform and is typically managed through GitOps or machine configuration, not by the network operator.

## 3. Design Decisions

### 3.1 New Intent-Driven CRDs

We introduce eleven new cluster-scoped CRDs under `network.t-caas.telekom.com/v1alpha1`:

| CRD | Purpose |
|---|---|
| `VRF` | Backbone VRF metadata — name, VNI, route target, loopbacks. Defined once, referenced by `Destination` |
| `Network` | Pool definition — CIDR, VLAN, VNI, allocation pool. Referenced by name from usage CRDs |
| `Destination` | Routing target — prefixes + either `vrfRef` (VRF import) or `nextHop` (static route). Referenced by label from attachments |
| `Layer2Attachment` | Attach a `Network` as L2 segment to nodes; supports HBN and non-HBN (physical/SR-IOV interfaces) |
| `Inbound` | Allocate IPs from a `Network` for MetalLB pools + BGP/L2 advertisement; works with or without HBN |
| `Outbound` | Allocate IPs from a `Network` for Coil NAT + Calico pools; works with or without HBN |
| `BGPNeighbor` | Allow workload BGP route advertisement (references `Inbound` connections) |
| `PodNetwork` | Allocate additional pod-level networks from a `Network` (CNI integration) |
| `Collector` | GRE collector endpoint + mirror VRF binding — defined once, referenced by TrafficMirrors |
| `TrafficMirror` | Declaratively mirror traffic from an attachment to a Collector |
| `NodeNetworkStatus` | Per-node inventory: interfaces (excl. pod veths), routes, IPs — populated by agents for validation and observability |

The design separates **pool definition** (`Network`) from **pool usage** (`Layer2Attachment`, `Inbound`, `Outbound`, `PodNetwork`). A `Network` is not per se L2 — it only becomes a Layer 2 segment when a `Layer2Attachment` attaches it to nodes. An `Inbound` or `Outbound` allocates IPs from the same `Network` pool without implying any L2 presence.

Ingress and egress connectivity were originally combined in a single `VRFAttachment` CRD with an inline `connections[]` list. We split them into separate `Inbound` and `Outbound` CRDs because:

1. **Ingress and egress are already separate concepts** — the SchiffCluster API (`network.ingress[]`, `network.egress[]`) and schiff CLI treat them independently.
2. **Different lifecycle** — inbound (MetalLB pool + advertisement) and outbound (Coil NAT, egressPolicy) are managed by different platform components with different configuration models.
3. **Non-HBN support** — an inbound connection without HBN is just a MetalLB pool + BGP/L2 advertisement (no VRF). Combining this with VRF-aware egress in one CRD would be awkward.
4. **Simplicity** — users configure one CR per concern rather than a single large CR with an inline connection list.

These CRDs describe **what** the user wants, not **how** to configure it. Crucially, `Network` separates pool definition from usage, `VRF` centralises backbone VRF metadata, and `Destination` decouples routing targets from the resources that need them — see §3.5.

### 3.2 Controller Architecture — Tenant Cluster Only

All intent controllers run in the **tenant cluster**, integrated into the existing network-operator. There is no management cluster component in this iteration.

```
┌─────────────────────────────────────────────────────────────────────┐
│                     Tenant Cluster                                  │
│                                                                     │
│  Network ───────────┐                                               │
│  Layer2Attachment ──┤                                               │
│  Inbound ───────────┤  network-intent-controller(s)                │
│  Outbound ──────────┤  ┌──────────────────────────────────┐        │
│  BGPNeighbor ───────┤  │ • Resolve networkRef → Network   │        │
│  PodNetwork ────────┘  │ • Resolve destinations → VRFs    │        │
│                        │ • Apply nodeSelector for scope   │        │
│                        │ • Configure platform components  │        │
│                        │   (MetalLB, Coil, Calico, etc.)  │        │
│                        │ • Support HBN and non-HBN modes  │        │
│                        │ • Non-HBN: MetalLB/advertisement │        │
│                        │   only (no VRF plumbing)         │        │
│                        └────────┬─────────────────────────┘        │
│                                 │                                   │
│                                 ▼                                   │
│                ┌────────────────────────────────────────┐           │
│                │ Integration with revision pipeline     │           │
│                │ (see §3.4 — open design question)      │           │
│                └────────┬───────────────────────────────┘           │
│                         │                                           │
│                         ▼                                           │
│                   NetworkConfigRevision → NodeNetworkConfig → CRA   │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

All network parameters (VLAN IDs, VNIs, subnets, IP addresses) are specified directly in the `Network` CRDs by the user. Usage CRDs (`Layer2Attachment`, `Inbound`, `Outbound`, `PodNetwork`) reference a `Network` by name via `networkRef`. Automatic allocation through a management cluster controller can be added later as a transparent layer on top — the `Network` CRD API is designed to accommodate this (`allocationPool` fields are reserved but not processed in this iteration).

### 3.3 Use-Case Coverage

The design targets four categories of deployment. The table below maps each to the CRDs and fields involved.

| # | Use Case | CRDs / Fields | Notes |
|---|---|---|---|
| **UC 1** | **L2 ordering on the fly** — vSphere / OpenStack controller orders a network, attaches it to VMs, optionally assigns IPs | `Network` (with `allocationPool` for future auto-allocation, or user-specified CIDR/VLAN). Pure L2 (VLAN-only, no IPs) is valid. `Layer2Attachment` with `interfaceRef` + `nodeSelector` for L2 presence on nodes | A future management-cluster controller uses `allocationPool` to order from BM4X / vSphere / OpenStack and writes back CIDR + VLAN. "With IP assignment or not" is handled by the optional `ipv4`/`ipv6` fields on `Network` |
| **UC 2** | **GitOps L2 configs** — bonds, VLANs on bonds (no IPs), SR-IOV VF provisioning | **Bond creation** and **VF provisioning** are out of scope (infrastructure-level; see note above). The operator *consumes* them: `Network` (VLAN-only, no IPs) + `Layer2Attachment` with `interfaceRef: bond2` + `nodeSelector` creates the VLAN sub-interface on the existing bond. Per-VLAN: one `Network` + one `Layer2Attachment` per VLAN | Example: 10 VLANs on `bond2` = 10 `Network` resources (each with `vlan: <id>`) + 10 `Layer2Attachment` resources (each with `networkRef`, `interfaceRef: bond2`, `nodeSelector`) |
| **UC 3** | **SR-IOV ordering** — BM4X orders a network, VLAN gets assigned, worker pool needs config, bond already exists | `Network` (with `vlan` from BM4X, optionally `allocationPool`). `Layer2Attachment` with `interfaceRef: bond0` + `sriov.enabled: true` + `nodeSelector`. Bond is pre-existing | `sriov.enabled` creates SR-IOV policies for `NetworkAttachmentDefinition` with the VLAN ID from the referenced `Network` |
| **UC 4** | **HBN use cases** — VXLAN tunnels, VRF routing, anycast gateways, MetalLB / Coil integration | `Network` (with CIDR + VLAN + VNI) + `VRF` (metadata) + `Destination` (routing, `vrfRef`). `Layer2Attachment` with `destinations` selector + `networkRef`. `Inbound`/`Outbound` for MetalLB/Coil integration. `BGPNeighbor`, `PodNetwork`, `Collector`, `TrafficMirror` for advanced use cases | This is the proposal's primary design centre — fully specified in §4–§6 |

**Non-HBN / pure L2 example** (mapping UC 2 — one of the 10 VLANs from the GitOps configs):

```yaml
# Pool: pure L2 segment, VLAN only, no IP
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Network
metadata:
  name: vlan1520
spec:
  vlan: 1520
---
# Attach VLAN 1520 to bond2 on all worker nodes — L2 bridge only, no VRF
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Layer2Attachment
metadata:
  name: vlan1520
spec:
  networkRef: vlan1520
  interfaceRef: bond2
  mtu: 9000
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
  # no destinations — pure L2, no VRF plumbing
```

### 3.4 Integration with the Revision Pipeline — Resolved (Option A)

A key architectural decision is **how the intent CRDs feed into the existing revision pipeline**. There are two viable approaches:

#### Option A — Intent → Existing Low-Level CRDs → Revision

The intent controllers produce standard `Layer2NetworkConfiguration`, `VRFRouteConfiguration`, and `BGPPeering` resources. The existing `ConfigReconciler` picks these up and creates revisions as usual.

```
 Layer2Attachment ──▶ generates ──▶ Layer2NetworkConfiguration ─┐
 Inbound ──────────▶ generates ──▶ VRFRouteConfiguration ───────┤
 Outbound ─────────▶ generates ──▶    + MetalLB / Coil / etc.  ├──▶ ConfigReconciler ──▶ Revision
 BGPNeighbor ──────▶ generates ──▶ BGPPeering ──────────────────┘
                                  (+ user-created low-level CRDs)
```

| Pro | Con |
|---|---|
| Zero changes to `ConfigReconciler`, `NetworkConfigRevisionSpec`, or the rollout mechanism | Two layers of CRDs — intent resources generate low-level resources that the user could also create directly, making ownership and conflict resolution complex |
| Users can still create low-level resources directly (escape hatch) | Debugging requires tracing through two levels (intent → generated resource → revision → node config) |
| Generated low-level resources can be inspected for verification | Generated low-level resources are an implementation detail that users may accidentally modify, causing drift |
| Clean separation of concerns — intent controller and revision controller remain independent | Intent controller needs to carefully manage owner references and garbage collection of generated resources |

#### Option B — Intent CRDs + Existing Low-Level CRDs → Revision Directly

The `ConfigReconciler` is extended to **also watch** the intent CRDs. At revision time, it reads both the existing low-level CRDs (for backward compatibility and escape hatch) and the intent CRDs, merging them into a single `NetworkConfigRevision`. No intermediate low-level resources are generated.

```
 Layer2Attachment ───────────────────┐
 Inbound ───────────────────────────┤
 Outbound ──────────────────────────┤
 BGPNeighbor ───────────────────────┤
                                    ├──▶ ConfigReconciler ──▶ Revision
 Layer2NetworkConfiguration ────────┤
 VRFRouteConfiguration ─────────────┤
 BGPPeering ────────────────────────┘
```

| Pro | Con |
|---|---|
| Single layer — no generated intermediate resources | `ConfigReconciler` becomes more complex: it must understand intent CRDs in addition to low-level CRDs |
| No ownership / GC complexity for generated resources | Requires changes to `ConfigReconciler` to resolve intent CRDs into revision entries |
| Simpler debugging — intent CRD → revision → node config, no middle layer | Need to detect and handle conflicts between an intent CRD and a manually created low-level CRD that configure the same VLAN/VRF |
| Intent CRDs are the only user-facing API; low-level CRDs remain purely as an advanced/escape-hatch mechanism | Intent-to-revision translation logic is coupled with the revision pipeline rather than isolated |
| Revision contains the merged view — easy to audit exactly what is deployed | The `NetworkConfigRevisionSpec` may need to record the source (intent vs. low-level) for debugging/audit |

#### Decision: Option A (D24)

**Option A is chosen.** Intent CRDs generate standard low-level CRDs (`Layer2NetworkConfiguration`, `VRFRouteConfiguration`, `BGPPeering`). The existing `ConfigReconciler` picks these up unchanged. Low-level CRDs remain available as a direct-use escape hatch but are not the primary interface.

**Conflict handling (D32):** When intent-based CRDs are present for a given VRF/network scope, the operator rejects any manually created low-level CRDs for the same scope. Users must remove conflicting low-level CRDs before intent-based CRDs are processed. This avoids ambiguity from partial overlaps.

A future migration to **Option B** remains possible once the API is stable and low-level CRDs can be phased out as user-facing resources.

### 3.5 VRF + Destination — Separating Metadata from Routing

#### The Problem with Inline VRF Lists

In the current low-level model, `VRFRouteConfiguration` is a powerful, composable resource. Multiple `VRFRouteConfiguration` objects can target the **same VRF**, each contributing import/export entries, SBR prefixes, aggregates, and communities. They are merged at revision build time (sorted by `seq`), and the `buildNodeVrf` function creates a single `FabricVRF` from all of them. This enables patterns like:

- Multiple teams independently adding routes to the same VRF.
- SBR prefixes creating intermediate local VRFs (`s-<vrf>`) with policy routes.
- Different `VRFRouteConfiguration` resources targeting the same VRF with different `nodeSelector` to scope routes per worker group.

The initial intent CRD design embedded VRF lists **inline** in each attachment (`Layer2Attachment.spec.network.VRFs`, `Inbound.spec.network.VRFs`). This has several issues:

1. **Duplication:** If two `Layer2Attachment`s and an `Inbound` all need connectivity to `m2m_enc`, the VRF name and its routing config appear in three places.
2. **No composability:** The current model lets multiple `VRFRouteConfiguration` resources build up a VRF's import/export list incrementally. With VRFs inlined in attachments, each attachment must carry its own complete routing view.
3. **SBR complexity hidden:** Source-based routing (SBR) is needed when two different attachments on the same node reach different VRFs whose imported prefixes overlap — e.g. both "internet" and "m2m_enc" import `0.0.0.0/0`. The controller must auto-detect this overlap and generate intermediate VRFs (`s-<vrf>`) with policy routes. This is a cross-attachment concern that belongs in the controller, not on any single attachment's spec.
4. **No shared connectivity:** A VRF like `m2m_enc` represents a backbone destination. Multiple attachments, connections, and pod networks may all need routes to/from it. It's a **shared concept**, not something each attachment should independently define.

#### Solution: Two Complementary CRDs — `VRF` and `Destination`

We separate **VRF metadata** from **routing targets** into two distinct CRDs:

- **`VRF`** — the backbone VRF identity. Carries the VRF name, VNI, route target, and optional loopback interfaces (for mirror VRFs). Defined once per VRF, referenced by name from `Destination` resources. This is pure metadata — it does not describe *what* is reachable, only *where* the VRF is in the overlay.

- **`Destination`** — a routing target. Carries `prefixes` (what is reachable) plus either a `vrfRef` (HBN mode — VRF import routing) or a `nextHop` (non-HBN mode — static routing). Defined once, referenced by label selector from attachments and connections. This is pure routing — it does not carry VRF plumbing details.

```yaml
# VRF: metadata — defined once per backbone VRF
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: VRF
metadata:
  name: m2m-enc
spec:
  vrf: m2m_enc          # VRF name (≤12 chars)
  vni: 10100            # VXLAN Network Identifier
  routeTarget: "64500:10100"
---
# Destination: routing target — references the VRF
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Destination
metadata:
  name: m2m-enc-routes
  labels:
    network.t-caas.telekom.com/vrf: m2m_enc
    network.t-caas.telekom.com/zone: secure
spec:
  vrfRef: m2m-enc       # → VRF resource by name
  prefixes:
  - 198.51.100.0/27
  - 192.0.2.0/24
```

A non-HBN Destination uses `nextHop` instead of `vrfRef`:

```yaml
# Destination without VRF — static routing via next-hop
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Destination
metadata:
  name: upstream-default
  labels:
    network.t-caas.telekom.com/zone: upstream
spec:
  nextHop:
    ipv4: 198.51.100.1
    ipv6: 2001:db8:100::1
  prefixes:
  - 0.0.0.0/0           # default route via next-hop
  - ::/0
```

Attachments and connections reference destinations via label selectors:

```yaml
# Layer2Attachment references destinations by label
spec:
  destinations:
    matchLabels:
      network.t-caas.telekom.com/zone: secure
```

This means:
- **VRF metadata is defined once on the `VRF` resource.** VNI, route target, and loopbacks are not duplicated across Destinations or attachments.
- **Destinations are defined once, referenced many times.** No duplication of routing targets.
- **Import prefixes live on the Destination** — the subnets reachable through that VRF (or next-hop) are defined once and inherited by every attachment that selects it. **Export prefixes are derived automatically** from each attachment's own subnet.
- **Dual-mode routing:** A Destination with `vrfRef` triggers **HBN mode** — `prefixes` become VRF import entries, routing is handled by VXLAN/VRF. A Destination with `nextHop` triggers **non-HBN static routing** — the controller creates static routes for each prefix via the next-hop address on the VLAN sub-interface. A Destination with `prefixes: ["0.0.0.0/0"]` + `nextHop` acts as a default gateway — static routing is the general concept, default gw is a special case.
- **`vrfRef` and `nextHop` are mutually exclusive** — a Destination uses one or the other, never both.
- **Additional routes** can still be specified on the attachment side for edge cases (extra prefixes beyond what the Destination defines).
- **SBR and intermediate VRFs** are auto-detected by the controller. When two or more attachments on the same node group reach destinations whose imported prefixes overlap, the controller automatically generates intermediate local VRFs (`s-<vrf>`) with policy routes to steer traffic by source subnet. This is transparent to the user — no SBR configuration appears on the intent API.
- **Labels enable grouping.** An attachment can select all destinations in a zone, or a specific one.

#### Why Labels Instead of Direct Name References?

Direct name references (`vrfs: [m2m_enc]`) are simple but rigid:
- Adding a new VRF requires updating every attachment that should reach it.
- No grouping — each VRF must be listed individually.
- No way to express "all production VRFs" or "all VRFs in security zone X".

Label selectors are standard Kubernetes practice and enable:
- **Loose coupling:** A new `Destination` with matching labels is automatically picked up.
- **Grouping:** Select multiple destinations with a single selector.
- **Flexibility:** Labels can represent security zones, environments, teams, etc.

#### Relationship to `VRFRouteConfiguration`

`VRF` + `Destination` replace the VRF-reference portion of the intent CRDs. They do **not** replace `VRFRouteConfiguration` — the low-level CRD remains available as an escape hatch and is still what gets produced in the revision pipeline. The intent controller translates `Destination` references (resolving `vrfRef` → `VRF` metadata) + attachment routing specs into the equivalent `VRFRouteConfiguration` entries (or directly into revision data, depending on §3.4).

Multiple attachments selecting the same `Destination` produce **merged** VRF config in the revision, just like multiple `VRFRouteConfiguration` resources do today.

#### Diagram

```
  VRF "m2m-enc"                     VRF "internet"
    vrf: m2m_enc                      vrf: internet
    vni: 10100                        vni: 10200
    routeTarget: 64500:10100          routeTarget: 64500:10200
        ▲                                 ▲
        │ vrfRef                          │ vrfRef
        │                                 │
  Destination "m2m-enc-routes"      Destination "internet-routes"
  labels:                           labels:
    zone: secure                      zone: public
  spec:                             spec:
    vrfRef: m2m-enc                   vrfRef: internet
    prefixes:                         prefixes:
    - 192.0.2.0/24                      - 0.0.0.0/0
    - 203.0.113.0/24
        ▲          ▲                          ▲
        │          │                          │
        │          │                          │
  ┌─────┘    ┌─────┘                    ┌─────┘
  │          │                          │
  │   Layer2Attachment "vlan100"   Inbound "prod-ingress"
  │     networkRef: secure-net      networkRef: ingress-net
  │     destinations:               destinations:
  │       zone: secure                zone: public
  │     (inherits prefixes            count: 2
  │      from Destination)            (inherits prefixes
  │                                    from Destination)
  │
  PodNetwork "extra-pods"
    networkRef: pod-extra-net
    destinations:
      zone: secure
    (inherits prefixes
     from Destination)
```

All three resources select the `m2m-enc-routes` destination and inherit its prefixes (`192.0.2.0/24`, `203.0.113.0/24`). The controller resolves `vrfRef: m2m-enc` → VRF metadata (VNI, RT) and merges each attachment's export requirements (its own subnets) into a single VRF configuration for `m2m_enc`, preserving the composability of today's model. Attachments can optionally specify additional routes beyond what the Destination defines.

### 3.6 Traffic Mirroring — Intent-Based Wrapper Around MirrorSelector / MirrorTarget

[Proposal 01](../01-traffic-mirroring/README.md) introduces low-level `MirrorSelector` and `MirrorTarget` CRDs for traffic mirroring. These are powerful but require users to manually coordinate several moving parts:

1. Create a dedicated mirror VRF via `VRFRouteConfiguration` (with VNI, RT, loopbacks, IPAM pool ref).
2. Create a `MirrorTarget` pointing at the collector IP, GRE key/type, and binding it to the mirror VRF + loopback.
3. Create a `MirrorSelector` per traffic flow, referencing the `MirrorTarget`, a `MirrorSource` (low-level L2/VRF CRD), direction, and traffic match.

This is exactly the kind of multi-resource coordination that intent CRDs are designed to simplify.

#### Splitting the Concerns: `Collector` + `TrafficMirror`

The same GRE collector (IP address, protocol, tunnel key) and mirror VRF are typically shared across many mirroring rules. Embedding collector config inline in every `TrafficMirror` would cause the same duplication that `Destination` solves for production VRFs. We therefore split mirroring into two CRDs:

- **`Collector`** — defines *where* mirrored traffic goes: GRE endpoint properties + which `VRF` (mirror VRF) hosts the tunnel. Defined once, referenced by name from `TrafficMirror` resources.
- **`TrafficMirror`** — defines *what* to mirror: source attachment, direction, traffic match, and a reference to a `Collector`. Lightweight and per-flow.

```
Collector "prod-collector"                TrafficMirror "capture-vlan100"
  spec:                                     spec:
    address: 192.0.2.100                     source:
    protocol: l3gre                             kind: Layer2Attachment
    key: 1001                                   name: vlan100
    mirrorVRF:                                collector: prod-collector
      name: mirror-vrf                        direction: ingress
                                              trafficMatch:
        ▲                                       srcPrefix: 203.0.113.0/24
        │  shared by                            protocol: tcp
        │                                       dstPort: 443
TrafficMirror "capture-vrf-egress"
  spec:
    source:
      kind: Outbound
      name: prod-egress
    collector: prod-collector          ← same collector, different flow
    direction: egress
```

This mirrors (pun intended) the `Destination` pattern: shared infrastructure defined once, referenced many times.

#### Relationship to Low-Level Mirror CRDs

`Collector` is the intent-level equivalent of `MirrorTarget`, and `TrafficMirror` is the intent-level equivalent of `MirrorSelector`. Depending on the pipeline integration approach (§3.4):

- **Option A:** The controllers generate `MirrorTarget` (from `Collector`) and `MirrorSelector` (from `TrafficMirror`) resources, plus the mirror VRF's `VRFRouteConfiguration` with loopbacks.
- **Option B:** The `ConfigReconciler` resolves `Collector` + `TrafficMirror` directly at `NodeNetworkConfig` build time, populating GRE interfaces, loopbacks, EVPN export filters, and `MirrorACLs` on target nodes.

In both cases, the low-level `MirrorSelector` / `MirrorTarget` CRDs remain available as an escape hatch for advanced use cases (e.g., custom GRE naming, multiple loopbacks per VRF, complex multi-target scenarios).

#### Mirror VRF as a `VRF` CRD

The mirror VRF (where the GRE tunnel lives) is modeled as a `VRF` resource with `loopbacks` configured. `Collector.mirrorVRF` references this `VRF` by name (D35). This is consistent with the rest of the intent design — a VRF is a VRF, whether it carries production traffic or mirror traffic. The `VRF` for mirroring carries:

- `vrf`, `vni`, `routeTarget` — standard VRF properties.
- `loopbacks` — with `poolRef` for per-node CAPI IPAM allocation (as designed in Proposal 01).

Multiple `Collector` resources can reference the same mirror `VRF` (e.g., multiple collectors sharing one mirror VRF), and multiple `TrafficMirror` resources can reference the same `Collector`.

## 4. Resource Specifications

### 4.1 VRF

`VRF` represents a backbone VRF identity — its name, overlay parameters (VNI, route target), and optional loopback interfaces. It is defined once per VRF and referenced by name from `Destination` resources.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: VRF
metadata:
  name: m2m-enc
spec:
  # VRF name in the backbone
  vrf: m2m_enc
  # VXLAN Network Identifier for the VRF
  vni: 10100
  # BGP route target for the VRF
  routeTarget: "64500:10100"
  # Optional: Loopback interfaces with per-node IP allocation via CAPI IPAM.
  # Primarily used for mirror VRFs (GRE source IPs) but generic enough for
  # any VRF that needs per-node loopback addresses (e.g., BGP peering).
  # loopbacks:
  # - name: lo.mir
  #   poolRef:
  #     apiGroup: ipam.cluster.x-k8s.io
  #     kind: InClusterIPPool
  #     name: mirror-source-ips
```

**Status:**
```yaml
status:
  # How many Destinations reference this VRF
  referenceCount: 2
  conditions:
  - type: Ready
    status: "True"
```

**Validation Rules:**
- `spec.vrf` is required and immutable.
- VRF name must be ≤ 12 characters (HBR constraint).
- `spec.vni` is optional — when omitted, the controller resolves it from operator config.
- `spec.routeTarget` is optional — when omitted, the controller resolves it from operator config.

### 4.2 Destination

`Destination` represents a routing target — a set of prefixes reachable via either a VRF (HBN mode) or a static next-hop (non-HBN mode). It is defined once and referenced by label selector from attachments and connections.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Destination
metadata:
  name: m2m-enc-routes
  labels:
    network.t-caas.telekom.com/vrf: m2m_enc
    network.t-caas.telekom.com/zone: secure
spec:
  # --- VRF mode (HBN) ---
  # References a VRF resource by name. The controller resolves VNI, RT,
  # and loopback config from the VRF. Prefixes become VRF import entries.
  vrfRef: m2m-enc
  # Subnets reachable via this destination.
  # Defined once here, inherited by every attachment that selects this destination.
  # Attachments do NOT need to repeat these prefixes.
  prefixes:
  - 198.51.100.0/27
  - 192.0.2.0/24
  # Note: BGP communities are NOT on the Destination — they belong on the
  # usage CRDs (Layer2Attachment, Inbound, Outbound, PodNetwork) because
  # different attachments reaching the same VRF may need different communities.

  # --- Static mode (non-HBN) ---
  # Instead of vrfRef, a Destination can specify nextHop for static routing.
  # When a Layer2Attachment (with interfaceRef) selects this Destination,
  # the controller creates static routes for each prefix via the nextHop.
  # vrfRef and nextHop are mutually exclusive.
  #
  # This generalises the "default gateway" pattern: a Destination with
  # prefixes: ["0.0.0.0/0"] + nextHop becomes a default route.
  # nextHop:
  #   ipv4: 198.51.100.1
  #   ipv6: 2001:db8:100::1

  # Note: SBR (source-based routing) is NOT configured here. It is
  # auto-detected by the controller when two attachments on the same
  # node group reach destinations with overlapping imported prefixes.
```

**Non-HBN Example** (static routing, no VRF):
```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Destination
metadata:
  name: upstream-default
  labels:
    network.t-caas.telekom.com/zone: upstream
spec:
  # No vrfRef — static routing mode
  nextHop:
    ipv4: 198.51.100.1
    ipv6: 2001:db8:100::1
  prefixes:
  - 0.0.0.0/0         # default route via next-hop
  - ::/0
```

**Status:**
```yaml
status:
  # How many attachments/connections reference this destination
  referenceCount: 3
  conditions:
  - type: Ready
    status: "True"
```

**Validation Rules:**
- `spec.vrfRef` and `spec.nextHop` are mutually exclusive — exactly one must be set.
- When `spec.vrfRef` is set, it must reference an existing `VRF` resource.
- `spec.prefixes` entries must be valid CIDR notation.
- `spec.nextHop.ipv4` and `spec.nextHop.ipv6` must be valid IP addresses (not CIDR).

### 4.3 Network

`Network` is a pure pool definition — it describes a network segment (CIDR, VLAN, VNI) and how its addresses are allocated. It does **not** carry VRFs, node scope, or any usage semantics. Those belong on the resources that *consume* the network (`Layer2Attachment`, `Inbound`, `Outbound`, `PodNetwork`).

This mirrors the SchiffCluster model where `AdditionalNetwork` defines a network pool and separate `Ingress` / `Egress` resources reference it via `fromAdditionalNetwork`. Importantly, a `Network` is **not per se L2** — it only becomes a Layer 2 segment when a `Layer2Attachment` attaches it to a set of nodes.

A `Network` may also represent a **pure L2 segment** with no IP addresses — only `vlan` (and optionally `vni`). This is common for non-HBN deployments where VLANs are provisioned on bonds for external consumption (e.g., VM attachment by vSphere / OpenStack) and IP addressing is handled outside the cluster.

**Example — L3 network (IP addresses + VLAN + VNI):**
```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Network
metadata:
  name: secure-net
spec:
  # --- Address Pools (optional — omit for pure L2 segments) ---
  ipv4:
    cidr: 198.51.100.0/24
    prefixLength: 28             # allocation slice size (e.g. /28 per attachment)
  ipv6:
    cidr: 2001:db8:100::/48
    prefixLength: 64             # allocation slice size (e.g. /64 per attachment)

  # --- Allocation Pool (per address family) ---
  # Determines how prefixes are allocated from an upstream IPAM / BM4X.
  # Separate per AF because IPv4 and IPv6 often come from different pools.
  # Reserved for future automatic allocation — not processed in this iteration.
  # allocationPool:
  #   ipv4: "private/cndtag"     # e.g. BM4X harmonisation class for IPv4
  #   ipv6: "global/cndtag"      # e.g. BM4X harmonisation class for IPv6

  # --- L2 Properties ---
  vlan: 234
  vni: 10234                     # VXLAN Network Identifier

  # --- Managed flag ---
  # When false, the operator treats this Network as a reference for IP/VLAN
  # parameters only — it does not attempt to create or modify the underlying
  # L2 segment. Use for pre-existing infrastructure-provided networks.
  # Defaults to true.
  # managed: false
```

**Example — Pure L2 segment (VLAN only, no IP addresses):**
```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Network
metadata:
  name: vlan1520
spec:
  # No ipv4/ipv6 — this is a pure L2 segment without IP assignment.
  # Common for non-HBN deployments where VLANs on bonds are consumed
  # by external systems (vSphere, OpenStack) or SR-IOV workloads.
  vlan: 1520
```

**Key design points:**
- **No VRFs.** VRFs (`destinations`) belong on the usage CRDs that reference this network.
- **No node scope.** `nodeSelector` belongs on the usage CRDs, not on the pool definition.
- **Per-AF allocation.** `allocationPool.ipv4` and `allocationPool.ipv6` are independent — IPv4 and IPv6 addresses may come from different upstream pools (matching SchiffCluster's `Harmonization.Level` / `LevelV6`).
- **`prefixLength`** on each AF determines the slice size allocated to each consumer. For example, a `/24` CIDR with `prefixLength: 28` yields up to 16 `/28` slices.
- **`managed: false`** for pre-existing/external networks. The operator only reads parameters from the `Network` but does not provision or modify the underlying segment. Defaults to `true`.

**Status:**
```yaml
status:
  # How many attachments reference this network
  referenceCount: 4
  # Allocated slices (future — when automatic allocation is implemented)
  # allocatedSlices:
  # - name: vlan100
  #   ipv4: 198.51.100.0/28
  #   ipv6: 2001:db8:100::/64
  conditions:
  - type: Ready
    status: "True"
```

**Validation Rules:**
- At least one of `ipv4`, `ipv6`, or `vlan` must be specified (a completely empty `Network` is invalid).
- `ipv4` and `ipv6` are independently optional — omitting both creates a pure L2 segment.
- When `ipv4` is set, `ipv4.cidr` must be valid CIDR notation.
- When `ipv6` is set, `ipv6.cidr` must be valid CIDR notation.
- `ipv4.prefixLength` must be ≥ the CIDR prefix length (cannot allocate a larger block than the pool).
- `vlan` must be in range 1–4094.
- CRDs that require IP addresses (`Inbound`, `Outbound`) must not reference a `Network` that has no `ipv4`/`ipv6`.
- When `managed` is `false`, the operator skips L2 provisioning for this network (reference-only mode).
- When multiple usage CRDs reference the same `Network`, the controller validates that IP allocations do not overlap.

**Controller Behavior:**
- **Validation and reference tracking in this iteration.** The `Network` controller validates the spec, tracks reference count, enforces no IP conflicts across consumers, and sets conditions. It does **not** allocate addresses — consumers specify their subnet directly and reference the `Network` by name.
- **`managed: false`:** The controller skips any L2 provisioning and treats the `Network` as a parameter reference only.
- **Future (automatic allocation):** When `allocationPool` is set and processed, the controller contacts the upstream IPAM (BM4X, OpenStack, vSphere) to carve out subnets of size `prefixLength` and writes them into the consumer's status.

### 4.4 Layer2Attachment

`Layer2Attachment` attaches a `Network` as a Layer 2 segment to a set of nodes selected by label. It supports two deployment modes:

- **HBN mode** (default): VXLAN tunnel + VRF routing via HBN. The referenced `Network` must have a `vni`, and `destinations` must be set.
- **Non-HBN mode**: Uses an existing physical interface (bond, NIC, SR-IOV PF). Set `interfaceRef` to name the physical interface. `destinations` is optional (when omitted, only the L2 bridge is created — no VRF plumbing).

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Layer2Attachment
metadata:
  name: example
spec:
  # --- Network Reference ---
  # References a Network CRD by name. The Network defines CIDR, VLAN, VNI.
  networkRef: secure-net

  # --- Destinations (replaces inline VRF lists) ---
  # Select which Destination resources (VRFs) should be reachable via this attachment.
  # Uses standard Kubernetes label selectors.
  # Optional for non-HBN: when omitted, only the L2 bridge is created (no VRF).
  destinations:
    matchLabels:
      network.t-caas.telekom.com/zone: secure

  # --- Node Scope ---
  # Selects which nodes receive this attachment using standard Kubernetes label selectors.
  nodeSelector:
    matchLabels:
      node.kubernetes.io/worker-group: wg1

  # --- Interface Configuration ---
  # interfaceName: suffix for the host interface name.
  # HBN: full name becomes 'l2.<interfaceName>'.
  # Non-HBN (with interfaceRef): defaults to 'vlan.<vlanID>' if omitted.
  interfaceName: xyz       # suffix → full name becomes 'l2.<interfaceName>' (immutable)
  # interfaceRef: use an existing physical interface instead of VXLAN.
  # When set, the attachment operates in non-HBN mode.
  # interfaceRef: bond0    # existing interface (bond, NIC, SR-IOV PF)
  mtu: 1500
  disableAnycast: false
  disableNeighborSuppression: false

  # --- BGP Configuration (non-SRIOV only) ---
  bgp:
    enabled: true
    advertiseTransferNetwork: true
    holdTime: 90s
    keepaliveTime: 30s
    maximumPrefixes: 10
    workloadAS: ''

  # --- SR-IOV Configuration ---
  sriov:
    enabled: true          # configure for VTEP_LEAF on BM4X (immutable)

  # --- Node IP Assignment ---
  nodeIPs:
    enabled: true
    reservedForPods: 4     # validate enough IPs for pods + nodes

  # --- Additional Routes (optional) ---
  # Extra prefixes to import/export beyond what the matched Destinations already define.
  # In most cases this is not needed — the Destination's prefixes are inherited automatically.
  # routes:
  # - prefixes:
  #   - 198.51.100.64/26

  # --- BGP Communities (optional) ---
  # Communities to tag on routes exported from this attachment into matched Destination VRFs.
  # Each attachment can set its own communities — different attachments reaching the same
  # VRF may need different community values.
  communities:
  - "64500:999"
  - "64500:1000"
```

**Non-HBN Example** (physical interface, no VRF):
```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Layer2Attachment
metadata:
  name: sriov-direct
spec:
  networkRef: bare-metal-net       # Network with vlanID but no VNI
  interfaceRef: bond0              # existing physical interface
  # interfaceName defaults to 'vlan.500' when omitted with interfaceRef
  nodeSelector:
    matchLabels:
      node.kubernetes.io/worker-group: wg1
  # no destinations — just L2 bridge on the physical interface
```

**Status:**
```yaml
status:
  sriovVlanID: 123          # VLAN ID for SR-IOV device traffic
  anycast:
    mac: aa:bb:cc:dd:ee:ff
    gateway: '2001:db8::1/64'
    gatewayv4: '198.51.100.129/25'
  conditions:
  - type: Ready
    status: "True"
```

**Validation Rules:**
- `networkRef` is required and must reference an existing `Network` resource.
- `interfaceName` is required when SR-IOV is disabled and `interfaceRef` is not set.
- When `interfaceRef` is set (non-HBN mode), the referenced `Network` must not have a `vni`.
- When `interfaceRef` is set and `interfaceName` is omitted, it defaults to `vlan.<vlanID>`.
- `nodeIPs.enabled` must be `false` when SR-IOV is enabled and `interfaceName` is not set.
- `disableNeighborSuppression` must be `true` when `disableAnycast` is set.
- BM4X: if both SR-IOV and `interfaceName` are set/enabled, only one VRF is allowed.

**Controller Behavior:**

| Scenario | Actions |
|---|---|
| **SR-IOV only** (no interfaceName) | Create policy allowing `NetworkAttachmentDefinition` with VLAN ID |
| **Non-SRIOV** | Create host interface `l2.<interfaceName>` with VLAN, MTU, VNI, neighborSuppression; allocate anycast gateway (first address); set VRF on interface; configure HBR routes; if `nodeIP.enabled`: configure routes in default routing table |
| **SR-IOV + interfaceName** (for macvlan/egress) | Create SR-IOV policies; create host interface `l2.<interfaceName>` attached to SR-IOV bridge; if `nodeIP.enabled`: configure routes |
| **Non-HBN + destinations (with nextHop)** | Create VLAN sub-interface on `interfaceRef`; assign node IP; for each matched `Destination` that has `nextHop`, add static routes for its `prefixes` via the next-hop address on the sub-interface. A Destination with `prefixes: ["0.0.0.0/0"]` + `nextHop` becomes a default route |

### 4.5 Inbound

`Inbound` allocates IP addresses from a `Network` and exposes them as load-balanced service endpoints via MetalLB. It optionally exports those IPs as host routes into VRFs (in HBN mode).

It supports two modes:
- **HBN mode** (with `destinations`): Produces VRF host exports + MetalLB pool + advertisement. This is the standard SCHIFF BM4X ingress flow.
- **Non-HBN mode** (without `destinations`): Produces only a MetalLB pool + advertisement. Useful for clusters without HBN where only MetalLB is needed.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Inbound
metadata:
  name: ingress-1
spec:
  # --- Network Reference ---
  networkRef: ingress-net

  # --- Destinations (optional — omit for non-HBN) ---
  # When set: IP addresses are exported as /32 host routes into each matched VRF.
  # When omitted: only MetalLB pool + advertisement is created (no VRF plumbing).
  destinations:
    matchLabels:
      network.t-caas.telekom.com/zone: secure

  # --- Node Scope ---
  nodeSelector:
    matchLabels:
      node.kubernetes.io/worker-group: wg1

  # --- Addresses ---
  count: 2                           # number of IPs to allocate from subnet
  # Explicit addresses (alternative to count-based allocation):
  # addresses:
  #   ipv4: ["203.0.113.1", "203.0.113.2"]
  #   ipv6: ["2001:db8:200::1", "2001:db8:200::2"]

  # --- Load Balancer Configuration ---
  # Pool name for MetalLB IPAddressPool (defaults to Destination VRF name or "default")
  poolName: my-pool
  # Optional: LoadBalancerClass for tenant-managed LB implementations (e.g., kube-vip)
  # tenantLoadBalancerClass: my-lb-class
  # Advertisement type: bgp (default) or l2
  advertisement:
    type: bgp                        # bgp | l2

  # Note: Ingress controller orchestration (e.g. nginx deployment) is
  # intentionally out of scope. The Inbound CRD stops at providing
  # the MetalLB IPAddressPool + advertisement. Users deploy and
  # configure their own ingress controllers separately.

  # --- Disable aggregation of the ingress network in VRF exports ---
  disableAggregation: false

  # --- BGP Communities (optional) ---
  # Communities to tag on routes exported from this inbound into matched Destination VRFs.
  communities:
  - "64500:999"
```

**Status:**
```yaml
status:
  addresses:
    ipv4:
    - "203.0.113.1"
    - "203.0.113.2"
    ipv6:
    - "2001:db8:200::1"
    - "2001:db8:200::2"
  poolName: my-pool
  conditions:
  - type: Ready
    status: "True"
```

**Validation Rules:**
- `networkRef` is required and must reference an existing `Network` resource.
- The referenced `Network` must have at least one of `ipv4` or `ipv6` defined (IP-less Networks cannot be used with `Inbound`).
- `count` or `addresses` must be specified, but not both.
- `sum(count)` must not exceed usable IP addresses in the referenced network's subnet.
- `tenantLoadBalancerClass` may only be set with HBN flavour `legacy-frr`.

**Controller Behavior:**

| Mode | Actions |
|---|---|
| **HBN** (destinations set) | Resolve `networkRef` → read subnet from `Network` → allocate IPs → add as `/32` host exports to each matched VRF (with communities from Inbound spec) → create MetalLB `IPAddressPool` → create `BGPAdvertisement` or `L2Advertisement` |
| **Non-HBN** (no destinations) | Resolve `networkRef` → read subnet from `Network` → allocate IPs → create MetalLB `IPAddressPool` → create `BGPAdvertisement` or `L2Advertisement` |

### 4.6 Outbound

`Outbound` enables egress connectivity from pods to external networks via SNAT. It allocates IPs from a `Network` and produces Coil `Egress` resources, Calico `IPPool` and `NetworkPolicy` resources, and optionally VRF route exports (in HBN mode).

It supports two modes:
- **HBN mode** (with `destinations`): Produces VRF host exports + Coil Egress + Calico pools/policies.
- **Non-HBN mode** (without `destinations`): Produces only Coil Egress + Calico pools/policies.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Outbound
metadata:
  name: egress-1
spec:
  # --- Network Reference ---
  networkRef: egress-net

  # --- Destinations (optional — omit for non-HBN) ---
  # When set: egress IPs are exported as /32 host routes into each matched VRF.
  # When omitted: only Coil Egress + Calico pools are created (no VRF plumbing).
  destinations:
    matchLabels:
      network.t-caas.telekom.com/zone: secure

  # --- Node Scope ---
  nodeSelector:
    matchLabels:
      node.kubernetes.io/worker-group: wg1

  # --- Egress Configuration ---
  replicas: 2                        # number of egress gateway replicas
  count: 3                           # number of IPs (must be > replicas)
  # Explicit addresses (alternative to count-based allocation):
  # addresses:
  #   ipv4: ["203.0.113.17", "203.0.113.18", "203.0.113.19"]

  # --- Egress NAT Destinations (user-managed) ---
  # CIDRs that should be reachable via this egress.
  # These end up in the Coil egressNAT ConfigMap.
  # egressDestinations:
  # - 198.51.100.0/24
  # - 203.0.113.0/24

  # --- Disable aggregation of the egress network in VRF exports ---
  disableAggregation: false

  # --- BGP Communities (optional) ---
  # Communities to tag on routes exported from this outbound into matched Destination VRFs.
  communities:
  - "64500:999"
```

**Status:**
```yaml
status:
  addresses:
    ipv4:
    - "203.0.113.17"
    - "203.0.113.18"
    - "203.0.113.19"
  conditions:
  - type: Ready
    status: "True"
```

**Validation Rules:**
- `networkRef` is required and must reference an existing `Network` resource.
- The referenced `Network` must have at least one of `ipv4` or `ipv6` defined (IP-less Networks cannot be used with `Outbound`).
- `count` must be > `replicas` (need N+1 IPs for N replicas).
- `count` or `addresses` must be specified, but not both.
- At least one `Destination` must match when `destinations` is set.

**Controller Behavior:**

| Mode | Actions |
|---|---|
| **HBN** (destinations set) | Resolve `networkRef` → read subnet from `Network` → allocate IPs → add as `/32` host exports to each matched VRF → create Coil `Egress` (replicas, IPs) → create Calico `IPPool` → create Calico `NetworkPolicy` for egress policy |
| **Non-HBN** (no destinations) | Resolve `networkRef` → read subnet from `Network` → allocate IPs → create Coil `Egress` (replicas, IPs) → create Calico `IPPool` → create Calico `NetworkPolicy` |

### 4.7 BGPNeighbor

`BGPNeighbor` describes a BGP neighbor that applications can use to advertise routes to the backbone. A neighbor references one or more `Inbound` connections (which must have `disableLoadBalancer` semantics — i.e., no MetalLB pool is created for that address range).

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: BGPNeighbor
metadata:
  name: example
spec:
  allowedInbounds:
  - name: bgp-transfer          # references an Inbound resource
  advertiseTransferNetwork: true
  holdTime: 90s
  keepaliveTime: 30s
  maximumPrefixes: 10
  workloadAS: ''
```

**Status:**
```yaml
status:
  asNumber: "64501"
  neighborIPs:
  - "2001:db8:1::192/127"
  - "2001:db8:1::194/127"
  neighborASNumber: 124
  workloadASNumber: 123
  holdTime: 90s
  keepaliveTime: 30s
  maximumPrefixes: 10
```

**Validation Rules:**
- All referenced `Inbound` resources must exist.
- All referenced `Inbound` resources must not have a MetalLB controller configured (BGP is handled by the workload, not MetalLB).

### 4.8 PodNetwork

`PodNetwork` allocates additional networks available to pods, configured through the CNI (Calico).

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: PodNetwork
metadata:
  name: example
spec:
  networkRef: pod-extra-net
  # --- Destinations ---
  destinations:
    matchLabels:
      network.t-caas.telekom.com/zone: secure
  # --- Node Scope ---
  nodeSelector:
    matchLabels:
      node.kubernetes.io/worker-group: wg1
  # Additional routes beyond what the Destination defines (optional)
  # routes:
  # - prefixes:
  #   - 198.51.100.64/26
```

**Controller Behavior:**
- Resolve `networkRef` → read subnet from `Network`.
- Configure Calico IP pools for the referenced network's subnets.
- Create appropriate network policies.
- Set up routes toward specified VRFs.

### 4.9 Collector

`Collector` defines a GRE collector endpoint and its binding to a mirror VRF. It is defined once and referenced by name from `TrafficMirror` resources. This is the intent-level equivalent of `MirrorTarget`.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Collector
metadata:
  name: prod-collector
spec:
  # --- GRE Endpoint ---
  # Remote collector IP address.
  address: 192.0.2.100
  # GRE encapsulation protocol.
  protocol: l3gre              # l3gre | l2gre
  # Optional GRE tunnel key.
  key: 1001

  # --- Mirror VRF ---
  # References a VRF CRD representing the mirror VRF.
  # The mirror VRF must have loopbacks with a poolRef for per-node
  # GRE source IP allocation via CAPI IPAM.
  mirrorVRF:
    name: mirror-vrf
```

**Status:**
```yaml
status:
  # GRE interface name generated for this collector
  greInterface: gre.mir
  # Number of TrafficMirror resources referencing this collector
  referenceCount: 2
  # Number of nodes where the GRE tunnel is active
  activeNodes: 3
  conditions:
  - type: Ready
    status: "True"
```

**Validation Rules:**
- `spec.address` must be a valid IP address.
- `spec.protocol` must be `l3gre` or `l2gre`.
- `spec.mirrorVRF.name` must reference an existing `VRF` with `loopbacks` configured.

**Controller Behavior:**

| Step | Action |
|---|---|
| 1. Resolve mirror VRF | Look up the `VRF` → get mirror VRF name, VNI, RT, loopback config |
| 2. Ensure per-node IPAM | For each node in scope: create/find `IPAddressClaim` for the mirror VRF loopback (CAPI IPAM) |
| 3. Generate GRE tunnel | Create GRE interface entry in the mirror VRF on `NodeNetworkConfig` (src = allocated loopback IP, dst = collector address) |
| 4. EVPN export filter | Auto-append `permit <loopback-ip>/32` to the mirror VRF's EVPN export filter |
| 5. Track references | Count how many `TrafficMirror` resources reference this collector |

### 4.10 TrafficMirror

`TrafficMirror` declaratively mirrors selected traffic from an attachment to a `Collector`. It is the intent-level equivalent of `MirrorSelector` — lightweight and per-flow.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: TrafficMirror
metadata:
  name: capture-vlan100
spec:
  # --- Source: which attachment's traffic to mirror ---
  source:
    # References a Layer2Attachment, Inbound, or Outbound by name.
    kind: Layer2Attachment       # Layer2Attachment | Inbound | Outbound
    name: vlan100

  # --- Collector: where to send mirrored traffic ---
  # References a Collector by name.
  collector: prod-collector

  # --- Direction ---
  direction: ingress             # ingress | egress

  # --- Traffic Match (optional — if omitted, all traffic is mirrored) ---
  trafficMatch:
    srcPrefix: "203.0.113.0/24"
    protocol: tcp
    dstPort: 443
```

**Status:**
```yaml
status:
  # Number of nodes where the mirror ACL is active
  activeNodes: 3
  conditions:
  - type: Resolved
    status: "True"
    message: "Source and collector resolved successfully"
  - type: Applied
    status: "True"
    message: "MirrorACLs programmed on 3 nodes"
```

**Validation Rules:**
- `source.kind` must be `Layer2Attachment`, `Inbound`, or `Outbound`.
- `source.name` must reference an existing resource of that kind.
- `collector` must reference an existing `Collector`.
- `direction` is required.

**Controller Behavior:**

| Step | Action |
|---|---|
| 1. Resolve source | Look up the referenced `Layer2Attachment`, `Inbound`, or `Outbound` → determine the L2 VLAN or VRF interface to mirror from, and the node scope (`nodeSelector`) |
| 2. Resolve collector | Look up the `Collector` → get its GRE interface name and mirror VRF |
| 3. Generate MirrorACL | Attach `MirrorACL` to the source L2 or VRF on `NodeNetworkConfig` (mirrorDestination = collector's GRE interface name) |

### 4.11 NodeNetworkStatus

`NodeNetworkStatus` provides a per-node inventory of network interfaces, routes, and IP addresses. It is populated by the CRA agents running on each node and serves two purposes:

1. **Validation** — the operator validates `Layer2Attachment.interfaceRef` against the interfaces reported in `NodeNetworkStatus`, catching configuration errors before they reach the node.
2. **Observability** — operators and GitOps tooling can inspect the actual network state of each node without SSH access.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: NodeNetworkStatus
metadata:
  name: worker-node-01    # matches the Kubernetes node name
spec: {}                   # no user-configurable spec — agent-populated
status:
  # Interfaces present on the node (excluding pod veths and container-internal interfaces)
  interfaces:
  - name: eth0
    mac: "aa:bb:cc:dd:ee:f0"
    mtu: 9000
    state: up
    addresses:
    - "192.168.1.10/24"
    - "2001:db8::10/64"
  - name: bond0
    mac: "aa:bb:cc:dd:ee:f1"
    mtu: 9000
    state: up
    type: bond
    members: ["ens5f0np0", "ens5f1np1"]
    addresses:
    - "10.0.0.1/24"
  - name: vlan.234
    mac: "aa:bb:cc:dd:ee:f1"
    mtu: 1500
    state: up
    type: vlan
    parent: bond0
    vlanID: 234
    addresses:
    - "198.51.100.1/24"

  # Routes on the node (summary — excludes per-pod and link-local routes)
  routes:
  - destination: "0.0.0.0/0"
    gateway: "192.168.1.1"
    interface: eth0
    table: main
  - destination: "198.51.100.0/24"
    interface: vlan.234
    table: main

  # Last update timestamp from the agent
  lastUpdated: "2026-02-18T10:30:00Z"

  conditions:
  - type: Ready
    status: "True"
    message: "Agent reported network status"
```

**Key design points:**
- **Agent-populated, not user-managed.** The `spec` is empty; all data lives in `status`, written by the node agent.
- **Pod veths excluded.** Container-internal interfaces (veth pairs, cni0, etc.) are filtered out to keep the resource focused on host-level networking.
- **One resource per node.** The resource name matches the Kubernetes node name.
- **Bond and VLAN metadata.** Bond interfaces include `members`; VLAN interfaces include `parent` and `vlanID`. This enables rich validation (e.g., "does `bond0` exist on this node?").

**Controller Behavior:**
- The CRA agent on each node periodically updates the `NodeNetworkStatus` resource for that node.
- The intent controllers (e.g., `Layer2Attachment` controller) read `NodeNetworkStatus` resources for nodes in the `nodeSelector` scope to validate `interfaceRef` references.
- If a referenced interface does not exist on a target node, the controller sets a `InterfaceNotFound` condition on the attachment's status.

## 5. Implementation Plan

> **Note:** The pipeline integration uses **Option A** (§3.4, D24) — intent CRDs generate intermediate low-level CRDs. The existing `ConfigReconciler` remains unchanged.

### Phase 1 — Core CRD Types and Scaffolding

1. **Define Go types** for `VRF`, `Network`, `Destination`, `Layer2Attachment`, `Inbound`, `Outbound`, `BGPNeighbor`, `PodNetwork`, `Collector`, `TrafficMirror`, `NodeNetworkStatus` in `api/v1alpha1/`.
2. **Generate CRDs** via `controller-gen` and **deep-copy** methods.
3. **Add webhook validation** for each new CRD with the rules described in §4.

### Phase 2 — Network Controller

1. **Watch `Network`** resources.
2. **Validate** that at least one of `ipv4`, `ipv6`, or `vlan` is set; CIDR notation is valid when present; `prefixLength` constraints are met.
3. **Track references** — count how many usage CRDs (`Layer2Attachment`, `Inbound`, `Outbound`, `PodNetwork`) reference this `Network`.
4. **Update `Network.Status`** with reference count and conditions.
5. **Future:** When `allocationPool` is processed, contact upstream IPAM to carve out subnets.

### Phase 3 — Destination Controller

1. **Watch `Destination`** resources.
2. **Validate** VRF name, resolve VNI/RT (from spec or operator config).
3. **Track references** — count how many attachments/connections select this destination.
4. **Update `Destination.Status`** with reference count and conditions.

### Phase 4 — Layer2Attachment Controller

This is the highest-value, most complex intent resource. Implementation steps:

1. **Watch `Layer2Attachment`** in a new controller.
2. **Resolve `networkRef`** — look up the referenced `Network` resource, read VLAN, VNI, subnets.
3. **Resolve `destinations` selector** — list all `Destination` resources matching the label selector.
4. **Translate into the revision pipeline** (approach depends on §3.4):
   - Map `mtu`, `vni`, `vlan` (from referenced `Network`).
   - Set `anycastGateways`, `anycastMac` (unless `disableAnycast`).
   - Set `neighSuppression` (unless `disableNeighborSuppression`).
   - For each matched destination: determine VRF, generate import entries from the destination's `prefixes` and export entries from the network's subnet.
   - Set the resolved VRF on the interface (single destination) or the cluster VRF (multiple destinations).
   - **SBR auto-detection:** After resolving all attachments' destinations per node group, compare imported prefix sets across attachments. If two or more attachments reach destinations with overlapping prefixes, auto-generate intermediate local VRFs (`s-<vrf>`) and policy routes (same mechanism as today's `sbrPrefixes`). This is transparent to the user.
   - Set `nodeSelector` from the spec's `nodeSelector` label selector.
5. **Configure node IPs** if `nodeIPs.enabled`.
6. **SR-IOV handling:**
   - When `sriov.enabled`: create SR-IOV policies for `NetworkAttachmentDefinition`.
   - When `sriov.enabled` + `interfaceName` set: additionally create the host interface attached to SR-IOV bridge.
7. **Non-HBN handling:**
   - When `interfaceRef` is set: use the named physical interface instead of creating VXLAN tunnels.
   - When `interfaceName` is omitted with `interfaceRef`: default to `vlan.<vlanID>`.
   - When `destinations` is omitted: skip VRF plumbing entirely (L2 bridge only).
8. **Update `Layer2Attachment.Status`** with anycast info, SR-IOV VLAN ID, matched destinations, conditions.

### Phase 5 — Inbound Controller

1. **Watch `Inbound`**.
2. **Resolve `networkRef`** — look up the referenced `Network` resource, read subnets.
3. **Resolve `destinations` selector** (if set) — list all matching `Destination` resources.
4. **Allocate IP addresses** from the network's subnet based on `count` or explicit `addresses`.
5. **HBN mode** (destinations set):
   - Add allocated IPs as `/32` host exports to each matched VRF (with communities from the `Inbound` spec).
   - Support aggregation (unless `disableAggregation`).
6. **Create MetalLB resources:** `IPAddressPool` with allocated addresses; `BGPAdvertisement` or `L2Advertisement` based on `advertisement.type`.
7. **Update `Inbound.Status`** with assigned addresses, pool name, conditions.

### Phase 5b — Outbound Controller

1. **Watch `Outbound`**.
2. **Resolve `networkRef`** — look up the referenced `Network` resource, read subnets.
3. **Resolve `destinations` selector** (if set) — list all matching `Destination` resources.
4. **Allocate IP addresses** from the network's subnet based on `count` or explicit `addresses`.
5. **HBN mode** (destinations set):
   - Add allocated IPs as `/32` host exports to each matched VRF.
   - Support aggregation (unless `disableAggregation`).
6. **Create Coil Egress** resource with replicas and IPs.
7. **Create Calico resources:** `IPPool` for the subnet; `NetworkPolicy` for egress policy.
8. **Update `Outbound.Status`** with assigned addresses, conditions.

### Phase 6 — BGPNeighbor Controller

1. **Watch `BGPNeighbor`**.
2. **Resolve referenced `Inbound` resources** → validate constraints.
3. **Translate into the revision pipeline** (approach depends on §3.4) with appropriate import/export filters derived from the referenced Inbound resources.
4. **Update `BGPNeighbor.Status`** with assigned AS numbers, neighbor IPs, timers.

### Phase 7 — PodNetwork Controller

1. **Watch `PodNetwork`**.
2. **Resolve `destinations` selector** — list all matching `Destination` resources.
3. **Configure Calico** `IPPool` for the user-specified subnet.
4. **Set up routes** toward each matched destination's VRF (Option A — generate `VRFRouteConfiguration`, D24).

### Phase 8 — NodeNetworkStatus Agent Integration

1. **CRA agent extension** — each CRA agent reports its node's network interfaces, routes, and IP addresses to a `NodeNetworkStatus` resource.
2. **Agent periodically updates** the `NodeNetworkStatus.status` with current data (excluding pod veths and container-internal interfaces).
3. **Intent controllers read `NodeNetworkStatus`** for nodes in `nodeSelector` scope to validate `interfaceRef` references.
4. **Set conditions** — `InterfaceNotFound` on the attachment if the referenced interface does not exist.

### Phase 9 — Collector Controller

1. **Watch `Collector`**.
2. **Resolve `mirrorVRF`** — look up the `VRF` for the mirror VRF, validate it has `loopbacks` with a `poolRef`.
3. **CAPI IPAM integration** — for each node in scope (determined by the `TrafficMirror` resources that reference this collector):
   - Create or find an `IPAddressClaim` named `<mirror-vrf>-<loopback>-<node>` using the loopback's `poolRef`.
   - Wait for the IPAM provider to fulfil the claim → read `IPAddress.Spec.Address`.
4. **Generate low-level CRDs** (Option A, D24):
   - Mirror VRF: generate `VRFRouteConfiguration` with loopback + allocated IP, GRE interface (src = loopback, dst = collector address), EVPN export filter entry.
5. **Update `Collector.Status`** with GRE interface name, reference count, active node count, conditions.

### Phase 10 — TrafficMirror Controller

1. **Watch `TrafficMirror`**.
2. **Resolve `source`** — look up the referenced `Layer2Attachment`, `Inbound`, or `Outbound` to determine the source interface and `nodeSelector` (node scope).
3. **Resolve `collector`** — look up the `Collector`, get its GRE interface name.
4. **Generate `MirrorSelector`** (Option A, D24) for the source L2 or VRF, with mirrorDestination = collector's GRE interface name.
5. **Update `TrafficMirror.Status`** with active node count, conditions.

### Phase 11 — Migration Path

1. **Coexistence:** Intent-based and low-level CRDs coexist. Both feed into the revision pipeline.
2. **Adoption tool:** Provide a utility to generate `Layer2Attachment` / `Inbound` / `Outbound` from existing low-level CRDs and SchiffCluster configs, facilitating migration.
3. **Deprecation:** Once intent-based CRDs are stable, low-level CRDs may be deprecated for direct user creation (they remain as an advanced/escape-hatch mechanism).

## 6. Translation Examples

### 6.1 Destinations + Layer2Attachment → Revision Pipeline

The controller resolves the destination selector, then translates the attachment + matched destinations into L2 and VRF config entries for the revision.

```
Network "secure-net"                      (pool definition)
  spec:
    ipv4: { cidr: 198.51.100.128/25 }
    ipv6: { cidr: 2001:db8:100::/64 }
    vlan: 234
    vni: 10234

VRF "m2m-enc"                             (VRF metadata — defined once)
  spec:
    vrf: m2m_enc
    vni: 10100
    routeTarget: "64500:10100"

Destination "m2m-enc-routes"              (routing target — references VRF)
  labels: { zone: secure }
  spec:
    vrfRef: m2m-enc                       ← resolves VNI/RT from VRF
    prefixes:                             ← subnets reachable via this VRF
    - 198.51.100.0/27
    - 192.0.2.0/24
                ▲
                │  label selector match
                │
Layer2Attachment "my-vlan"                Equivalent L2 + VRF config in revision:
  spec:                                     L2:
    networkRef: secure-net          ──▶       id: 234
    mtu: 1500                                 mtu: 1500
    interfaceName: mynet                      vni: 10234
    nodeSelector:                             anycastMac: aa:bb:cc:dd:ee:ff
      matchLabels:                            anycastGateways: [2001:db8:100::1/64]
        worker-group: wg1                     neighSuppression: true
    destinations:                             vrf: m2m_enc    ← from VRF via Destination
      matchLabels:                            nodeSelector:
        zone: secure                            worker-group: wg1
    # no routes needed — inherited       VRF:
    # from Destination                      vrf: m2m_enc      ← from VRF
                                              import:           ← from Destination.prefixes
                                              - cidr: 198.51.100.0/27
                                                action: permit
                                              - cidr: 192.0.2.0/24
                                                action: permit
                                              export:           ← network's own subnet
                                              - cidr: 2001:db8:100::0/64
                                                action: permit
```

**Key point:** If `PodNetwork "extra-pods"` also selects `zone: secure` and references another `Network`, its routes are **merged** into the same `m2m_enc` VRF config — just like multiple `VRFRouteConfiguration` resources merge today.

### 6.2 Shared Destination — Multiple Attachments, Merged VRF Config

```
VRF "m2m-enc"          ← VRF metadata
  vrf: m2m_enc
  vni: 10100
  routeTarget: 64500:10100
        ▲
        │ vrfRef
        │
Destination "m2m-enc-routes"  ← selected by both:
  vrfRef: m2m-enc
  prefixes:            ← defined once
  - 198.51.100.0/27
  - 192.0.2.0/24
        ▲         ▲
        │         │
  Layer2Attachment    PodNetwork
  "vlan100"           "extra-pods"
  networkRef:         networkRef:
    secure-net          pod-extra-net
  (no routes needed —  (no routes needed —
   inherits from Dest)  inherits from Dest)

        │                  │
        └────────┬─────────┘
                 ▼
  Merged VRF config for m2m_enc:
    import:                                   ← from Destination.prefixes (shared)
    - cidr: 198.51.100.0/27, action: permit
    - cidr: 192.0.2.0/24,     action: permit
    export:                                   ← each attachment exports its network's subnet
    - cidr: 2001:db8:1:…/64,      action: permit    (from secure-net)
    - cidr: 2001:db8:2:…/64,      action: permit    (from pod-extra-net)
```

Import prefixes come from the `Destination` (defined once), while export prefixes come from each attachment's `Network` subnet. This preserves the **composability** of today's `VRFRouteConfiguration` model while eliminating prefix duplication.

### 6.3 Inbound → Revision + Platform Configuration

```
Network "ingress-net"                     (pool definition)
  spec:
    ipv4: { cidr: 203.0.113.0/28 }
    vlan: 300

Destination "m2m-enc-routes"              (routing target)
  labels: { zone: secure }
  spec: { vrfRef: m2m-enc }
                ▲
                │
Inbound "ingress-1"
  spec:
    networkRef: ingress-net
    destinations:
      matchLabels: { zone: secure }
    count: 2
    advertisement:
      type: bgp

        │
        ▼

  ┌─────────────────────────────────────────────────────────┐
  │ Produced config:                                         │
  │                                                          │
  │ VRF entry for "m2m_enc" in revision                   │
  │   export (hosts): 203.0.113.1/32, 203.0.113.2/32       │
  │   communities: <from Inbound>                           │
  │                                                          │
  │ MetalLB IPAddressPool "ingress-1"                       │
  │   addresses: [203.0.113.1/32, 203.0.113.2/32]           │
  │                                                          │
  │ MetalLB BGPAdvertisement "ingress-1"                    │
  │   ipAddressPools: [ingress-1]                           │
  └─────────────────────────────────────────────────────────┘
```

### 6.4 Outbound → Revision + Platform Configuration

```
Network "egress-net"                      (pool definition)
  spec:
    ipv4: { cidr: 203.0.113.16/28 }
    vlan: 301

Destination "m2m-enc-routes"              (routing target)
  labels: { zone: secure }
  spec: { vrfRef: m2m-enc }
                ▲
                │
Outbound "egress-1"
  spec:
    networkRef: egress-net
    destinations:
      matchLabels: { zone: secure }
    replicas: 2
    count: 3

        │
        ▼

  ┌─────────────────────────────────────────────────────────┐
  │ Produced config:                                         │
  │                                                          │
  │ VRF entry for "m2m_enc" in revision                   │
  │   export (hosts): 203.0.113.17/32 .. 203.0.113.19/32   │
  │                                                          │
  │ Coil Egress "egress-1"                                  │
  │   replicas: 2                                           │
  │   ips: [203.0.113.17, 203.0.113.18, 203.0.113.19]      │
  │                                                          │
  │ Calico IPPool "egress-1-pool"                           │
  │   cidr: 203.0.113.16/28                                 │
  └─────────────────────────────────────────────────────────┘
```

### 6.5 Non-HBN Inbound (MetalLB only, no VRF)

```
Network "simple-net"                      (pool definition)
  spec:
    ipv4: { cidr: 203.0.113.32/28 }

Inbound "simple-lb"
  spec:
    networkRef: simple-net
    # no destinations → no VRF plumbing
    count: 1
    advertisement:
      type: l2

        │
        ▼

  ┌─────────────────────────────────────────────────────────┐
  │ Produced config:                                         │
  │                                                          │
  │ MetalLB IPAddressPool "simple-lb"                       │
  │   addresses: [203.0.113.33/32]                           │
  │                                                          │
  │ MetalLB L2Advertisement "simple-lb"                     │
  │   ipAddressPools: [simple-lb]                           │
  │                                                          │
  │ (no VRF config — non-HBN mode)                          │
  └─────────────────────────────────────────────────────────┘
```

### 6.6 Pure L2 — VLANs on Bond (no IP, no VRF)

This example maps to the real-world GitOps pattern: provision multiple VLANs on an existing bond for external consumers (vSphere / OpenStack VMs, SR-IOV workloads). No IP addresses, no VRF plumbing.

```
Network "vlan1520"                        (pure L2 — VLAN only)
  spec:
    vlan: 1520
    # no ipv4/ipv6 — IP-less L2 segment

Network "vlan1522"
  spec:
    vlan: 1522

Layer2Attachment "vlan1520"               Produced config on each worker node:
  spec:                                     VLAN sub-interface:
    networkRef: vlan1520         ──▶         name: vlan.1520 (auto-generated)
    interfaceRef: bond2                      parent: bond2
    mtu: 9000                                mtu: 9000
    nodeSelector:                            vlan: 1520
      matchLabels:                           (no VRF, no anycast gateway,
        node-role.kubernetes.io/              no VXLAN tunnel)
          worker: ""

Layer2Attachment "vlan1522"               Same pattern for each VLAN.
  spec:                                   10 Networks + 10 Layer2Attachments
    networkRef: vlan1522                  = 10 VLAN sub-interfaces on bond2,
    interfaceRef: bond2                     one per worker node.
    mtu: 9000
    nodeSelector:
      matchLabels:
        node-role.kubernetes.io/
          worker: ""
```

**Key points:**
- Bond creation is out of scope — `bond2` is assumed to exist (managed by platform tooling).
- `interfaceName` defaults to `vlan.<vlanID>` when omitted with `interfaceRef`.
- No `destinations` → no VRF plumbing, no anycast. Pure L2 bridge only.
- No `ipv4`/`ipv6` on the `Network` → no IP assignment. The VLAN is a pure L2 segment.

## 7. VRF, Network, Shared Types, and Go Type Definitions

The `VRF` CRD defines backbone VRF metadata (name, VNI, RT, loopbacks). The `Network` CRD is the pool definition (with `managed` flag for external networks). `Destination` is the routing target — referencing a `VRF` (HBN) or specifying a `nextHop` (non-HBN). Usage CRDs reference Networks by name via `networkRef` and Destinations by label selector via `destinations`. Node scoping uses `nodeSelector` (a standard `metav1.LabelSelector`) instead of string arrays. `NodeNetworkStatus` provides per-node interface/route/IP inventory populated by agents.

```go
// --- Network CRD ---

// NetworkSpec defines the desired state of Network — a pure pool definition.
// It describes an address pool (CIDRs, VLAN, VNI) and allocation properties.
// It does NOT carry VRFs, node scope, or any usage semantics — those
// belong on the resources that consume the network via networkRef.
//
// At least one of IPv4, IPv6, or VLAN must be set.
// When both IPv4 and IPv6 are omitted, the Network represents a pure L2
// segment (VLAN-only) without IP assignment — common for non-HBN
// deployments where VLANs on bonds serve external consumers (vSphere,
// OpenStack, SR-IOV workloads).
type NetworkSpec struct {
    // IPv4 configures the IPv4 address pool.
    // +optional
    IPv4 *IPNetwork `json:"ipv4,omitempty"`
    // IPv6 configures the IPv6 address pool.
    // +optional
    IPv6 *IPNetwork `json:"ipv6,omitempty"`
    // AllocationPool determines how prefixes are allocated from upstream IPAM.
    // Separate per AF because IPv4 and IPv6 often come from different pools.
    // Reserved for future automatic allocation — not processed in this iteration.
    // +optional
    AllocationPool *AllocationPool `json:"allocationPool,omitempty"`
    // VLAN is the VLAN ID for the network segment.
    // +optional
    VLAN *int `json:"vlan,omitempty"`
    // VNI is the VXLAN Network Identifier.
    // +optional
    VNI *int `json:"vni,omitempty"`
    // Managed indicates whether the operator should provision/manage the
    // underlying L2 segment. When false, the Network is treated as a
    // reference for IP/VLAN parameters only — no L2 provisioning is
    // attempted. Use for pre-existing infrastructure-provided networks.
    // Defaults to true.
    // +optional
    Managed *bool `json:"managed,omitempty"`
}

// IPNetwork describes an IP address pool for a single address family.
type IPNetwork struct {
    // CIDR is the IP network in CIDR notation (e.g. "198.51.100.0/24").
    CIDR string `json:"cidr"`
    // PrefixLength is the allocation slice size (e.g. 28 means /28 per consumer).
    // Must be >= the CIDR prefix length.
    // +optional
    PrefixLength *int `json:"prefixLength,omitempty"`
}

// AllocationPool determines the upstream allocation class per address family.
// Matches SchiffCluster's Harmonization (Level for IPv4, LevelV6 for IPv6).
type AllocationPool struct {
    // IPv4 is the allocation class for IPv4 (e.g. "private/cndtag").
    // +optional
    IPv4 *string `json:"ipv4,omitempty"`
    // IPv6 is the allocation class for IPv6 (e.g. "global/cndtag").
    // +optional
    IPv6 *string `json:"ipv6,omitempty"`
}

// --- Usage CRDs ---

// Layer2AttachmentSpec defines the desired state of Layer2Attachment.
type Layer2AttachmentSpec struct {
    // NetworkRef references a Network CRD by name.
    NetworkRef string `json:"networkRef"`
    // Destinations selects Destination resources by label.
    // If omitted, no VRF plumbing is generated (non-HBN mode).
    // +optional
    Destinations *metav1.LabelSelector `json:"destinations,omitempty"`
    // NodeSelector selects which nodes receive this attachment.
    // Uses standard Kubernetes label selector semantics.
    // +optional
    NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`
    // InterfaceRef specifies an existing host interface to use instead of
    // creating a VXLAN tunnel (e.g. a physical NIC, bond, or SR-IOV VF).
    // When set, the operator attaches the VLAN to this interface rather than
    // relying on HBN VXLAN tunneling. Required for non-HBN deployments.
    // +optional
    InterfaceRef *string `json:"interfaceRef,omitempty"`
    // InterfaceName overrides the auto-generated interface name.
    // Default: vlan.<vlanID> (when interfaceRef is set) or
    //          the HBN-generated name (when interfaceRef is not set).
    // +optional
    InterfaceName *string `json:"interfaceName,omitempty"`
    // SRIOV indicates whether this attachment uses SR-IOV VFs.
    // +optional
    SRIOV bool `json:"sriov,omitempty"`
    // Communities is a list of BGP communities to set on routes exported
    // from this attachment into matched Destination VRFs.
    // +optional
    Communities []string `json:"communities,omitempty"`
    // Note: Static routing for non-HBN mode (next-hop addresses) is on the
    // Destination CRD (NextHop field), not here. The controller reads
    // Destination.nextHop when building routes for interfaceRef-based attachments.
}

// InboundSpec defines the desired state of Inbound.
type InboundSpec struct {
    // NetworkRef references a Network CRD by name.
    NetworkRef string `json:"networkRef"`
    // Destinations selects Destination resources by label.
    // If omitted, no VRF plumbing is generated — only MetalLB pool
    // and advertisement are created (non-HBN mode).
    // +optional
    Destinations *metav1.LabelSelector `json:"destinations,omitempty"`
    // NodeSelector selects which nodes receive the VRF attachment.
    // +optional
    NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`
    // Count is the number of IP addresses to allocate from the subnet
    // for MetalLB service endpoints.
    // +kubebuilder:validation:Minimum=1
    Count int `json:"count"`
    // Advertisement configures how the allocated IPs are advertised.
    Advertisement AdvertisementConfig `json:"advertisement"`
    // Communities is a list of BGP communities to set on routes exported
    // from this inbound into matched Destination VRFs.
    // +optional
    Communities []string `json:"communities,omitempty"`
    // Note: Ingress controller orchestration (e.g. nginx) is intentionally
    // out of scope. The Inbound CRD stops at providing the MetalLB pool +
    // advertisement. Users deploy ingress controllers separately.
}

// AdvertisementConfig configures MetalLB advertisement mode.
type AdvertisementConfig struct {
    // Type is the advertisement mode: bgp or l2.
    // +kubebuilder:validation:Enum=bgp;l2
    Type string `json:"type"`
}

// OutboundSpec defines the desired state of Outbound.
type OutboundSpec struct {
    // NetworkRef references a Network CRD by name.
    NetworkRef string `json:"networkRef"`
    // Destinations selects Destination resources by label.
    // If omitted, no VRF plumbing is generated — only Coil Egress
    // and (optionally) Calico IPPool are created (non-HBN mode).
    // +optional
    Destinations *metav1.LabelSelector `json:"destinations,omitempty"`
    // NodeSelector selects which nodes receive the VRF attachment.
    // +optional
    NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`
    // Count is the number of IP addresses to allocate from the subnet
    // for Coil egress endpoints.
    // +kubebuilder:validation:Minimum=1
    Count int `json:"count"`
    // Replicas is the number of Coil egress pod replicas.
    // +optional
    Replicas *int32 `json:"replicas,omitempty"`
    // EgressDestinations are external subnets that should be routed through
    // this egress attachment (i.e., the subnets for which pods use NAT
    // egress). These populate the Calico IPPool or similar routing rules.
    // +optional
    EgressDestinations []string `json:"egressDestinations,omitempty"`
    // Communities is a list of BGP communities to set on routes exported
    // from this outbound into matched Destination VRFs.
    // +optional
    Communities []string `json:"communities,omitempty"`
}

// PodNetworkSpec defines the desired state of PodNetwork.
type PodNetworkSpec struct {
    // NetworkRef references a Network CRD by name.
    NetworkRef string `json:"networkRef"`
    // Destinations selects Destination resources by label.
    // +optional
    Destinations *metav1.LabelSelector `json:"destinations,omitempty"`
    // NodeSelector selects which nodes receive this pod network.
    // +optional
    NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`
    // Communities is a list of BGP communities to set on routes exported
    // from this pod network into matched Destination VRFs.
    // +optional
    Communities []string `json:"communities,omitempty"`
}

// --- VRF CRD ---

// VRFSpec defines the desired state of VRF — backbone VRF metadata.
type VRFSpec struct {
    // VRF is the name of the backbone VRF.
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MaxLength=12
    VRF string `json:"vrf"`
    // VNI is the VXLAN Network Identifier for the VRF.
    // When omitted, the controller resolves it from operator config.
    // +optional
    VNI *int `json:"vni,omitempty"`
    // RouteTarget is the BGP route target for the VRF.
    // When omitted, the controller resolves it from operator config.
    // +optional
    RouteTarget *string `json:"routeTarget,omitempty"`
    // Loopbacks defines loopback interfaces for this VRF with optional
    // per-node IP allocation via Cluster API IPAM. Used for mirror VRFs
    // (GRE source IPs) and any VRF needing per-node loopback addresses.
    // +optional
    Loopbacks []VRFLoopback `json:"loopbacks,omitempty"`
}

// VRFLoopback defines a loopback interface within a VRF.
type VRFLoopback struct {
    // Name is the loopback interface name (e.g. "lo.mir").
    Name string `json:"name"`
    // PoolRef references a Cluster API IPAM pool (e.g. InClusterIPPool, InfobloxIPPool).
    // The operator creates an IPAddressClaim per node and uses the allocated IP.
    PoolRef corev1.TypedObjectReference `json:"poolRef"`
}

// --- Destination CRD ---

// DestinationSpec defines a routing target — prefixes reachable via
// either a VRF (HBN) or a static next-hop (non-HBN).
type DestinationSpec struct {
    // VRFRef references a VRF resource by name.
    // When set, the controller resolves VNI, RT, and loopback config from
    // the VRF. Prefixes become VRF import entries.
    // Mutually exclusive with NextHop.
    // +optional
    VRFRef *string `json:"vrfRef,omitempty"`
    // Prefixes lists the subnets reachable via this destination.
    // With vrfRef: these become import entries in the VRF configuration.
    // With nextHop: these become static routes via the next-hop address.
    // Defined once here, inherited by every attachment that selects this destination.
    // +optional
    Prefixes []string `json:"prefixes,omitempty"`
    // NextHop specifies next-hop addresses for static routing in non-HBN mode.
    // When a Layer2Attachment with interfaceRef selects this Destination,
    // the controller creates static routes for each prefix via the next-hop.
    // A Destination with prefixes: ["0.0.0.0/0"] + NextHop becomes a default route.
    // Mutually exclusive with VRFRef.
    // +optional
    NextHop *NextHopConfig `json:"nextHop,omitempty"`
    // Note: Community is NOT on Destination — it belongs on the usage CRDs
    // (Layer2Attachment, Inbound, Outbound, PodNetwork) because different
    // attachments may tag exports to the same VRF with different communities.

    // Note: SBR (source-based routing) is NOT part of the Destination API.
    // SBR is a cross-attachment concern — it's needed when two different
    // attachments on the same node reach different VRFs whose imported
    // prefixes overlap. The controller auto-detects this by comparing
    // prefix sets across all resolved attachments per node group, and
    // generates intermediate local VRFs (s-<vrf>) + policy routes
    // automatically. Users never configure SBR on the intent layer.
    // The low-level VRFRouteConfiguration.sbrPrefixes remains as an
    // escape hatch for edge cases.
}

// NextHopConfig specifies next-hop addresses for static routing.
// Used in non-HBN mode: the controller creates static routes for each
// Destination prefix via these addresses on the VLAN sub-interface.
// At least one of IPv4 or IPv6 must be set.
type NextHopConfig struct {
    // IPv4 is the IPv4 next-hop address (e.g. "198.51.100.1").
    // +optional
    IPv4 *string `json:"ipv4,omitempty"`
    // IPv6 is the IPv6 next-hop address (e.g. "2001:db8:100::1").
    // +optional
    IPv6 *string `json:"ipv6,omitempty"`
}

// CollectorSpec defines the desired state of Collector.
// Collector is the intent-level equivalent of MirrorTarget —
// it binds a GRE endpoint to a mirror VRF.
type CollectorSpec struct {
    // Address is the remote collector's IP address.
    Address string `json:"address"`
    // Protocol is the GRE encapsulation type.
    // +kubebuilder:validation:Enum=l3gre;l2gre
    Protocol string `json:"protocol"`
    // Key is an optional GRE tunnel key.
    // +optional
    Key *uint32 `json:"key,omitempty"`
    // MirrorVRF references the VRF CRD representing the mirror VRF.
    MirrorVRF MirrorVRFRef `json:"mirrorVRF"`
}

// MirrorVRFRef references a VRF CRD for the mirror VRF.
type MirrorVRFRef struct {
    // Name of the VRF resource.
    Name string `json:"name"`
}

// TrafficMirrorSpec defines the desired state of TrafficMirror.
// TrafficMirror is the intent-level equivalent of MirrorSelector —
// it binds a source attachment + traffic match to a Collector.
type TrafficMirrorSpec struct {
    // Source identifies the attachment whose traffic should be mirrored.
    Source MirrorSource `json:"source"`
    // Collector is the name of the Collector resource to send mirrored traffic to.
    Collector string `json:"collector"`
    // Direction specifies whether to mirror ingress or egress traffic.
    // +kubebuilder:validation:Enum=ingress;egress
    Direction string `json:"direction"`
    // TrafficMatch optionally filters which traffic to mirror.
    // If omitted, all traffic in the specified direction is mirrored.
    // +optional
    TrafficMatch *TrafficMatch `json:"trafficMatch,omitempty"`
}

// MirrorSource identifies an attachment to mirror traffic from.
type MirrorSource struct {
    // Kind is the type of attachment: Layer2Attachment, Inbound, or Outbound.
    // +kubebuilder:validation:Enum=Layer2Attachment;Inbound;Outbound
    Kind string `json:"kind"`
    // Name is the name of the attachment resource.
    Name string `json:"name"`
}

// --- NodeNetworkStatus CRD ---

// NodeNetworkStatusStatus holds the agent-reported network inventory for a node.
// The spec is empty — all data is agent-populated.
type NodeNetworkStatusStatus struct {
    // Interfaces lists the network interfaces on the node,
    // excluding pod veths and container-internal interfaces.
    Interfaces []NodeInterface `json:"interfaces,omitempty"`
    // Routes lists the routes on the node (excluding per-pod and link-local routes).
    Routes []NodeRoute `json:"routes,omitempty"`
    // LastUpdated is the timestamp of the last agent report.
    LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`
    // Conditions describe the current state of the node's network inventory.
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NodeInterface describes a network interface on a node.
type NodeInterface struct {
    // Name is the interface name (e.g. "eth0", "bond0", "vlan.234").
    Name string `json:"name"`
    // MAC is the hardware address.
    // +optional
    MAC *string `json:"mac,omitempty"`
    // MTU is the interface MTU.
    // +optional
    MTU *int `json:"mtu,omitempty"`
    // State is the link state (e.g. "up", "down").
    State string `json:"state"`
    // Type is the interface type (e.g. "bond", "vlan", "physical").
    // +optional
    Type *string `json:"type,omitempty"`
    // Members lists bonded member interfaces (for type=bond).
    // +optional
    Members []string `json:"members,omitempty"`
    // Parent is the parent interface name (for type=vlan).
    // +optional
    Parent *string `json:"parent,omitempty"`
    // VlanID is the VLAN ID (for type=vlan).
    // +optional
    VlanID *int `json:"vlanID,omitempty"`
    // Addresses lists the IP addresses assigned to this interface.
    // +optional
    Addresses []string `json:"addresses,omitempty"`
}

// NodeRoute describes a route on a node.
type NodeRoute struct {
    // Destination is the route destination in CIDR notation.
    Destination string `json:"destination"`
    // Gateway is the next-hop gateway address.
    // +optional
    Gateway *string `json:"gateway,omitempty"`
    // Interface is the outgoing interface name.
    Interface string `json:"interface"`
    // Table is the routing table name (e.g. "main").
    // +optional
    Table *string `json:"table,omitempty"`
}
```

## 8. Open Questions

All open questions have been resolved. See the Decision Record (§10) for rationale.

1. ~~**Pipeline integration approach (§3.4):**~~ **RESOLVED — Option A (keep low-level CRDs as an option).** Intent CRDs generate intermediate low-level CRDs. Low-level CRDs remain available as a direct-use escape hatch for advanced users but are not the primary interface. See D24.

2. ~~**Destination granularity:**~~ **RESOLVED — Aggregation is user-steerable; default is per-network.** Aggregation is a broader topic but should be controllable by the user. By default, aggregation happens for a whole network and does not extend beyond it. Per-attachment `disableAggregation` overrides this default. See D25.

3. ~~**SBR auto-detection implementation:**~~ **RESOLVED — Always auto-detect, always default to SBR.** The controller eagerly generates SBR whenever *any* prefix overlap exists across attachments on the same node group. This is the safest approach — it avoids subtle routing issues even if it generates more intermediate VRFs. The implementation will be "messy" but correctness trumps elegance. See D26.

4. ~~**Destination label conventions:**~~ **RESOLVED — Leave labelling to users, governed by internal docs.** No standard labels are mandated in the API. Internal documentation will provide recommended conventions (e.g., `network.t-caas.telekom.com/vrf`, `network.t-caas.telekom.com/zone`) but these are guidance, not enforcement. See D27.

5. ~~**Multi-destination selectors:**~~ **RESOLVED — Multi-destination is required from the start.** Multi-destination support is needed for real-world use cases and must be available from the first iteration. See D28.

6. ~~**`networkRef` optionality:**~~ **RESOLVED — `networkRef` is always required.** The `Network` CRD assigns IPs (or at minimum defines the L2 segment), so a `networkRef` is always needed. Even trivial cases benefit from having the network defined as a separate resource. See D29.

7. ~~**MetalLB / Coil integration details:**~~ **RESOLVED — Target the latest current versions; partly decouple the logic.** The implementation targets the latest stable versions of MetalLB and Coil at the time of development. The integration logic should be partly decoupled from the core intent controller so that version-specific details can be updated independently. See D30.

8. ~~**Bidirectional connections:**~~ **RESOLVED — Bidirectional is Inbound + Outbound combined. Consider bundling into a `Gress` CRD.** A bidirectional connection is semantically an `Inbound` + `Outbound` on the same network. Rather than adding a separate `Bidirectional` CRD, a future `Gress` CRD could bundle both roles into a single resource for convenience. This is deferred — users can create an `Inbound` + `Outbound` pair for now. See D31.

9. ~~**Escape hatch interaction:**~~ **RESOLVED — Reject low-level CRDs when high-level is configured.** When intent-based CRDs are present for a given VRF/network, the operator completely rejects conflicting low-level CRDs. Users must remove all low-level CRDs for that scope before the operator processes the intent-based ones. This avoids ambiguity and partial overlaps. See D32.

10. ~~**VNI requirement on Layer2Attachment vs. Destination:**~~ **RESOLVED — L2 also needs a VNI; use a cluster VNI range for auto-assignment, or allow explicit configuration.** L2 VXLAN VNIs are distinct from L3/VRF VNIs. The `Network` CRD carries the L2 VNI (`spec.vni`). For automatic assignment, a cluster-level VNI range is defined and VNIs are allocated from it. When attaching to an existing L2 segment with a known VNI, the user configures it explicitly on the `Network`. See D33.

11. ~~**Collector / TrafficMirror and Proposal 01 coexistence:**~~ **RESOLVED — Low-level mirror CRDs co-exist.** `MirrorTarget` and `MirrorSelector` remain available for direct use (same principle as OQ 9). When intent-based mirror CRDs (`Collector` / `TrafficMirror`) are present for a given scope, conflicting low-level mirror CRDs are rejected. See D34.

12. ~~**Mirror VRF Destination lifecycle:**~~ **RESOLVED — Mirror VRF must select `vrfRef`.** The `Collector.mirrorVRF` must reference an existing `VRF` resource. The user pre-creates the mirror `VRF` (with loopbacks). Auto-creation is not supported — the user controls VRF metadata explicitly. See D35.

13. ~~**GRE interface naming with multiple Collectors:**~~ **RESOLVED — Hash-based naming is acceptable.** `gre.<hash>` / `gretap.<hash>` is the chosen naming scheme. See D36.

14. ~~**Collector node scope:**~~ **RESOLVED — Implicit derivation only, configured when needed.** The collector's GRE tunnels are only configured on nodes where a `TrafficMirror` selects something. No explicit `nodeSelector` on `Collector` — the scope is fully determined by the union of `TrafficMirror` sources' `nodeSelector` values. See D37.

15. ~~**Non-HBN `interfaceRef` validation:**~~ **RESOLVED — Add a `NodeNetworkStatus` CRD.** A new `NodeNetworkStatus` CRD is introduced (see §4.11). It is populated by agents and lists interfaces (excluding pod veths), routes, and IPs on each node. The operator validates `interfaceRef` against `NodeNetworkStatus` data. See D38.

16. ~~**Non-HBN `Inbound`/`Outbound` without `destinations`:**~~ **RESOLVED — Do not reconcile; provide status info.** When `destinations` is omitted on a cluster that has HBN enabled, the operator does not reconcile VRF plumbing (as expected) and sets a status condition informing the user that no VRF configuration was generated. It does not reject the resource — non-HBN mode is always valid. See D39.

17. ~~**Unmanaged / external networks:**~~ **RESOLVED — Yes, support unmanaged networks.** `Network` supports a `managed: false` flag for pre-existing infrastructure-provided networks where the operator should not attempt L2 provisioning. The operator treats the `Network` as a reference for IP/VLAN parameters only. See D40.

18. ~~**`Network` reuse across multiple attachment types:**~~ **RESOLVED — Controller enforces no IP conflicts.** When a `Network` is referenced by multiple usage CRDs simultaneously (e.g., `Layer2Attachment` + `Inbound`), the controller must validate that IP allocations do not overlap. VLAN/VNI consistency is implicitly guaranteed (they come from the same `Network` resource). See D41.

19. ~~**VRF configuration source (`vrfRef` vs. inline on `Destination`):**~~ **RESOLVED — Option (c).** Introduced a dedicated `VRF` CRD that holds VNI, route target, and loopbacks. `Destination` references it via `vrfRef` and carries only `prefixes` + routing mode (`vrfRef` or `nextHop`). See D22, D23.

## 9. Considerations

### 9.1 Future: Automatic Network Allocation

The `Network` CRD API reserves `allocationPool` fields (per-AF: `ipv4`, `ipv6`) that are not processed in this iteration. A future enhancement can add a management cluster controller that:

- Watches `Network` CRDs across tenant clusters.
- Allocates address ranges via BM4X, OpenStack, or vSphere APIs based on the `allocationPool` class.
- Writes allocated CIDRs back into the `Network`'s `.status` or into a dedicated allocation resource.

This is a **transparent enhancement** — the `Network` CRD API does not change, only the controller gains the ability to fill in unspecified parameters from external allocators. For now, users must specify all network parameters (`ipv4.cidr`, `ipv6.cidr`, `vlan`, `vni`) directly in the `Network` resource.

### 9.2 Incremental Delivery

Given the breadth of the design, we prioritize delivery of value:

| Priority | Component | Rationale |
|---|---|---|
| P0 | `VRF` CRD + controller | Foundation — holds VRF metadata (VNI, RT, loopbacks) referenced by Destinations |
| P0 | `Network` CRD + controller | Foundation — all usage CRDs depend on it for pool definition |
| P0 | `Destination` CRD + controller | Foundation — all other intent CRDs depend on it for routing targets |
| P0 | `Layer2Attachment` (non-SRIOV, multi-destination, HBN) | Most common use case; highest ops burden today. Multi-destination required from the start (D28) |
| P0 | `Inbound` (multi-destination, HBN) | Load-balanced services are the primary connectivity need |
| P0 | `Inbound` (non-HBN, MetalLB only) | Common non-HBN scenario — MetalLB pool + advertisement |
| P0 | SBR auto-detection | Always on, always default (D26); must be part of the first delivery |
| P1 | `Layer2Attachment` (SR-IOV) | SR-IOV specific logic |
| P1 | `Layer2Attachment` (non-HBN, `interfaceRef`) | Physical interface / bond / SR-IOV VF without HBN |
| P1 | `Outbound` (HBN) | Egress NAT is needed but less frequent |
| P1 | `Outbound` (non-HBN) | Coil Egress without VRF plumbing |
| P1 | `NodeNetworkStatus` CRD + agent reporting | Enables `interfaceRef` validation; operational visibility |
| P2 | `BGPNeighbor` | Advanced use case, existing workarounds exist |
| P2 | `PodNetwork` | Lower demand; Calico integration complexity |
| P2 | `Collector` + `TrafficMirror` (single collector, single source) | Depends on Proposal 01 low-level implementation; high value for observability |
| P2 | `Gress` convenience CRD (bundled Inbound + Outbound) | Deferred; users create Inbound + Outbound pair for now (D31) |
| P3 | Automatic network allocation (mgmt cluster) | Can be manual/user-specified initially |
| P3 | DNS integration | Can be manual initially |

## 10. Decision Record

| # | Decision | Rationale |
|---|---|---|
| D1 | Introduce intent-based CRDs (`VRF`, `Network`, `Destination`, `Layer2Attachment`, `Inbound`, `Outbound`, `BGPNeighbor`, `PodNetwork`, `Collector`, `TrafficMirror`, `NodeNetworkStatus`) | Reduce configuration complexity; enable tenant self-service; provide node network observability |
| D2 | **`Destination` as a first-class, labeled, referenceable CRD** — VRFs are defined once and selected by label from attachments | Avoids VRF duplication across attachments; preserves composability of today's multi-`VRFRouteConfiguration` merging; enables grouping and loose coupling |
| D3 | **RESOLVED — Pipeline integration: Option A** (Intent → Low-Level CRDs → Revision) — intent controllers generate `Layer2NetworkConfiguration`, `VRFRouteConfiguration`, `BGPPeering`; existing `ConfigReconciler` remains unchanged | Option A is simpler, preserves the existing pipeline, and allows generated low-level CRDs to be inspected. Low-level CRDs remain as an escape hatch. See D24 |
| D4 | All controllers run in the **tenant cluster only** — no management cluster component in this iteration | Simplifies architecture; auto-allocation can be added later transparently |
| D5 | All network parameters (VLAN, VNI, subnet, IPs) are **user-specified** — no automatic allocation | Reduces complexity; `allocationPool` fields are reserved in the `Network` CRD API |
| D6 | **`Network` CRD as pure pool definition** — CIDR, VLAN, VNI, allocation pool. Referenced by name via `networkRef` from usage CRDs. No VRFs, no node scope. | Separates pool definition from pool usage. A `Network` is not per se L2 — it only becomes L2 when a `Layer2Attachment` attaches it. Mirrors SchiffCluster's `AdditionalNetwork` / `Ingress.fromAdditionalNetwork` pattern |
| D7 | Coexistence of intent-based and low-level CRDs during migration | Non-disruptive adoption; escape hatch for edge cases |
| D8 | Prioritize `VRF` + `Network` + `Destination` + `Layer2Attachment` (non-SRIOV) + `Inbound` for first iteration | Highest value, most common use cases, fastest ops burden reduction |
| D9 | Bidirectional = Inbound + Outbound combined; a convenience `Gress` CRD may bundle both roles in a future iteration | Reduces first-iteration scope; inbound + outbound cover the majority of use cases. See D31 |
| D10 | Integrate intent controllers into the existing network-operator | Avoid deploying a separate controller; leverage existing RBAC, scheme, and manager setup |
| D11 | **`Collector` + `TrafficMirror` split** — `Collector` (GRE endpoint + mirror VRF binding, defined once) and `TrafficMirror` (source + direction + filter, per-flow) | Same pattern as `Destination`: shared infrastructure defined once, referenced many times. Avoids duplicating collector config across mirror rules. Maps cleanly to low-level `MirrorTarget` / `MirrorSelector` |
| D12 | **Mirror VRF modeled as a `VRF` with `loopbacks`** — `Collector.mirrorVRF` references a `VRF` resource | Reuses existing VRF metadata patterns; loopback + IPAM allocation flows through the standard pipeline; multiple Collectors can share the same mirror VRF |
| D13 | **Split `VRFAttachment` into `Inbound` + `Outbound`** — separate CRDs for ingress (MetalLB + optional controller) and egress (Coil + Calico) | Ingress and egress are semantically distinct (different IP allocation logic, different platform resources, different user intent). The SchiffCluster API already models them as separate top-level concepts (`network.ingress[]` vs `network.egress[]`). Splitting avoids a polymorphic `connections[]` array and keeps each CRD self-contained |
| D14 | **Non-HBN support via optional `destinations`** — when `destinations` is omitted, only platform resources (MetalLB / Coil) are created without VRF plumbing. `Layer2Attachment.interfaceRef` enables attaching VLANs to physical interfaces | Enables use on clusters without HBN (e.g. MetalLB-only load balancing, physical NIC / bond / SR-IOV VF L2 networks). Keeps the same CRD API surface — HBN vs non-HBN is determined by presence of `destinations`, not a separate CRD |
| D15 | **SBR is a controller implementation detail, not an API surface** — auto-detected when two attachments on the same node group reach destinations with overlapping imported prefixes | SBR is a cross-attachment concern: the user creating Inbound "web" may not know that Inbound "api" exists on the same nodes with overlapping prefixes. Making it user-configured would require global knowledge and leak infrastructure complexity into the intent layer. The low-level `VRFRouteConfiguration.sbrPrefixes` remains as an escape hatch |
| D16 | **`allocationPool` is independently configurable per address family** — `allocationPool.ipv4` and `allocationPool.ipv6` are separate fields | IPv4 and IPv6 addresses often come from different upstream pools (e.g. BM4X `private/cndtag` for IPv4, `global/cndtag` for IPv6). Matches SchiffCluster's `Harmonization.Level` / `LevelV6` pattern |
| D17 | **`nodeSelector` (Kubernetes label selector) replaces `workerGroups` (string array)** — all CRDs use `metav1.LabelSelector` for node scoping | More flexible than hardcoded worker group names. Supports arbitrary label combinations, set-based requirements, and standard Kubernetes selection semantics. Users can target nodes by role, zone, hardware type, or any custom label |
| D18 | **`Network` supports pure L2 segments (IP-optional)** — `ipv4` and `ipv6` are both independently optional. A `Network` with only `vlan` (and no CIDRs) is valid. | Real-world non-HBN deployments commonly provision VLANs on bonds without any IP assignment (e.g., for vSphere / OpenStack VM attachment, or SR-IOV workloads that manage their own IPs). Requiring at least one IP AF would force artificial dummy CIDRs. CRDs that need IPs (`Inbound`, `Outbound`) validate at their level that the referenced `Network` has the required AF |
| D19 | **Bond creation and SR-IOV VF provisioning are infrastructure-level concerns, out of scope** — the operator consumes existing bonds (`interfaceRef`) and VFs (`sriov.enabled`) but does not create them | Bond and VF lifecycle varies per platform (CaaS NetworkConfiguration, cloud-init, netplan, systemd-networkd) and is managed through GitOps or machine configuration. Mixing infrastructure provisioning with intent-based network configuration would conflate two different concerns and add platform-specific complexity |
| D20 | **`communities` is a list on the usage CRDs, not a single string on `Destination`** — each `Layer2Attachment`, `Inbound`, `Outbound`, `PodNetwork` carries its own `communities: []string` | Different attachments reaching the same VRF may need different community tags (e.g., one attachment exports with `64500:999`, another with `64500:1000`). Putting community on the Destination would force all attachments to share the same value. This matches the low-level model where `VRFRouteConfiguration.community` is per-attachment, not per-VRF. A list (not a single string) supports multiple communities per export |
| D21 | **`nextHop` on `Destination` for non-HBN static routing** — specifies per-AF next-hop addresses used to create static routes for the destination's prefixes | Non-HBN L2 segments may need routing to upstream networks without VRF plumbing. Placing `nextHop` on `Destination` (rather than a gateway on `Layer2Attachment`) generalises the concept: any prefix list + next-hop = static route. A default gateway is just `prefixes: ["0.0.0.0/0"]` + `nextHop`. In HBN mode, `nextHop` is ignored — VRF imports handle routing |
| D22 | **RESOLVED — VRF configuration source: separate `VRF` CRD (option c)** — `VRF` holds backbone metadata (name, VNI, RT, loopbacks); `Destination` references it via `vrfRef` and carries only prefixes + routing mode | Separates VRF identity/metadata from routing targets. VRF metadata is defined once; multiple Destinations can reference the same VRF with different prefix sets. Resolves Open Question 19 |
| D23 | **`VRF` CRD introduced; `Destination` split into routing-only** — `vrfRef` (HBN, VRF import routing) and `nextHop` (non-HBN, static routing) are mutually exclusive on `Destination` | VRF = metadata (*where* in the overlay), Destination = routing (*what* is reachable and *how* to reach it). This separation makes the model uniform: a Destination is always "prefixes + forwarding method", whether that method is a VRF or a static next-hop |
| D24 | **Pipeline integration: Option A — intent CRDs generate low-level CRDs** — low-level CRDs remain available as a direct-use escape hatch | Keeps the existing `ConfigReconciler` unchanged; generated low-level CRDs can be inspected for debugging. Low-level CRDs are not the primary interface but remain an option for advanced users. Resolves OQ 1 |
| D25 | **Aggregation is user-steerable, default per-network** — aggregation happens for a whole network by default and does not extend beyond it; per-attachment `disableAggregation` overrides | Sensible default: users should expect that their network's prefixes are aggregated. Cross-network aggregation would be surprising. The override provides an escape valve. Resolves OQ 2 |
| D26 | **SBR is always auto-detected, always default** — the controller eagerly generates SBR whenever *any* prefix overlap exists across attachments on the same node group | Correctness trumps elegance. Missing SBR causes subtle routing failures; extra intermediate VRFs are harmless. The implementation may be complex but the user-facing API stays clean. Resolves OQ 3 |
| D27 | **Label conventions are user-managed, governed by internal docs** — no standard labels mandated in the API | Labels are inherently organisation-specific. Mandating them in the API creates friction for teams with different naming conventions. Internal docs recommend patterns (e.g., `network.t-caas.telekom.com/vrf`, `…/zone`). Resolves OQ 4 |
| D28 | **Multi-destination selector support from the start** — the first iteration supports selectors that match multiple `Destination` resources | Real-world topologies require multi-VRF connectivity. Restricting to single-destination would force users into workarounds. The merge logic mirrors today's multi-`VRFRouteConfiguration` merge. Resolves OQ 5 |
| D29 | **`networkRef` is always required** — even trivial cases must reference a `Network` CRD | The `Network` CRD assigns IPs (or at minimum defines the L2 segment). Making it optional would create two code paths and complicate validation. A simple `Network` is trivial to create. Resolves OQ 6 |
| D30 | **Target latest stable MetalLB / Coil versions; partly decouple integration logic** — version-specific details can be updated independently of the core intent controller | Avoids coupling the intent controller tightly to a specific MetalLB/Coil release. The integration layer can be swapped or updated as versions evolve. Resolves OQ 7 |
| D31 | **Bidirectional = Inbound + Outbound combined; consider future `Gress` CRD** — users create an `Inbound` + `Outbound` pair for now; a convenience `Gress` CRD that bundles both may be added later | Adding a third CRD (`Gress`) now would increase scope without clear value. The Inbound + Outbound pair is functionally equivalent. If demand for a convenience resource emerges, it can be added without breaking the existing API. Resolves OQ 8 |
| D32 | **Reject low-level CRDs when intent-based CRDs are configured for the same scope** — the operator completely rejects conflicting low-level CRDs until they are removed | Avoids ambiguity from partial overlaps. Clear ownership: either intent CRDs or low-level CRDs control a given VRF/network, never both simultaneously. Resolves OQ 9 |
| D33 | **L2 VNI on `Network`; cluster VNI range for auto-assignment or explicit configuration** — L2 VXLAN VNIs are distinct from L3/VRF VNIs; a cluster-level range enables auto-assignment; explicit config for existing segments | L2 and L3 VNIs serve different purposes. Auto-assignment from a range reduces manual coordination; explicit config supports attaching to pre-existing L2 VXLAN segments. Resolves OQ 10 |
| D34 | **Low-level mirror CRDs (`MirrorTarget`, `MirrorSelector`) co-exist with intent mirror CRDs** — same rejection rule as D32 applies per scope | Consistent with D32: intent-level and low-level can co-exist in the cluster, but not for the same scope. Resolves OQ 11 |
| D35 | **Mirror VRF must be a pre-created `VRF` resource referenced via `Collector.mirrorVRF`** — no auto-creation of VRFs | Users control VRF metadata explicitly. Auto-creation would require the operator to pick VNI/RT values, which conflicts with the principle that all network parameters are user-specified (D5). Resolves OQ 12 |
| D36 | **GRE interface naming: hash-based** — `gre.<hash>` / `gretap.<hash>` | Deterministic, unique, fits within the 15-char kernel limit. Resolves OQ 13 |
| D37 | **Collector node scope: implicit only, configured when needed** — GRE tunnels are created only on nodes where a `TrafficMirror` selects something; no explicit `nodeSelector` on `Collector` | Avoids waste: tunnels only exist where mirroring is active. The implicit derivation (union of TrafficMirror sources' nodeSelectors) is correct by construction. Resolves OQ 14 |
| D38 | **`NodeNetworkStatus` CRD introduced for node inventory** — lists interfaces (excluding pod veths), routes, and IPs per node; populated by agents | Enables `interfaceRef` validation against real node state. Also useful for debugging and operational visibility. Resolves OQ 15 |
| D39 | **Non-HBN without `destinations`: do not reconcile VRF, provide informational status** — the operator sets a status condition but does not reject the resource | Non-HBN mode is always valid regardless of cluster capabilities. The status condition informs users that no VRF plumbing was generated, which may or may not be intentional. Resolves OQ 16 |
| D40 | **`Network` supports `managed: false` for unmanaged/external networks** — the operator treats the `Network` as a reference for IP/VLAN parameters only, without attempting L2 provisioning | Pre-existing infrastructure-provided networks (e.g., VLANs from upstream platforms) should be referenceable without the operator trying to create or modify them. Resolves OQ 17 |
| D41 | **Controller enforces no IP conflicts when `Network` is reused across attachment types** — VLAN/VNI consistency is implicit (same `Network`); IP allocations must not overlap | A `Network` is a shared pool. Multiple consumers can reference it, but the controller must ensure IP ranges don't collide. VLAN/VNI consistency is guaranteed by design since all consumers read from the same `Network` resource. Resolves OQ 18 |
