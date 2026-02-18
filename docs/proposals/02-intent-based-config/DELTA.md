# Delta: Architecture Input → Proposal 02

This document summarises the differences between the **internal architecture input** (`input-doc-from-internal-arch-decision.adoc`, April 2025) and the **proposal** (`README.md`, February 2026). It is intended to make review easier by highlighting what was kept, what changed, and what was added or dropped.

---

## 1. Scope Changes

| Topic | Input Document | Proposal | Reason |
|---|---|---|---|
| **Management-cluster controller** | Included — responsible for DNS, network allocation, and cross-cluster reconciliation | **Removed entirely** — all controllers run in the tenant cluster only | Simplifies first iteration; can be added later as a transparent layer (§3.2) |
| **Automatic network allocation** (BM4X, OpenStack, vSphere) | Core part of the design — `harmonization`, `size`, `networkName` drive allocation | **Deferred** — fields are reserved in the API but not processed; users specify all params directly | Reduces scope; allocation is a transparent enhancement that doesn't change the CRD API (§9.1) |
| **DNS record creation** | Mentioned as a management-cluster responsibility | **Dropped** from this iteration | No controller or CRD is defined for DNS |
| **Traffic mirroring** | Not covered | **Added** — `Collector` and `TrafficMirror` CRDs (§3.6, §4.8, §4.9) | Builds on Proposal 01; mirroring is an ideal fit for intent-based simplification |
| **VRFAttachment → Inbound + Outbound** | Single `VRFAttachment` CRD with `connections[]` array mixing inbound/outbound | **Split into two CRDs**: `Inbound` (MetalLB pool + advertisement) and `Outbound` (Coil Egress + Calico) | Ingress and egress are semantically distinct — different IP allocation logic, different platform resources. SchiffCluster API already models them separately (`network.ingress[]` vs `network.egress[]`) |
| **Non-HBN support** | Not addressed — all examples assumed HBN VXLAN tunneling | **Added** — `destinations` is optional; omitting it produces only platform resources (MetalLB/Coil) without VRF plumbing. `Layer2Attachment.interfaceRef` enables physical interface / bond / SR-IOV VF attachment | Enables use on clusters without HBN infrastructure |
| **`Network` CRD (pool/usage separation)** | Network params embedded inline on each attachment (`spec.network.vlanID`, `spec.network.vni`, etc.) | **Added** — new `Network` CRD is a pure pool definition (CIDR, VLAN, VNI, allocationPool). Usage CRDs reference it via `networkRef`. A Network is not per se L2 — it only becomes L2 when a `Layer2Attachment` attaches it to nodes. **IP addresses are optional** — a `Network` with only `vlan` (no `ipv4`/`ipv6`) represents a pure L2 segment | Separates pool definition from usage. Mirrors SchiffCluster's `AdditionalNetwork` / `fromAdditionalNetwork` pattern. Enables future automatic allocation on a single CRD. Pure L2 segments (VLAN-only, no IP) are common for non-HBN deployments |
| **`nodeSelector` replaces `workerGroups`** | `workerGroups: [wg1, wg2]` — string array referencing named worker groups | **Replaced** with `nodeSelector: metav1.LabelSelector` — standard Kubernetes label selector | More flexible: supports arbitrary label combinations, set-based requirements, and standard K8s selection semantics. No dependency on a specific "worker group" label convention |
| **Infrastructure provisioning out of scope** | Not addressed (input assumed infrastructure already exists) | **Explicitly declared out of scope** — bond creation (e.g., `bond2` from NICs) and SR-IOV VF provisioning (e.g., VF count on NICs) are infrastructure-level concerns managed by external tooling. The operator *consumes* existing bonds via `interfaceRef` and VFs via `sriov.enabled` | Bond and VF lifecycle varies per platform (CaaS NetworkConfiguration, cloud-init, netplan, etc.) and is typically managed through GitOps or machine configuration, not by the network operator |
| **Use-case coverage matrix** | Not present | **Added** (§3.3) — maps four deployment categories (L2 ordering on the fly, GitOps L2 configs, SR-IOV ordering via BM4X, HBN use cases) to CRDs and fields, with a concrete pure-L2 YAML example | Makes coverage explicit; validates the design against real-world production configs |

