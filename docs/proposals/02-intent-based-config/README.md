# Proposal 02 — Cluster-Local Intent-Driven Network Configuration

- **Status:** Draft
- **Date:** 2026-02-09
- **Authors:** das-schiff network-operator team

## 1. Summary

This proposal describes how to introduce **intent-driven custom resources** for cluster networking in the das-schiff network operator. Instead of requiring operators to understand and manually compose low-level HBR configuration (`Layer2NetworkConfiguration`, `VRFRouteConfiguration`, `BGPPeering`, `MirrorSelector`, `MirrorTarget`), tenants will express their desired network state through high-level "attachment" resources. A set of new controllers translates these intents into the operator's configuration pipeline and manages ancillary platform components (load balancers, egress NAT, CNI pools, traffic mirroring).

In this first iteration, all network parameters (VLAN IDs, VNIs, subnets, addresses) are **specified directly** by the user in the intent CRDs. Automatic network allocation via a management cluster controller is explicitly out of scope and can be added later as a transparent enhancement.

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

> **Explicitly out of scope for this iteration:** automatic network allocation (BM4X / OpenStack / vSphere), DNS integration, and management-cluster cross-cluster reconciliation. Users specify all network parameters (VLAN IDs, VNIs, subnets) directly.

## 3. Design Decisions

### 3.1 New Intent-Driven CRDs

We introduce seven new cluster-scoped CRDs under `network.t-caas.telekom.com/v1alpha1`:

| CRD | Purpose |
|---|---|
| `Destination` | Reachability target (VRF) — defined once, referenced by label from attachments |
| `Layer2Attachment` | Attach L2/VLAN networks to worker groups |
| `VRFAttachment` | Connect a cluster to VRFs with inbound/outbound connections |
| `BGPNeighbor` | Allow workload BGP route advertisement |
| `PodNetwork` | Allocate additional pod-level networks (CNI integration) |
| `Collector` | GRE collector endpoint + mirror VRF binding — defined once, referenced by TrafficMirrors |
| `TrafficMirror` | Declaratively mirror traffic from an attachment to a Collector |

These CRDs describe **what** the user wants, not **how** to configure it. Crucially, `Destination` decouples reachability targets from the resources that need them — see §3.4.

### 3.2 Controller Architecture — Tenant Cluster Only

All intent controllers run in the **tenant cluster**, integrated into the existing network-operator. There is no management cluster component in this iteration.

```
┌─────────────────────────────────────────────────────────────────────┐
│                     Tenant Cluster                                  │
│                                                                     │
│  Layer2Attachment ──┐                                               │
│  VRFAttachment ─────┤  network-intent-controller(s)                │
│  BGPNeighbor ───────┤  ┌──────────────────────────────────┐        │
│  PodNetwork ────────┘  │ • Translate intents → config     │        │
│                        │ • Configure platform components  │        │
│                        │   (MetalLB, Coil, Calico, etc.)  │        │
│                        │ • All network params specified   │        │
│                        │   directly by the user           │        │
│                        └────────┬─────────────────────────┘        │
│                                 │                                   │
│                                 ▼                                   │
│                ┌────────────────────────────────────────┐           │
│                │ Integration with revision pipeline     │           │
│                │ (see §3.3 — open design question)      │           │
│                └────────┬───────────────────────────────┘           │
│                         │                                           │
│                         ▼                                           │
│                   NetworkConfigRevision → NodeNetworkConfig → CRA   │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

All network parameters (VLAN IDs, VNIs, subnets, IP addresses) are specified directly in the intent CRDs by the user. Automatic allocation through a management cluster controller can be added later as a transparent layer on top — the intent CRD API is designed to accommodate this (fields like `harmonization` and `networkName` are reserved but not processed in this iteration).

### 3.3 Integration with the Revision Pipeline — Open Design Question

A key architectural decision is **how the intent CRDs feed into the existing revision pipeline**. There are two viable approaches:

#### Option A — Intent → Existing Low-Level CRDs → Revision

The intent controllers produce standard `Layer2NetworkConfiguration`, `VRFRouteConfiguration`, and `BGPPeering` resources. The existing `ConfigReconciler` picks these up and creates revisions as usual.

```
 Layer2Attachment ──▶ generates ──▶ Layer2NetworkConfiguration ─┐
 VRFAttachment ────▶ generates ──▶ VRFRouteConfiguration ───────┼──▶ ConfigReconciler ──▶ Revision
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
 VRFAttachment ─────────────────────┤
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

#### Recommendation

This decision is **still open**. Both options preserve the revision-based, gated rollout pipeline unchanged at the `NodeNetworkConfig` level.

**Option A** is simpler to implement first (no changes to the existing `ConfigReconciler`), but introduces a second layer of generated resources that may cause confusion.

**Option B** is architecturally cleaner long-term (single source of truth, no generated artifacts), but requires the `ConfigReconciler` to gain awareness of intent CRDs.

A possible migration path: start with **Option A** to validate the intent CRD API quickly, then move to **Option B** once the API is stable and the low-level CRDs can be phased out as user-facing resources.

### 3.4 Destinations as a First-Class Concept — Decoupling Reachability from Attachments

#### The Problem with Inline VRF Lists

In the current low-level model, `VRFRouteConfiguration` is a powerful, composable resource. Multiple `VRFRouteConfiguration` objects can target the **same VRF**, each contributing import/export entries, SBR prefixes, aggregates, and communities. They are merged at revision build time (sorted by `seq`), and the `buildNodeVrf` function creates a single `FabricVRF` from all of them. This enables patterns like:

- Multiple teams independently adding routes to the same VRF.
- SBR prefixes creating intermediate local VRFs (`s-<vrf>`) with policy routes.
- Different `VRFRouteConfiguration` resources targeting the same VRF with different `nodeSelector` to scope routes per worker group.

