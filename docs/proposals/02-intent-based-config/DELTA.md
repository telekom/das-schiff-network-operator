# Delta: Architecture Input → Proposal 02

This document summarises the differences between the **internal architecture input** (`input-doc-from-internal-arch-decision.adoc`, April 2025) and the **proposal** (`README.md`, February 2026). It is intended to make review easier by highlighting what was kept, what changed, and what was added or dropped.

---

## 1. Scope Changes

| Topic | Input Document | Proposal | Reason |
|---|---|---|---|
| **Management-cluster controller** | Included — responsible for DNS, network allocation, and cross-cluster reconciliation | **Removed entirely** — all controllers run in the tenant cluster only | Simplifies first iteration; can be added later as a transparent layer (§3.2) |
| **Automatic network allocation** (BM4X, OpenStack, vSphere) | Core part of the design — `harmonization`, `size`, `networkName` drive allocation | **Deferred** — fields are reserved in the API but not processed; users specify all params directly | Reduces scope; allocation is a transparent enhancement that doesn't change the CRD API (§9.1) |
| **DNS record creation** | Mentioned as a management-cluster responsibility | **Dropped** from this iteration | No controller or CRD is defined for DNS |
| **Traffic mirroring** | Not covered | **Added** — `Collector` and `TrafficMirror` CRDs (§3.5, §4.5, §4.6) | Builds on Proposal 01; mirroring is an ideal fit for intent-based simplification |

---

## 2. New Concepts Not in the Input

| Concept | What It Is | Why It Was Added |
|---|---|---|
| **`Destination` CRD** (§3.4) | A first-class, labeled resource representing a reachability target (VRF). Referenced by label selector from attachments. Carries `prefixes` (reachable subnets), `vni`, `routeTarget`, optional `loopbacks`. | The input used inline VRF lists (`spec.network.VRFs: [vrf_a, vrf_b]`) on every attachment. This caused duplication and lost the composability of today's `VRFRouteConfiguration` merging. `Destination` solves both. |
| **`Collector` CRD** (§4.5) | GRE collector endpoint + mirror VRF binding. Defined once, referenced by `TrafficMirror`. | Intent-level wrapper around `MirrorTarget` from Proposal 01. Same "define once, reference many" pattern as `Destination`. |
| **`TrafficMirror` CRD** (§4.6) | Per-flow mirroring rule: source attachment, direction, optional traffic match, collector ref. | Intent-level wrapper around `MirrorSelector`. Lightweight, per-flow. |
| **Label-selector–based destination binding** | Attachments use `matchLabels` instead of naming VRFs directly. | Enables loose coupling, grouping, and the standard Kubernetes selection pattern. |
| **Pipeline integration as open question** (§3.3) | Option A (generate low-level CRDs) vs. Option B (extend `ConfigReconciler` directly). | The input assumed the controller would produce the "already existing configuration resources." The proposal formalises both approaches with trade-offs. |
| **Shared `NetworkSpec` Go type** (§7) | Single type reused by `Layer2Attachment`, `VRFAttachment`, `PodNetwork`. | DRY; the input had slightly different network blocks per CRD. |
| **Translation examples** (§6) | Detailed diagrams showing how intent CRDs map to revision pipeline entries and platform resources. | Makes the proposal concrete; not present in the input. |
| **Go type definitions** (§7) | `DestinationSpec`, `CollectorSpec`, `TrafficMirrorSpec`, `NetworkSpec`, `IPv4Config`, etc. | Implementation guidance; not present in the input. |

---

## 3. CRD-by-CRD Comparison

### 3.1 Layer2Attachment

| Aspect | Input | Proposal | Delta |
|---|---|---|---|
| **VRF binding** | `spec.network.VRFs: [vrf_a, vrf_b]` — inline list of VRF names | `spec.destinations.matchLabels: { zone: secure }` — label selector on `Destination` CRDs | Major structural change — VRFs are now first-class `Destination` resources selected by label |
| **Routes** | `spec.routes[].vrf` + `prefixes` — per-VRF route blocks on the attachment | Import prefixes inherited from `Destination.spec.prefixes`; export derived from attachment subnet; optional `routes` for extras | Simplifies: most cases need no `routes` at all |
| **Network allocation fields** | `networkName`, `harmonization`, `ipv4.size` — actively used for BM4X/OpenStack/vSphere | Fields reserved but **not processed** in this iteration | Users must supply `vlanID`, `vni`, `subnet` directly |
| **VNI** | Not on the attachment (implied from network allocation) | `spec.network.vni` — explicitly user-specified | Because allocation is deferred |
| **BGP config** | Inline `spec.bgp` block | Inline `spec.bgp` block — **kept as-is** | No change |
| **SR-IOV** | `spec.sriov.enabled` | Same — **kept as-is** | No change |
| **`disableAnycast`** | `spec.disableAnycast` | Same — **kept as-is** | No change |
| **`disableNeighborSuppression`** | Appeared twice in input (likely typo) | Deduplicated — single field | Bug fix |
| **Status** | `sriovVlanID`, `anycast.mac/gateway/gatewayv4` | Same structure, plus `conditions` | Added standard conditions |
| **Validation rules** | Listed as bullet points | Same rules, formatted as a table | Presentation only |
| **Controller behaviour** | Listed as bullet points covering SR-IOV, non-SRIOV, and combined scenarios | Rewritten as a table (§4.2) with same logic | Clearer format |

### 3.2 VRFAttachment