---

## 2. New Concepts Not in the Input

| Concept | What It Is | Why It Was Added |
|---|---|---|
| **`Network` CRD** (§4.2) | Pure pool definition: IPv4/IPv6 CIDRs with `prefixLength`, VLAN, VNI, per-AF `allocationPool`. Referenced by name via `networkRef` from usage CRDs. No VRFs, no node scope. **IP addresses are optional** — a `Network` with only `vlan` is a valid pure L2 segment. | The input embedded network parameters inline on each attachment. This caused duplication and conflated pool definition with pool usage. `Network` separates them — a pool is defined once and referenced many times. Mirrors SchiffCluster's `AdditionalNetwork`. Pure L2 (VLAN-only, no IP) is common for non-HBN deployments. |
| **Use-case coverage matrix** (§3.3) | Maps four deployment categories to CRDs and fields: (1) L2 ordering on the fly (vSphere/OpenStack), (2) GitOps L2 configs (bonds+VLANs+SR-IOV VFs), (3) SR-IOV ordering (BM4X), (4) HBN. Includes concrete YAML example for pure L2. | Validates the design against real-world production configs; makes coverage explicit. |
| **`VRF` CRD** (§4.1) | A first-class resource representing backbone VRF metadata: name, VNI, route target, loopbacks. Referenced by name from `Destination` via `vrfRef` and from `Collector` via `mirrorVRF`. | Separates VRF identity from routing targets. VRF metadata is defined once; Destination references it and adds only routing concerns (prefixes + forwarding method). Resolves Open Question 19 (option c). |
| **`Destination` CRD** (§4.2) | A routing target: prefixes + either `vrfRef` (HBN — VRF import routing) or `nextHop` (non-HBN — static routing). Referenced by label selector from attachments. | The input used inline VRF lists (`spec.network.VRFs: [vrf_a, vrf_b]`) on every attachment. `Destination` decouples routing from VRF metadata and supports both HBN and non-HBN modes uniformly. |
| **`Collector` CRD** (§4.9) | GRE collector endpoint + mirror VRF binding (`mirrorVRF` references a `VRF` resource). Defined once, referenced by `TrafficMirror`. | Intent-level wrapper around `MirrorTarget` from Proposal 01. Same "define once, reference many" pattern. |
| **`TrafficMirror` CRD** (§4.10) | Per-flow mirroring rule: source attachment, direction, optional traffic match, collector ref. | Intent-level wrapper around `MirrorSelector`. Lightweight, per-flow. |
| **`Inbound` CRD** (§4.5) | Replaces the inbound side of `VRFAttachment`. Allocates IPs from a `Network` (via `networkRef`), creates MetalLB IPAddressPool + advertisement (BGP or L2). Stops at the LB layer — ingress controller deployment is out of scope. | Cleaner separation of concerns: inbound is about *IP allocation + LB pool provisioning*, distinct from egress NAT. Ingress controller lifecycle belongs to the user or a separate tool |
| **`Outbound` CRD** (§4.6) | Replaces the outbound side of `VRFAttachment`. Allocates IPs from a `Network` (via `networkRef`), creates Coil Egress + Calico IPPool. | Egress NAT has its own lifecycle (replicas, egressDestinations) that doesn't belong on an ingress resource |
| **Non-HBN mode** (all attachment CRDs) | When `destinations` is omitted, no VRF plumbing is generated. `Layer2Attachment.interfaceRef` enables direct physical interface attachment without VXLAN. | Supports clusters without HBN: MetalLB-only load balancing, physical NIC / bond / SR-IOV VF L2 networks |
| **Label-selector–based destination binding** | Attachments use `matchLabels` instead of naming VRFs directly. | Enables loose coupling, grouping, and the standard Kubernetes selection pattern. |
| **`nodeSelector` (label selector)** | All CRDs use `metav1.LabelSelector` instead of `workerGroups: []string` for node scoping. | Standard Kubernetes pattern; more flexible than hardcoded worker group name arrays. Supports arbitrary label combinations and set-based requirements. |
| **Pipeline integration as open question** (§3.4) | Option A (generate low-level CRDs) vs. Option B (extend `ConfigReconciler` directly). **RESOLVED: Option A** (D24). | The input assumed the controller would produce the "already existing configuration resources." Option A is chosen — intent controllers generate low-level CRDs. Low-level CRDs remain as an escape hatch. |
| **Translation examples** (§6) | Detailed diagrams showing how `Network` → `Layer2Attachment` → revision pipeline entries and platform resources. | Makes the proposal concrete; not present in the input. |
| **Go type definitions** (§7) | `VRFSpec`, `VRFLoopback`, `NetworkSpec`, `IPNetwork`, `AllocationPool`, `DestinationSpec`, `NextHopConfig`, `CollectorSpec`, `MirrorVRFRef`, `TrafficMirrorSpec`, `InboundSpec`, `OutboundSpec`, `Layer2AttachmentSpec`, `PodNetworkSpec`, `AdvertisementConfig`, `NodeNetworkStatusStatus`, `NodeInterface`, `NodeRoute`, etc. | Implementation guidance; not present in the input. |
| **`NodeNetworkStatus` CRD** (§4.11) | Per-node inventory: interfaces (excl. pod veths), routes, IPs. Agent-populated. | Enables `interfaceRef` validation against real node state; provides operational visibility (D38). |
| **`Network.managed` flag** | `managed: false` for pre-existing/external networks. The operator skips L2 provisioning and treats the Network as a parameter reference only. | Supports infrastructure-provided networks without the operator attempting to create/modify them (D40). |
| **All open questions resolved** (§8) | 18 open questions + OQ 19 are all RESOLVED. Decisions D24–D41 added. | Provides clear direction for implementation; no architectural ambiguity remains. |