The initial intent CRD design embedded VRF lists **inline** in each attachment (`Layer2Attachment.spec.network.VRFs`, `VRFAttachment.spec.network.VRFs`). This has several issues:

1. **Duplication:** If two `Layer2Attachment`s and a `VRFAttachment` all need connectivity to `m2m_enc`, the VRF name and its routing config appear in three places.
2. **No composability:** The current model lets multiple `VRFRouteConfiguration` resources build up a VRF's import/export list incrementally. With VRFs inlined in attachments, each attachment must carry its own complete routing view.
3. **SBR complexity hidden:** The SBR pattern (where traffic from specific source prefixes is steered into an intermediate VRF that imports from both the target VRF and the cluster VRF) is naturally expressed today as a `VRFRouteConfiguration` with `sbrPrefixes`. If VRFs are inlined in attachments, this pattern has nowhere clean to live.
4. **No shared connectivity:** A VRF like `m2m_enc` represents a backbone destination. Multiple attachments, connections, and pod networks may all need routes to/from it. It's a **shared concept**, not something each attachment should independently define.

#### Solution: `Destination` as a Labeled, Referenceable Resource

We introduce a `Destination` CRD that represents a reachability target — typically a VRF in the backbone. Destinations carry **labels** and are **referenced by label selector** from attachments and connections.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Destination
metadata:
  name: m2m-enc
  labels:
    network.t-caas.telekom.com/vrf: m2m_enc
    network.t-caas.telekom.com/zone: secure
spec:
  vrf: m2m_enc
```

Attachments and connections then reference destinations via label selectors:

```yaml
# Layer2Attachment references destinations by label
spec:
  destinations:
    matchLabels:
      network.t-caas.telekom.com/zone: secure
```

This means:
- **Destinations are defined once, referenced many times.** No duplication.
- **Import prefixes live on the Destination** — the subnets reachable through that VRF are defined once and inherited by every attachment that selects it. **Export prefixes are derived automatically** from each attachment's own subnet.
- **Additional routes** can still be specified on the attachment side for edge cases (extra prefixes beyond what the Destination defines).
- **SBR and intermediate VRFs** can be expressed as properties of the destination or derived by the controller when an attachment specifies SBR-relevant source prefixes toward a destination.
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

`Destination` replaces the VRF-reference portion of the intent CRDs. It does **not** replace `VRFRouteConfiguration` — the low-level CRD remains available as an escape hatch and is still what gets produced in the revision pipeline. The intent controller translates `Destination` references + attachment routing specs into the equivalent `VRFRouteConfiguration` entries (or directly into revision data, depending on §3.3).

Multiple attachments selecting the same `Destination` produce **merged** VRF config in the revision, just like multiple `VRFRouteConfiguration` resources do today.

#### Diagram

```
  Destination "m2m-enc"                 Destination "internet"
  labels:                               labels:
    zone: secure                          zone: public
  spec:                                 spec:
    vrf: m2m_enc                        vrf: internet
    prefixes:                             prefixes:
    - 192.0.2.0/24                          - 0.0.0.0/0
    - 203.0.113.0/24
        ▲          ▲                          ▲
        │          │                          │
        │          │                          │
  ┌─────┘    ┌─────┘                    ┌─────┘
  │          │                          │
  │   Layer2Attachment "vlan100"   VRFAttachment "prod"
  │     destinations:               destinations:
  │       zone: secure                zone: public
  │     (inherits prefixes            connections:
  │      from Destination)            - name: ingress
  │                                     direction: inbound
  │                                     (inherits prefixes
  │                                      from Destination)
  │
  PodNetwork "extra-pods"
    destinations:
      zone: secure
    (inherits prefixes
     from Destination)
```

All three resources select the `m2m-enc` destination and inherit its prefixes (`192.0.2.0/24`, `203.0.113.0/24`). The controller merges each attachment's export requirements (its own subnets) into a single VRF configuration for `m2m_enc`, preserving the composability of today's model. Attachments can optionally specify additional routes beyond what the Destination defines.

### 3.5 Traffic Mirroring — Intent-Based Wrapper Around MirrorSelector / MirrorTarget

[Proposal 01](../01-traffic-mirroring/README.md) introduces low-level `MirrorSelector` and `MirrorTarget` CRDs for traffic mirroring. These are powerful but require users to manually coordinate several moving parts:

1. Create a dedicated mirror VRF via `VRFRouteConfiguration` (with VNI, RT, loopbacks, IPAM pool ref).
2. Create a `MirrorTarget` pointing at the collector IP, GRE key/type, and binding it to the mirror VRF + loopback.
3. Create a `MirrorSelector` per traffic flow, referencing the `MirrorTarget`, a `MirrorSource` (low-level L2/VRF CRD), direction, and traffic match.

This is exactly the kind of multi-resource coordination that intent CRDs are designed to simplify.

#### Splitting the Concerns: `Collector` + `TrafficMirror`

The same GRE collector (IP address, protocol, tunnel key) and mirror VRF are typically shared across many mirroring rules. Embedding collector config inline in every `TrafficMirror` would cause the same duplication that `Destination` solves for production VRFs. We therefore split mirroring into two CRDs:

- **`Collector`** — defines *where* mirrored traffic goes: GRE endpoint properties + which `Destination` (mirror VRF) hosts the tunnel. Defined once, referenced by name from `TrafficMirror` resources.
- **`TrafficMirror`** — defines *what* to mirror: source attachment, direction, traffic match, and a reference to a `Collector`. Lightweight and per-flow.

```
Collector "prod-collector"                TrafficMirror "capture-vlan100"
  spec:                                     spec:
    address: 192.0.2.100                     source:
    protocol: l3gre                             kind: Layer2Attachment
    key: 1001                                   name: vlan100
    mirrorDestination:                        collector: prod-collector
      name: mirror-vrf                        direction: ingress
                                              trafficMatch:
        ▲                                       srcPrefix: 203.0.113.0/24
        │  shared by                            protocol: tcp
        │                                       dstPort: 443