| Aspect | Input | Proposal | Delta |
|---|---|---|---|
| **VRF binding** | `spec.network.VRFs: [vrf_a, vrf_b]` — inline list | `spec.destinations.matchLabels` — label selector on `Destination` | Same structural change as Layer2Attachment |
| **Routes on connections** | `connections[].routes[].vrf` + `prefixes` — per-VRF per-connection | Import prefixes inherited from `Destination.spec.prefixes`; `connections[].routes` for extras | Simplified |
| **Network allocation fields** | `harmonization`, `ipv4.size` | Reserved but not processed | Same as Layer2Attachment |
| **Connections** | `direction: inbound/outbound(/bidirectional)`, `disableLoadBalancer`, `count`, `routes` | Same fields, bidirectional noted as "not first iteration" — **kept as-is** | No change |
| **Status** | `connections[].addresses` / `addressesv4` | Same, plus `conditions` | Added conditions |
| **Controller behaviour** | Bullet list: MetalLB inbound, Coil outbound, Calico pools, PBR | Rewritten as table per direction (§4.2) | Clearer format |

### 3.3 BGPNeighbor

| Aspect | Input | Proposal | Delta |
|---|---|---|---|
| **Spec** | `allowedConnections`, `advertiseTransferNetwork`, `holdTime`, `keepaliveTime`, `maximumPrefixes`, `workloadAS` | **Kept as-is** | No change |
| **Status** | `asNumber`, `neighborIPs`, `neighborASNumber`, `workloadASNumber`, timers | **Kept as-is** (IPs changed to documentation prefixes) | Cosmetic only |
| **Validation** | All connections must have `disableLoadBalancer` + `direction: inbound` | **Kept as-is** | No change |

### 3.4 PodNetwork

| Aspect | Input | Proposal | Delta |
|---|---|---|---|
| **VRF binding** | `spec.network.VRFs: [vrf_a, vrf_b]` — inline list | `spec.destinations.matchLabels` | Same structural change |
| **Routes** | `spec.routes[].vrf` + `prefixes` | Import prefixes from `Destination`; optional `routes` for extras | Simplified |
| **Network allocation fields** | `harmonization`, `ipv4.size` | Reserved but not processed | Same |
| **Controller behaviour** | "configure cluster networking implementation" (Calico) | Expanded: Calico IP pools, network policies, routes toward VRFs | More specific |

### 3.5 Collector & TrafficMirror *(new — not in input)*

These are entirely new CRDs, not present in the input document. See §3.5, §4.5, and §4.6 of the proposal.

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
| Assumed: intent controller produces "already existing configuration resources" (i.e., `Layer2NetworkConfiguration`, `VRFRouteConfiguration`, `BGPPeering`) — equivalent to **Option A** | Formalised as an **open design question** with two options (§3.3): Option A (generate low-level CRDs) and Option B (extend `ConfigReconciler` to watch intent CRDs directly). Recommendation: start with A, migrate to B. |

### 4.3 VRF References

| Input | Proposal |
|---|---|
| Inline VRF name lists on each attachment (`VRFs: [vrf_a, vrf_b]`) | `Destination` CRD + label selectors. Prefix imports inherited, exports derived automatically. |

### 4.4 Network Parameter Source

| Input | Proposal |
|---|---|
| Mix of user-specified and auto-allocated (BM4X, OpenStack) | All user-specified; allocation fields reserved for future use |

---

## 5. Items Deferred or Dropped

| Item | Status | Notes |
|---|---|---|
| Management-cluster controller | **Deferred** | Can be added when auto-allocation is needed |
| Automatic network allocation (BM4X / OpenStack / vSphere) | **Deferred** | API fields reserved; transparent enhancement |
| DNS record creation | **Dropped** | Not scoped in the proposal |
| `spec.network.networkName` (OpenStack/vSphere) | **Reserved** | Field exists in Go types but not processed |
| `spec.network.harmonization` (BM4X allocation class) | **Reserved** | Field exists in Go types but not processed |
| `spec.network.ipv4.size` (dynamic sizing) | **Reserved** | Field exists in Go types but not processed |
| Bidirectional connections | **Deferred** | Noted as "not first iteration" — same as input |
| `Consequences` / `Considerations` sections | **Reworked** | Replaced by §8 Open Questions, §9 Considerations, §10 Decision Record |

---

## 6. Items Kept Unchanged

The following concepts passed through from the input to the proposal with no structural change:

- Overall intent: tenant self-service, intent-based over low-level config.
- `Layer2Attachment`, `VRFAttachment`, `BGPNeighbor`, `PodNetwork` as the four original CRD names and their core semantics.
- Connection model on `VRFAttachment`: inbound/outbound, `disableLoadBalancer`, `count`.
- SR-IOV configuration model and validation rules.
- BGP configuration block on `Layer2Attachment`.
- `BGPNeighbor` spec and status structure.
- `workerGroups` for node scoping.
- `nodeIPs` configuration.
- Anycast gateway + MAC allocation.
- Validation rules (mostly identical, just reformatted).
- Platform integration targets: MetalLB (inbound), Coil Egress (outbound), Calico (pod networks).

---

## 7. Summary of Key Decisions

| # | Decision | Input Position | Proposal Position |
|---|---|---|---|
| 1 | Management-cluster controller | Included | Removed (tenant-only) |
| 2 | Network allocation | Automatic via BM4X/OpenStack/vSphere | User-specified; fields reserved |
| 3 | VRF references | Inline name lists | `Destination` CRD + label selectors |
| 4 | Import prefixes | Per-attachment route blocks | On `Destination`, inherited automatically |
| 5 | Pipeline integration | Implicit Option A | Open question (A vs. B) |
| 6 | Traffic mirroring | Not covered | `Collector` + `TrafficMirror` CRDs |
| 7 | DNS | Mentioned | Dropped |
| 8 | Bidirectional connections | "Not first iteration" | Same |