---

## 3. CRD-by-CRD Comparison

### 3.1 Layer2Attachment

| Aspect | Input | Proposal | Delta |
|---|---|---|---|
| **Network definition** | Inline `spec.network.vlanID`, `spec.network.vni`, `spec.network.subnet`, etc. | `spec.networkRef` references a `Network` CRD by name. Network params live on the `Network` resource. | Major structural change — network pool definition separated from usage |
| **VRF binding** | `spec.network.VRFs: [vrf_a, vrf_b]` — inline list of VRF names | `spec.destinations.matchLabels: { zone: secure }` — label selector on `Destination` CRDs (which reference `VRF` resources via `vrfRef`). **Optional** — omitting it activates non-HBN mode. | Major structural change — VRFs are now first-class resources; Destinations are routing targets selected by label |
| **Node scoping** | `spec.workerGroups: [wg1]` — string array | `spec.nodeSelector.matchLabels` — standard `metav1.LabelSelector` | More flexible: arbitrary label combinations, set-based requirements |
| **Routes** | `spec.routes[].vrf` + `prefixes` — per-VRF route blocks on the attachment | Import prefixes inherited from `Destination.spec.prefixes`; export derived from `Network` subnet; optional `routes` for extras | Simplifies: most cases need no `routes` at all |
| **Network allocation fields** | `networkName`, `harmonization`, `ipv4.size` — actively used for BM4X/OpenStack/vSphere | Moved to `Network.spec.allocationPool` (per-AF: `ipv4`, `ipv6`). **Not processed** in this iteration | Users must supply CIDRs, VLAN, VNI directly in the `Network` CRD |
| **VNI** | Not on the attachment (implied from network allocation) | On the `Network` CRD (`spec.vni`) — explicitly user-specified | Moved to `Network` CRD |
| **BGP config** | Inline `spec.bgp` block | Inline `spec.bgp` block — **kept as-is** | No change |
| **SR-IOV** | `spec.sriov.enabled` | Same — **kept as-is** | No change |
| **`interfaceRef`** | Not in input | `spec.interfaceRef` — references an existing host interface (physical NIC, bond, SR-IOV VF) for non-HBN deployments | New field enabling non-HBN L2 attachment |
| **`interfaceName`** | Not in input | `spec.interfaceName` — overrides auto-generated name. Defaults to `vlan.<vlanID>` when `interfaceRef` is set. | New field for interface name control |
| **`disableAnycast`** | `spec.disableAnycast` | Same — **kept as-is** | No change |
| **`disableNeighborSuppression`** | Appeared twice in input (likely typo) | Deduplicated — single field | Bug fix |
| **Status** | `sriovVlanID`, `anycast.mac/gateway/gatewayv4` | Same structure, plus `conditions` | Added standard conditions |
| **Validation rules** | Listed as bullet points | Same rules, formatted as a table | Presentation only |
| **Controller behaviour** | Listed as bullet points covering SR-IOV, non-SRIOV, and combined scenarios | Rewritten as a table (§4.2) with same logic | Clearer format |
| **Community** | Not on attachment (community was on VRFRouteConfiguration, per-attachment) | `spec.communities: []string` — list of BGP communities on the attachment, not on Destination. Different attachments to the same VRF can use different communities | Moved to usage level, made a list |
| **Static routing (nextHop)** | Not in input | Static routing for non-HBN moved to `Destination.spec.nextHop.ipv4`/`.ipv6`. When a `Layer2Attachment` with `interfaceRef` selects a Destination, its `prefixes` + `nextHop` become static routes. Default gw is `prefixes: ["0.0.0.0/0"]` + `nextHop` | Routing info belongs on the Destination (which already defines *what* is reachable); the attachment only decides *which* Destinations to select |