TrafficMirror "capture-vrf-egress"
  spec:
    source:
      kind: VRFAttachment
      name: prod-access
    collector: prod-collector          ← same collector, different flow
    direction: egress
```

This mirrors (pun intended) the `Destination` pattern: shared infrastructure defined once, referenced many times.

#### Relationship to Low-Level Mirror CRDs

`Collector` is the intent-level equivalent of `MirrorTarget`, and `TrafficMirror` is the intent-level equivalent of `MirrorSelector`. Depending on the pipeline integration approach (§3.3):

- **Option A:** The controllers generate `MirrorTarget` (from `Collector`) and `MirrorSelector` (from `TrafficMirror`) resources, plus the mirror VRF's `VRFRouteConfiguration` with loopbacks.
- **Option B:** The `ConfigReconciler` resolves `Collector` + `TrafficMirror` directly at `NodeNetworkConfig` build time, populating GRE interfaces, loopbacks, EVPN export filters, and `MirrorACLs` on target nodes.

In both cases, the low-level `MirrorSelector` / `MirrorTarget` CRDs remain available as an escape hatch for advanced use cases (e.g., custom GRE naming, multiple loopbacks per VRF, complex multi-target scenarios).

#### Mirror VRF as a `Destination` CRD

The mirror VRF (where the GRE tunnel lives) is modeled as a `Destination` with a dedicated label (e.g., `network.t-caas.telekom.com/role: mirror`). This is consistent with the rest of the intent design — a VRF is a VRF, whether it carries production traffic or mirror traffic. The `Destination` for the mirror VRF carries:

- `vrf`, `vni`, `routeTarget` — standard VRF properties.
- `loopbacks` — with `poolRef` for per-node CAPI IPAM allocation (as designed in Proposal 01).

Multiple `Collector` resources can reference the same mirror `Destination` (e.g., multiple collectors sharing one mirror VRF), and multiple `TrafficMirror` resources can reference the same `Collector`.

## 4. Resource Specifications

### 4.1 Destination

`Destination` represents a reachability target — a VRF in the backbone or a routing domain. It is defined once and referenced by label selector from attachments and connections.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Destination
metadata:
  name: m2m-enc
  labels:
    network.t-caas.telekom.com/vrf: m2m_enc
    network.t-caas.telekom.com/zone: secure
spec:
  # VRF name in the backbone
  vrf: m2m_enc
  # Subnets reachable via this destination.
  # Defined once here, inherited by every attachment that selects this destination.
  # Attachments do NOT need to repeat these prefixes.
  prefixes:
  - 198.51.100.0/27
  - 192.0.2.0/24
  # Optional: VNI and route target (if not derivable from operator config)
  vni: 10100
  routeTarget: "64500:10100"
  # Optional: community to set on exported routes toward this destination
  community: "64500:999"
  # Optional: Loopback interfaces with per-node IP allocation via CAPI IPAM.
  # Primarily used for mirror VRFs (GRE source IPs) but generic enough for
  # any VRF that needs per-node loopback addresses (e.g., BGP peering).
  # loopbacks:
  # - name: lo.mir
  #   poolRef:
  #     apiGroup: ipam.cluster.x-k8s.io
  #     kind: InClusterIPPool
  #     name: mirror-source-ips
  # Optional: SBR configuration for this destination
  # When an attachment routes traffic toward this destination via SBR,
  # the controller creates intermediate local VRFs automatically.
  sbr:
    enabled: false
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
- `spec.vrf` is required and immutable.
- VRF name must be ≤ 12 characters (HBR constraint).
- `spec.prefixes` entries must be valid CIDR notation.

### 4.2 Layer2Attachment

`Layer2Attachment` attaches Layer 2 VLAN-based networks to one or more worker groups within a cluster.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Layer2Attachment
metadata:
  name: example
spec:
  # --- Network Source (immutable) ---
  network:
    # VLAN ID to use
    vlanID: 234
    # VNI for VXLAN encapsulation
    vni: 10234
    # IPv6 subnet
    subnet: 2001:db8:100::0/64
    ipv4:
      subnet: 198.51.100.128/25
    disablev6: false       # IPv6 enabled by default
    # --- Reserved for future automatic allocation (not processed in this iteration) ---
    # networkName: schiff-1         # OpenStack/vSphere network reference
    # harmonization: "public/internet"  # BM4X allocation class

  # --- Destinations (replaces inline VRF lists) ---
  # Select which Destination resources (VRFs) should be reachable via this attachment.
  # Uses standard Kubernetes label selectors.
  destinations:
    matchLabels:
      network.t-caas.telekom.com/zone: secure

  # --- Interface Configuration ---
  interfaceName: xyz       # suffix → full name becomes 'l2.<interfaceName>' (immutable)
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

  # --- Scope ---
  workerGroups:
  - wg1

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
- `interfaceName` is required when SR-IOV is disabled.
- `nodeIPs.enabled` must be `false` when SR-IOV is enabled and `interfaceName` is not set.
- `disableNeighborSuppression` must be `true` when `disableAnycast` is set.
- BM4X: if both SR-IOV and `interfaceName` are set/enabled, only one VRF is allowed.

**Controller Behavior:**

| Scenario | Actions |
|---|---|
| **SR-IOV only** (no interfaceName) | Create policy allowing `NetworkAttachmentDefinition` with VLAN ID |
| **Non-SRIOV** | Create host interface `l2.<interfaceName>` with VLAN, MTU, VNI, neighborSuppression; allocate anycast gateway (first address); set VRF on interface; configure HBR routes; if `nodeIP.enabled`: configure routes in default routing table |
| **SR-IOV + interfaceName** (for macvlan/egress) | Create SR-IOV policies; create host interface `l2.<interfaceName>` attached to SR-IOV bridge; if `nodeIP.enabled`: configure routes |

### 4.2 VRFAttachment

`VRFAttachment` connects a cluster to VRFs and specifies inbound/outbound traffic handling.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: VRFAttachment
metadata:
  name: example
spec:
  # --- Network Configuration (immutable) ---
  network:
    subnet: 2001:db8:100::0/64
    ipv4:
      subnet: 198.51.100.128/25
    disablev6: false

  # --- Destinations (replaces inline VRF lists) ---
  destinations:
    matchLabels:
      network.t-caas.telekom.com/zone: secure

  # --- Connections ---
  connections:
  - name: "x"
    direction: outbound        # inbound | outbound | bidirectional
    disableLoadBalancer: false
    count: 1                   # number of IPs/instances
    # Additional routes beyond what the Destination defines (optional)
    routes: []
  - name: "y"
    direction: inbound
    routes: []
```

**Status:**
```yaml
status:
  connections:
  - name: "x"
    addresses:
    - "2001:db8::1"
    addressesv4:
    - "198.51.100.130"
  conditions:
  - type: Ready
    status: "True"
```

**Validation Rules:**
- At least one `Destination` must match the `destinations` selector.
- Specified network subnets must not overlap with route prefixes.
- `sum(connections[*].count)` must not exceed usable IP addresses.
- No two routes may contain identical prefixes.

**Controller Behavior per Connection Direction:**

| Direction | Platform Configuration |
|---|---|
| **Inbound** | Configure MetalLB load balancer (no ingress); create `LoadBalancerClass` (where applicable); configure policy-based routing on HBN for multi-VRF |
| **Outbound** | Configure Coil Egress NAT; configure Calico IP pools and policies; configure policy-based routing on HBN for multi-VRF |
| **Bidirectional** | Not implemented in first iteration |

### 4.3 BGPNeighbor

`BGPNeighbor` describes a BGP neighbor that applications can use to advertise routes to the backbone. A neighbor can span multiple `VRFAttachment` connections.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: BGPNeighbor
metadata:
  name: example
spec:
  allowedConnections:
  - vrfAttachment:
      name: test
    connectionNames:
    - x
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
- All referenced connections must have `disableLoadBalancer: true`.
- All referenced connections must be `direction: inbound`.

### 4.4 PodNetwork

`PodNetwork` allocates additional networks available to pods, configured through the CNI (Calico).

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: PodNetwork
metadata:
  name: example
spec:
  network:
    subnet: 2001:db8:100::0/64
    ipv4:
      subnet: 198.51.100.128/25
    disablev6: false
  # --- Destinations ---
  destinations:
    matchLabels:
      network.t-caas.telekom.com/zone: secure
  # Additional routes beyond what the Destination defines (optional)
  # routes:
  # - prefixes:
  #   - 198.51.100.64/26