### 3.2 Inbound *(was VRFAttachment inbound)*

| Aspect | Input (`VRFAttachment`) | Proposal (`Inbound`) | Delta |
|---|---|---|---|
| **CRD name** | `VRFAttachment` | `Inbound` | **Renamed** — split from the combined VRFAttachment; ingress is now its own CRD |
| **Network definition** | Inline `spec.network.subnet`, `spec.network.vlanID`, etc. | `spec.networkRef` references a `Network` CRD by name | Pool definition separated from usage |
| **VRF binding** | `spec.network.VRFs: [vrf_a, vrf_b]` — inline list | `spec.destinations.matchLabels` — label selector on `Destination`. **Optional** — omitting it activates non-HBN mode. | Major structural change |
| **Node scoping** | `spec.workerGroups: [wg1]` | `spec.nodeSelector` — standard `metav1.LabelSelector` | More flexible |
| **Connections array** | `connections[].direction: inbound`, `count`, `routes`, `disableLoadBalancer` | **Removed** — `count`, `advertisement` are top-level fields. No `disableLoadBalancer` (always creates MetalLB pool). | Simplified: no polymorphic array, each field has a single purpose |
| **MetalLB advertisement** | Implicit (always BGP) | Explicit `spec.advertisement.type: bgp|l2` | User controls BGP vs. L2 advertisement mode |
| **Ingress controller** | Not in input | **Not in proposal** — intentionally out of scope. Inbound stops at MetalLB pool + advertisement. | Ingress controller lifecycle is a separate concern |
| **Network allocation fields** | `harmonization`, `ipv4.size` | Moved to `Network.spec.allocationPool` (per-AF). Reserved but not processed | Same as Layer2Attachment |
| **Non-HBN mode** | Not addressed | Omit `destinations` → MetalLB pool + advertisement only, no VRF plumbing | New capability |
| **Status** | `connections[].addresses` / `addressesv4` | `allocatedIPs`, `metalLBPoolName`, `conditions` | Restructured for single-purpose CRD |
| **Community** | On VRFRouteConfiguration (per-attachment, single string) | `spec.communities: []string` — list of BGP communities on the Inbound, not on Destination | Moved to usage level, made a list |