```

**Controller Behavior:**
- Configure Calico IP pools for the specified subnets.
- Create appropriate network policies.
- Set up routes toward specified VRFs.

### 4.5 Collector

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
  # References a Destination CRD representing the mirror VRF.
  # The mirror VRF must have loopbacks with a poolRef for per-node
  # GRE source IP allocation via CAPI IPAM.
  mirrorDestination:
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
- `spec.mirrorDestination.name` must reference an existing `Destination` with `loopbacks` configured.

**Controller Behavior:**

| Step | Action |
|---|---|
| 1. Resolve mirror destination | Look up the `Destination` → get mirror VRF name, VNI, RT, loopback config |
| 2. Ensure per-node IPAM | For each node in scope: create/find `IPAddressClaim` for the mirror VRF loopback (CAPI IPAM) |
| 3. Generate GRE tunnel | Create GRE interface entry in the mirror VRF on `NodeNetworkConfig` (src = allocated loopback IP, dst = collector address) |
| 4. EVPN export filter | Auto-append `permit <loopback-ip>/32` to the mirror VRF's EVPN export filter |
| 5. Track references | Count how many `TrafficMirror` resources reference this collector |

### 4.6 TrafficMirror

`TrafficMirror` declaratively mirrors selected traffic from an attachment to a `Collector`. It is the intent-level equivalent of `MirrorSelector` — lightweight and per-flow.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: TrafficMirror
metadata:
  name: capture-vlan100
spec:
  # --- Source: which attachment's traffic to mirror ---
  source:
    # References a Layer2Attachment or VRFAttachment by name.
    kind: Layer2Attachment       # Layer2Attachment | VRFAttachment
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
- `source.kind` must be `Layer2Attachment` or `VRFAttachment`.
- `source.name` must reference an existing attachment.
- `collector` must reference an existing `Collector`.
- `direction` is required.

**Controller Behavior:**

| Step | Action |
|---|---|
| 1. Resolve source | Look up the referenced `Layer2Attachment` or `VRFAttachment` → determine the L2 VLAN or VRF interface to mirror from, and the node scope (`workerGroups`) |
| 2. Resolve collector | Look up the `Collector` → get its GRE interface name and mirror VRF |
| 3. Generate MirrorACL | Attach `MirrorACL` to the source L2 or VRF on `NodeNetworkConfig` (mirrorDestination = collector's GRE interface name) |

## 5. Implementation Plan

> **Note:** The pipeline integration approach (Option A vs. Option B from §3.3) affects phases 2–5. The steps below describe the controller behavior for each intent CRD. Whether the controller generates intermediate low-level CRDs (Option A) or feeds directly into the `ConfigReconciler` (Option B) is an open decision.

### Phase 1 — Core CRD Types and Scaffolding

1. **Define Go types** for `Destination`, `Layer2Attachment`, `VRFAttachment`, `BGPNeighbor`, `PodNetwork`, `Collector`, `TrafficMirror` in `api/v1alpha1/`.
2. **Generate CRDs** via `controller-gen` and **deep-copy** methods.
3. **Add webhook validation** for each new CRD with the rules described in §4.

### Phase 2 — Destination Controller

1. **Watch `Destination`** resources.
2. **Validate** VRF name, resolve VNI/RT (from spec or operator config).
3. **Track references** — count how many attachments/connections select this destination.
4. **Update `Destination.Status`** with reference count and conditions.

### Phase 3 — Layer2Attachment Controller

This is the highest-value, most complex intent resource. Implementation steps:

1. **Watch `Layer2Attachment`** in a new controller.
2. **Resolve `destinations` selector** — list all `Destination` resources matching the label selector.
3. **Read user-specified network parameters** (`vlanID`, `vni`, `subnet`, `ipv4.subnet`) directly from the CR spec.
4. **Translate into the revision pipeline** (approach depends on §3.3):
   - Map `mtu`, `vni`, `vlan` (from spec).
   - Set `anycastGateways`, `anycastMac` (unless `disableAnycast`).
   - Set `neighSuppression` (unless `disableNeighborSuppression`).
   - For each matched destination: determine VRF, generate import entries from the destination's `prefixes` and export entries from the attachment's subnet.
   - If multiple destinations match: set cluster VRF on interface, configure policy-based routing (SBR + intermediate VRFs).
   - If single destination: set destination's VRF directly on the interface.
   - Set `nodeSelector` from `workerGroups`.
5. **Configure node IPs** if `nodeIPs.enabled`.
6. **SR-IOV handling:**
   - When `sriov.enabled`: create SR-IOV policies for `NetworkAttachmentDefinition`.
   - When `sriov.enabled` + `interfaceName` set: additionally create the host interface attached to SR-IOV bridge.
7. **Update `Layer2Attachment.Status`** with anycast info, SR-IOV VLAN ID, matched destinations, conditions.

### Phase 4 — VRFAttachment Controller

1. **Watch `VRFAttachment`**.
2. **Resolve `destinations` selector** — list all matching `Destination` resources.
3. **Read user-specified network parameters** directly from the CR spec.
4. **Translate into the revision pipeline** (approach depends on §3.3) for each matched destination's VRF.
5. **Process connections:**
   - **Inbound:** configure MetalLB `IPAddressPool` + `L2Advertisement` or `BGPAdvertisement`; create `LoadBalancerClass`; use addresses from the user-specified subnet.
   - **Outbound:** configure Coil `Egress` resources; configure Calico `IPPool` and `NetworkPolicy`; use addresses from the user-specified subnet.
6. **Policy-based routing** for multi-destination scenarios → generate appropriate SBR prefixes and intermediate VRFs.
7. **Update `VRFAttachment.Status`** with assigned addresses per connection, matched destinations.

### Phase 5 — BGPNeighbor Controller

1. **Watch `BGPNeighbor`**.
2. **Resolve referenced `VRFAttachment` connections** → validate constraints.
3. **Translate into the revision pipeline** (approach depends on §3.3) with appropriate import/export filters derived from the referenced connections.
4. **Update `BGPNeighbor.Status`** with assigned AS numbers, neighbor IPs, timers.

### Phase 6 — PodNetwork Controller

1. **Watch `PodNetwork`**.
2. **Resolve `destinations` selector** — list all matching `Destination` resources.
3. **Configure Calico** `IPPool` for the user-specified subnet.
4. **Set up routes** toward each matched destination's VRF (approach depends on §3.3).

### Phase 7 — Collector Controller

1. **Watch `Collector`**.
2. **Resolve `mirrorDestination`** — look up the `Destination` for the mirror VRF, validate it has loopbacks with a `poolRef`.
3. **CAPI IPAM integration** — for each node in scope (determined by the `TrafficMirror` resources that reference this collector):
   - Create or find an `IPAddressClaim` named `<mirror-vrf>-<loopback>-<node>` using the loopback's `poolRef`.
   - Wait for the IPAM provider to fulfil the claim → read `IPAddress.Spec.Address`.
4. **Inject into `NodeNetworkConfig`** (or generate low-level CRDs, depending on §3.3):
   - Mirror VRF: add loopback with allocated IP, GRE interface (src = loopback, dst = collector address), EVPN export filter entry.
5. **Update `Collector.Status`** with GRE interface name, reference count, active node count, conditions.

### Phase 8 — TrafficMirror Controller

1. **Watch `TrafficMirror`**.
2. **Resolve `source`** — look up the referenced `Layer2Attachment` or `VRFAttachment` to determine the source interface (L2 VLAN or VRF) and `workerGroups` (node scope).
3. **Resolve `collector`** — look up the `Collector`, get its GRE interface name.
4. **Generate `MirrorACL`** entries on the source L2 or VRF in `NodeNetworkConfig` (or generate `MirrorSelector` in Option A), with mirrorDestination = collector's GRE interface name.
5. **Update `TrafficMirror.Status`** with active node count, conditions.

### Phase 9 — Migration Path

1. **Coexistence:** Intent-based and low-level CRDs coexist. Both feed into the revision pipeline.
2. **Adoption tool:** Provide a utility to generate `Layer2Attachment` / `VRFAttachment` from existing low-level CRDs, facilitating migration.
3. **Deprecation:** Once intent-based CRDs are stable, low-level CRDs may be deprecated for direct user creation (they remain as an advanced/escape-hatch mechanism).

## 6. Translation Examples

### 6.1 Destinations + Layer2Attachment → Revision Pipeline

The controller resolves the destination selector, then translates the attachment + matched destinations into L2 and VRF config entries for the revision.

```
Destination "m2m-enc"                     (defined once)
  labels: { zone: secure }
  spec:
    vrf: m2m_enc
    prefixes:                             ← subnets reachable via this VRF
    - 198.51.100.0/27
    - 192.0.2.0/24
                ▲
                │  label selector match
                │
Layer2Attachment "my-vlan"                Equivalent L2 + VRF config in revision:
  spec:                                     L2:
    network:                                  id: 234
      vlanID: 234                   ──▶       mtu: 1500
      vni: 10234                              vni: 10234
    mtu: 1500                                 anycastMac: aa:bb:cc:dd:ee:ff
    interfaceName: mynet                      anycastGateways: [2001:db8:100::1/64]
    workerGroups: [wg1]                       neighSuppression: true
    destinations:                             vrf: m2m_enc    ← from Destination
      matchLabels:                            nodeSelector:
        zone: secure                            worker-group: wg1
    # no routes needed — inherited       VRF:
    # from Destination                      vrf: m2m_enc
                                              import:           ← from Destination.prefixes
                                              - cidr: 198.51.100.0/27
                                                action: permit
                                              - cidr: 192.0.2.0/24
                                                action: permit
                                              export:           ← attachment's own subnet
                                              - cidr: 2001:db8:100::0/64
                                                action: permit
```

**Key point:** If `PodNetwork "extra-pods"` also selects `zone: secure`, its routes are **merged** into the same `m2m_enc` VRF config — just like multiple `VRFRouteConfiguration` resources merge today.

### 6.2 Shared Destination — Multiple Attachments, Merged VRF Config

```
Destination "m2m-enc"  ← selected by both:
  vrf: m2m_enc
  prefixes:            ← defined once
  - 198.51.100.0/27
  - 192.0.2.0/24
        ▲         ▲
        │         │
  Layer2Attachment    PodNetwork
  "vlan100"           "extra-pods"
  network:            network:
    subnet: 2001:db8:1:…      subnet: 2001:db8:2:…
  (no routes needed —  (no routes needed —
   inherits from Dest)  inherits from Dest)

        │                  │
        └────────┬─────────┘
                 ▼
  Merged VRF config for m2m_enc:
    import:                                   ← from Destination.prefixes (shared)
    - cidr: 198.51.100.0/27, action: permit
    - cidr: 192.0.2.0/24,     action: permit
    export:                                   ← each attachment exports its own subnet
    - cidr: 2001:db8:1:…/64,      action: permit    (from vlan100)
    - cidr: 2001:db8:2:…/64,      action: permit    (from extra-pods)
```

Import prefixes come from the `Destination` (defined once), while export prefixes come from each attachment's own subnet. This preserves the **composability** of today's `VRFRouteConfiguration` model while eliminating prefix duplication.

### 6.3 VRFAttachment → Revision + Platform Configuration

```
Destination "internet"                    (defined once)
  labels: { zone: public }
  spec: { vrf: internet }
                ▲
                │
VRFAttachment "prod-access"
  spec:
    network:
      subnet: 2001:db8:200::0/64
      ipv4: { subnet: 203.0.113.0/24 }
    destinations:
      matchLabels: { zone: public }
    connections:
    - name: "ingress"
      direction: inbound
      count: 2
    - name: "egress"
      direction: outbound
      count: 1

        │
        ▼

  ┌─────────────────────────────────────────────────────────┐
  │ Produced config:                                         │
  │                                                          │
  │ VRF entry for "internet" in revision                  │
  │   import/export: <derived from connections>              │
  │                                                          │
  │ MetalLB IPAddressPool "prod-access-ingress"             │
  │   addresses: [203.0.113.1, 203.0.113.2]                    │
  │                                                          │
  │ Coil Egress "prod-access-egress"                        │
  │   address: 203.0.113.3                                    │
  │                                                          │
  │ Calico IPPool "prod-access-egress-pool"                 │
  │   cidr: 203.0.113.0/24                                    │
  └─────────────────────────────────────────────────────────┘
```

## 7. Shared Network Spec

The `network` field appears identically in `Layer2Attachment`, `VRFAttachment`, and `PodNetwork`. It should be defined as a shared Go type:

```go
// NetworkSpec describes the network configuration.
// All network parameters are specified directly by the user.
// Fields for future automatic allocation (NetworkName, Harmonization)
// are reserved but not processed in this iteration.
//
// Note: VRFs/destinations are NOT part of NetworkSpec. They are specified
// via a `destinations` label selector on the parent resource, referencing
// Destination CRDs.
type NetworkSpec struct {
    // VlanID is the VLAN ID for the network.
    // +optional
    VlanID *int `json:"vlanID,omitempty"`
    // VNI is the VXLAN Network Identifier.
    // +optional
    VNI *int `json:"vni,omitempty"`
    // Subnet is the IPv6 subnet for the network.
    // +optional
    Subnet *string `json:"subnet,omitempty"`
    // IPv4 configures IPv4 for the network.
    // +optional
    IPv4 *IPv4Config `json:"ipv4,omitempty"`
    // DisableV6 disables IPv6 for the network. IPv6 is enabled by default.
    // +optional
    DisableV6 bool `json:"disablev6,omitempty"`

    // --- Reserved for future automatic allocation (not processed in this iteration) ---

    // NetworkName is the name of an existing OpenStack/vSphere network.
    // +optional
    NetworkName *string `json:"networkName,omitempty"`
    // Harmonization determines the allocation class for BM4X (e.g. 'public/internet').
    // +optional
    Harmonization *string `json:"harmonization,omitempty"`
}