### 3.3 Outbound *(was VRFAttachment outbound)*

| Aspect | Input (`VRFAttachment`) | Proposal (`Outbound`) | Delta |
|---|---|---|---|
| **CRD name** | `VRFAttachment` | `Outbound` | **Renamed** — egress is now its own CRD |
| **Network definition** | Inline `spec.network.*` | `spec.networkRef` references a `Network` CRD by name | Pool definition separated from usage |
| **VRF binding** | `spec.network.VRFs: [vrf_a, vrf_b]` | `spec.destinations.matchLabels` — optional (non-HBN mode) | Same structural change |
| **Node scoping** | `spec.workerGroups: [wg1]` | `spec.nodeSelector` — standard `metav1.LabelSelector` | More flexible |
| **Connections array** | `connections[].direction: outbound`, `count`, `routes` | **Removed** — `count`, `replicas`, `egressDestinations` are top-level fields | Simplified |
| **Coil / Calico** | Implied by outbound direction | Explicit: `count` IPs for Coil Egress, `egressDestinations` for Calico IPPool | More explicit |
| **Non-HBN mode** | Not addressed | Omit `destinations` → Coil Egress + Calico only, no VRF plumbing | New capability |
| **Status** | `connections[].addresses` / `addressesv4` | `allocatedIPs`, `coilEgressName`, `conditions` | Restructured |
| **Community** | On VRFRouteConfiguration (per-attachment, single string) | `spec.communities: []string` — list of BGP communities on the Outbound, not on Destination | Moved to usage level, made a list |

### 3.4 BGPNeighbor

| Aspect | Input | Proposal | Delta |
|---|---|---|---|
| **Spec** | `allowedConnections`, `advertiseTransferNetwork`, `holdTime`, `keepaliveTime`, `maximumPrefixes`, `workloadAS` | Renamed to `allowedInbounds` referencing `Inbound` resources; other fields **kept as-is** | `allowedConnections` → `allowedInbounds` to match the Inbound CRD name |
| **Status** | `asNumber`, `neighborIPs`, `neighborASNumber`, `workloadASNumber`, timers | **Kept as-is** (IPs changed to documentation prefixes) | Cosmetic only |
| **Validation** | All connections must have `disableLoadBalancer` + `direction: inbound` | Updated: all referenced `Inbound` resources must exist; `disableLoadBalancer` is gone (BGPNeighbor implies no MetalLB LB for those IPs) | Simplified validation |

### 3.5 PodNetwork

| Aspect | Input | Proposal | Delta |
|---|---|---|---|
| **Network definition** | Inline `spec.network.*` | `spec.networkRef` references a `Network` CRD by name | Pool definition separated from usage |
| **VRF binding** | `spec.network.VRFs: [vrf_a, vrf_b]` — inline list | `spec.destinations.matchLabels` | Same structural change |
| **Node scoping** | `spec.workerGroups: [wg1]` | `spec.nodeSelector` — standard `metav1.LabelSelector` | More flexible |
| **Routes** | `spec.routes[].vrf` + `prefixes` | Import prefixes from `Destination`; optional `routes` for extras | Simplified |
| **Network allocation fields** | `harmonization`, `ipv4.size` | Moved to `Network.spec.allocationPool` (per-AF). Reserved but not processed | Same |
| **Controller behaviour** | "configure cluster networking implementation" (Calico) | Expanded: Calico IP pools, network policies, routes toward VRFs | More specific |

### 3.6 Collector & TrafficMirror *(new — not in input)*

These are entirely new CRDs, not present in the input document. See §3.6, §4.8, and §4.9 of the proposal.

---