// IPv4Config configures IPv4 for a network.
type IPv4Config struct {
    // Subnet is the IPv4 subnet for the network.
    // +optional
    Subnet *string `json:"subnet,omitempty"`

    // --- Reserved for future automatic allocation ---

    // Size is the minimum number of usable addresses (for automatic allocation).
    // +optional
    Size *int `json:"size,omitempty"`
}

// DestinationSpec defines properties of a reachability target (VRF).
type DestinationSpec struct {
    // VRF is the name of the backbone VRF this destination represents.
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MaxLength=12
    VRF string `json:"vrf"`
    // Prefixes lists the subnets reachable via this destination.
    // These become import entries in the VRF configuration for every
    // attachment that selects this destination — defined once, inherited everywhere.
    // +optional
    Prefixes []string `json:"prefixes,omitempty"`
    // VNI is the VXLAN Network Identifier for the VRF.
    // +optional
    VNI *int `json:"vni,omitempty"`
    // RouteTarget is the BGP route target for the VRF.
    // +optional
    RouteTarget *string `json:"routeTarget,omitempty"`
    // Community is the BGP community to set on exported routes.
    // +optional
    Community *string `json:"community,omitempty"`
    // Loopbacks defines loopback interfaces for this VRF with optional
    // per-node IP allocation via Cluster API IPAM. Used for mirror VRFs
    // (GRE source IPs) and any VRF needing per-node loopback addresses.
    // +optional
    Loopbacks []DestinationLoopback `json:"loopbacks,omitempty"`
    // SBR configures source-based routing for this destination.
    // +optional
    SBR *SBRConfig `json:"sbr,omitempty"`
}

// DestinationLoopback defines a loopback interface within a destination's VRF.
type DestinationLoopback struct {
    // Name is the loopback interface name (e.g. "lo.mir").
    Name string `json:"name"`
    // PoolRef references a Cluster API IPAM pool (e.g. InClusterIPPool, InfobloxIPPool).
    // The operator creates an IPAddressClaim per node and uses the allocated IP.
    PoolRef corev1.TypedObjectReference `json:"poolRef"`
}

// SBRConfig configures source-based routing for a destination.
type SBRConfig struct {
    // Enabled indicates whether SBR should be used for this destination.
    Enabled bool `json:"enabled"`
}

// CollectorSpec defines the desired state of Collector.
// Collector is the intent-level equivalent of MirrorTarget —
// it binds a GRE endpoint to a mirror VRF Destination.
type CollectorSpec struct {
    // Address is the remote collector's IP address.
    Address string `json:"address"`
    // Protocol is the GRE encapsulation type.
    // +kubebuilder:validation:Enum=l3gre;l2gre
    Protocol string `json:"protocol"`
    // Key is an optional GRE tunnel key.
    // +optional
    Key *uint32 `json:"key,omitempty"`
    // MirrorDestination references the Destination CRD representing the mirror VRF.
    MirrorDestination MirrorDestinationRef `json:"mirrorDestination"`
}

// MirrorDestinationRef references a Destination CRD for the mirror VRF.
type MirrorDestinationRef struct {
    // Name of the Destination resource.
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
    // Kind is the type of attachment: Layer2Attachment or VRFAttachment.
    // +kubebuilder:validation:Enum=Layer2Attachment;VRFAttachment
    Kind string `json:"kind"`
    // Name is the name of the attachment resource.
    Name string `json:"name"`
}
```

## 8. Open Questions

1. **Pipeline integration approach (§3.3):** Should intent CRDs generate intermediate low-level CRDs (Option A) or feed directly into the `ConfigReconciler` alongside existing low-level CRDs (Option B)? This is the most impactful architectural decision and must be resolved before implementation.

2. **Destination granularity:** `Destination` now carries `prefixes` (the subnets reachable via the VRF). Should it also carry route aggregation rules, default communities, or other policy? How much policy belongs on the `Destination` vs. the attachment?

3. **SBR and intermediate VRF generation:** When a `Layer2Attachment` or `VRFAttachment` selects multiple destinations, the controller needs to create intermediate local VRFs (`s-<vrf>`) with policy routes — the same pattern as today's `SBRPrefixes`. Should the `Destination` specify whether SBR is needed, or should the controller auto-detect it based on multi-destination selectors?

4. **Destination label conventions:** Should we define a standard set of labels (e.g., `network.t-caas.telekom.com/vrf`, `network.t-caas.telekom.com/zone`) or leave labeling entirely to users?

5. **Multi-destination attachment complexity:** When a label selector matches multiple destinations, the resulting VRF config (SBR, intermediate VRFs, PBR) is complex. Should the first iteration restrict to single-destination selectors?

6. **Worker group to nodeSelector mapping:** How are `workerGroups` mapped to Kubernetes node labels? Is there a well-known label convention (e.g., `node.kubernetes.io/worker-group`)?

7. **MetalLB / Coil integration details:** What version and configuration model of MetalLB and Coil are targeted? Are there existing CRDs we need to match?

8. **Bidirectional connections:** The input document notes bidirectional connections are not in the first iteration. What timeline and design is planned?

9. **Escape hatch interaction:** When a user creates both intent-based and low-level CRDs, how are conflicts detected and resolved? Should the operator reject low-level CRDs that configure the same VRF as an intent-produced config?

10. **VNI requirement on Layer2Attachment vs. Destination:** Should VNI be on the attachment (layer-2 specific), on the destination (VRF-level), or both? For L2 attachments the VNI is L2-specific (VXLAN for the bridge), while for destinations the VNI is L3 (VRF VXLAN). These are different VNIs.

11. **Collector / TrafficMirror and Proposal 01 coexistence:** `Collector` maps to `MirrorTarget` and `TrafficMirror` maps to `MirrorSelector`. When using Option A (§3.3), should the generated low-level mirror CRDs be visible to users or hidden? Should users be prevented from creating `MirrorTarget` / `MirrorSelector` resources that conflict with `Collector` / `TrafficMirror`-managed ones?

12. **Mirror VRF Destination lifecycle:** Should the mirror `Destination` (VRF) be auto-created by the first `Collector` that needs it, or must the user always pre-create it? Auto-creation reduces boilerplate but means the operator must pick VNI/RT values.

13. **GRE interface naming with multiple Collectors:** Multiple `Collector` resources may share the same mirror VRF but target different collector IPs. Each needs a unique GRE interface name (≤15 chars). Proposed: `gre.<hash>` / `gretap.<hash>`. Is this acceptable?

14. **Collector node scope:** A `Collector` sets up GRE tunnels on nodes, but which nodes? The node scope is implicitly determined by which `TrafficMirror` resources reference the collector (and their source attachments' `workerGroups`). Should the collector also support an explicit node selector, or is the implicit derivation sufficient?

## 9. Considerations

### 9.1 Future: Automatic Network Allocation

The intent CRD API reserves fields (`harmonization`, `networkName`, `ipv4.size`) that are not processed in this iteration. A future enhancement can add a management cluster controller that:

- Watches intent CRDs (or intermediate request resources) across tenant clusters.
- Allocates networks via BM4X, OpenStack, or vSphere APIs.
- Writes results back into the intent CRD's `.status` or into a dedicated allocation resource.

This is a **transparent enhancement** — the intent CRD API does not change, only the controller gains the ability to fill in unspecified parameters from external allocators. For now, users must specify all network parameters directly.

### 9.2 Incremental Delivery

Given the breadth of the design, we prioritize delivery of value:

| Priority | Component | Rationale |
|---|---|---|
| P0 | Resolve pipeline integration (§3.3) | Architectural foundation — must decide before implementation |
| P0 | `Destination` CRD + controller | Foundation — all other intent CRDs depend on it |
| P0 | `Layer2Attachment` (non-SRIOV, single destination) | Most common use case; highest ops burden today |
| P0 | `VRFAttachment` (inbound only, single destination) | Load-balanced services are the primary connectivity need |
| P1 | Multi-destination support (SBR, intermediate VRFs) | Required for complex multi-VRF topologies |
| P1 | `Layer2Attachment` (SR-IOV) | SR-IOV specific logic |
| P1 | `VRFAttachment` (outbound) | Egress NAT is needed but less frequent |
| P2 | `BGPNeighbor` | Advanced use case, existing workarounds exist |
| P2 | `PodNetwork` | Lower demand; Calico integration complexity |
| P2 | `Collector` + `TrafficMirror` (single collector, single source) | Depends on Proposal 01 low-level implementation; high value for observability |
| P3 | Automatic network allocation (mgmt cluster) | Can be manual/user-specified initially |
| P3 | DNS integration | Can be manual initially |

## 10. Decision Record

| # | Decision | Rationale |
|---|---|---|
| D1 | Introduce intent-based CRDs (`Destination`, `Layer2Attachment`, `VRFAttachment`, `BGPNeighbor`, `PodNetwork`, `Collector`, `TrafficMirror`) | Reduce configuration complexity; enable tenant self-service |
| D2 | **`Destination` as a first-class, labeled, referenceable CRD** — VRFs are defined once and selected by label from attachments | Avoids VRF duplication across attachments; preserves composability of today's multi-`VRFRouteConfiguration` merging; enables grouping and loose coupling |
| D3 | **OPEN** — Pipeline integration approach: Option A (Intent → Low-Level CRDs → Revision) vs. Option B (Intent + Low-Level CRDs → Revision directly) | See §3.3 — both preserve the revision-based rollout; trade-off is implementation simplicity vs. architectural cleanliness |
| D4 | All controllers run in the **tenant cluster only** — no management cluster component in this iteration | Simplifies architecture; auto-allocation can be added later transparently |
| D5 | All network parameters (VLAN, VNI, subnet, IPs) are **user-specified** — no automatic allocation | Reduces complexity; fields for future allocation are reserved in the API |
| D6 | **Shared `NetworkSpec` type** across all attachment CRDs (without VRFs — those come from `Destination` references) | DRY; consistent user experience for network specification |
| D7 | Coexistence of intent-based and low-level CRDs during migration | Non-disruptive adoption; escape hatch for edge cases |
| D8 | Prioritize `Destination` + `Layer2Attachment` (non-SRIOV) + `VRFAttachment` (inbound) for first iteration | Highest value, most common use cases, fastest ops burden reduction |
| D9 | Bidirectional connections deferred to a later iteration | Reduces first-iteration scope; inbound + outbound cover the majority of use cases |
| D10 | Integrate intent controllers into the existing network-operator | Avoid deploying a separate controller; leverage existing RBAC, scheme, and manager setup |
| D11 | **`Collector` + `TrafficMirror` split** — `Collector` (GRE endpoint + mirror VRF binding, defined once) and `TrafficMirror` (source + direction + filter, per-flow) | Same pattern as `Destination`: shared infrastructure defined once, referenced many times. Avoids duplicating collector config across mirror rules. Maps cleanly to low-level `MirrorTarget` / `MirrorSelector` |
| D12 | **Mirror VRF modeled as a `Destination` with `loopbacks`** — consistent with production VRFs | Reuses existing Destination/label-selector patterns; loopback + IPAM allocation flows through the standard pipeline; multiple Collectors can share the same mirror Destination |