## 4. Architectural Differences

### 4.1 Controller Topology

| Input | Proposal |
|---|---|
| Two controller groups: **tenant-cluster controllers** (name TBD) + **management-cluster controller** | **Tenant cluster only** — all intent controllers integrated into the existing network-operator |
| Management-cluster controller handles network allocation, DNS, cross-cluster reconciliation | No management-cluster component — deferred |

### 4.2 Pipeline Integration

| Input | Proposal |
|---|---|
| Assumed: intent controller produces "already existing configuration resources" (i.e., `Layer2NetworkConfiguration`, `VRFRouteConfiguration`, `BGPPeering`) — equivalent to **Option A** | **Resolved: Option A** (D24). Intent controllers generate low-level CRDs (`Layer2NetworkConfiguration`, `VRFRouteConfiguration`, `BGPPeering`). Low-level CRDs remain as an escape hatch. Conflicting low-level CRDs are rejected when intent CRDs cover the same scope (D32). |

### 4.3 VRF References

| Input | Proposal |
|---|---|
| Inline VRF name lists on each attachment (`VRFs: [vrf_a, vrf_b]`) | `Destination` CRD + label selectors. Prefix imports inherited, exports derived automatically. |

### 4.4 Network Parameter Source

| Input | Proposal |
|---|---|
| Mix of user-specified and auto-allocated (BM4X, OpenStack); params inline on each attachment | `Network` CRD defines the pool (CIDR, VLAN, VNI, allocationPool). Usage CRDs reference via `networkRef`. All user-specified; `allocationPool` fields reserved for future use |

### 4.5 Node Scoping

| Input | Proposal |
|---|---|
| `workerGroups: [wg1, wg2]` — string array naming worker groups | `nodeSelector: metav1.LabelSelector` — standard Kubernetes label selector with `matchLabels` and `matchExpressions` |

---

## 5. Items Deferred or Dropped

| Item | Status | Notes |
|---|---|---|
| Management-cluster controller | **Deferred** | Can be added when auto-allocation is needed |
| Automatic network allocation (BM4X / OpenStack / vSphere) | **Deferred** | API fields reserved; transparent enhancement |
| DNS record creation | **Dropped** | Not scoped in the proposal |
| `spec.network.networkName` (OpenStack/vSphere) | **Dropped** | Replaced by `Network` CRD — the `Network` resource name serves as the logical network identifier |
| `spec.network.harmonization` (BM4X allocation class) | **Restructured** | Replaced by `Network.spec.allocationPool` with separate `ipv4` and `ipv6` fields. Not processed in this iteration. |
| `spec.network.ipv4.size` (dynamic sizing) | **Replaced** | Replaced by `Network.spec.ipv4.prefixLength` — allocation slice size. Not processed in this iteration. |
| Bidirectional connections | **Deferred** | Bidirectional = Inbound + Outbound combined (D31). A future `Gress` convenience CRD may bundle both roles |
| `Consequences` / `Considerations` sections | **Reworked** | Replaced by §8 Open Questions, §9 Considerations, §10 Decision Record |

---

## 6. Items Kept Unchanged

The following concepts passed through from the input to the proposal with no structural change:

- Overall intent: tenant self-service, intent-based over low-level config.
- `Layer2Attachment`, `BGPNeighbor`, `PodNetwork` as three of the four original CRD names and their core semantics.
- Inbound / outbound separation of concerns (the input had `connections[].direction`; the proposal promotes this to separate CRDs).
- SR-IOV configuration model and validation rules.
- BGP configuration block on `Layer2Attachment`.
- `BGPNeighbor` core spec structure (timers, prefixes, ASN).
- `nodeIPs` configuration.
- Anycast gateway + MAC allocation.
- Validation rules (mostly identical, just reformatted).
- Platform integration targets: MetalLB (inbound), Coil Egress (outbound), Calico (pod networks).

---

## 7. Summary of Key Decisions

| # | Decision | Input Position | Proposal Position |
|---|---|---|---|
| 1 | Management-cluster controller | Included | Removed (tenant-only) |
| 2 | Network allocation | Automatic via BM4X/OpenStack/vSphere | User-specified; `allocationPool` fields reserved per-AF on `Network` CRD |
| 3 | VRF references | Inline name lists | `VRF` CRD (metadata) + `Destination` CRD (routing) + label selectors |
| 4 | Import prefixes | Per-attachment route blocks | On `Destination`, inherited automatically |
| 5 | Pipeline integration | Implicit Option A | **Resolved: Option A** (D24). Low-level CRDs generated by intent controllers; escape hatch preserved. Conflicts rejected (D32) |
| 6 | Traffic mirroring | Not covered | `Collector` + `TrafficMirror` CRDs |
| 7 | DNS | Mentioned | Dropped |
| 8 | Bidirectional connections | "Not first iteration" | Bidirectional = Inbound + Outbound (D31). Future `Gress` convenience CRD considered |
| 9 | `VRFAttachment` → `Inbound` + `Outbound` | Single CRD with `connections[]` array | Split into two CRDs — cleaner separation, matches SchiffCluster API structure |
| 10 | Network pool definition | Inline `spec.network.*` on each attachment | New `Network` CRD — pool defined once, referenced via `networkRef`. Not per se L2 — only becomes L2 when attached. `managed: false` for external networks (D40) |
| 11 | Node scoping | `workerGroups: []string` | `nodeSelector: metav1.LabelSelector` — standard Kubernetes label selector |
| 12 | Per-AF allocation | Single `harmonization` string for both AFs | `allocationPool.ipv4` and `allocationPool.ipv6` — independent per address family |
| 13 | Non-HBN support | Not addressed (HBN assumed) | Optional `destinations` + `interfaceRef` enable non-HBN deployments |
| 14 | Community placement | On `VRFRouteConfiguration` (per-attachment, single string) | `communities: []string` on each usage CRD (`Layer2Attachment`, `Inbound`, `Outbound`, `PodNetwork`), **not** on `Destination`. List, not single string |
| 15 | Static routing for non-HBN L2 | Not addressed | `Destination.nextHop.ipv4`/`.ipv6` — controller creates static routes for the destination's prefixes via the next-hop on the VLAN sub-interface. Default gw = `prefixes: ["0.0.0.0/0"]` + `nextHop` |
| 16 | VRF config source | VNI/RT on VRFRouteConfiguration | Separate `VRF` CRD holds name, VNI, RT, loopbacks. `Destination` references it via `vrfRef`. Resolves OQ 19 (option c) |
| 17 | Aggregation default | Not addressed | User-steerable; default per-network; `disableAggregation` override (D25) |
| 18 | SBR strategy | Not addressed | Always auto-detect, always default to SBR on any prefix overlap (D26) |
| 19 | Label conventions | Not addressed | User-managed, governed by internal docs (D27) |
| 20 | Multi-destination | Not addressed | Required from the start (D28) |
| 21 | `networkRef` optionality | Not addressed | Always required — `Network` assigns IPs / defines L2 segment (D29) |
| 22 | MetalLB / Coil version | Not addressed | Latest stable; integration logic partly decoupled (D30) |
| 23 | L2 VNI | Not addressed | On `Network`; cluster VNI range for auto-assignment or explicit (D33) |
| 24 | Low-level / intent conflict | Not addressed | Reject low-level when intent covers same scope (D32, D34) |
| 25 | Mirror VRF lifecycle | Not addressed | Must pre-create `VRF` resource (D35) |
| 26 | Node network inventory | Not addressed | New `NodeNetworkStatus` CRD — agent-populated (D38) |
| 27 | Unmanaged networks | Not addressed | `Network.managed: false` for external networks (D40) |
| 28 | Network reuse IP conflicts | Not addressed | Controller enforces no IP overlap (D41) |
