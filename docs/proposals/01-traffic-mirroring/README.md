# Proposal 01 — Traffic Mirroring

- **Status:** Draft
- **Date:** 2026-02-07
- **Authors:** das-schiff network-operator team

## 1. Summary

This proposal describes how to implement end-to-end traffic mirroring in the das-schiff network operator.
The goal is to allow users to declaratively mirror selected traffic flows from Layer 2 (secondary network) or VRF (fabric) interfaces towards a remote GRE-encapsulated collector, while keeping the operator's revision-based, node-by-node rollout model intact.

## 2. Current State

### 2.1 Existing CRDs

The API types are already scaffolded but **not wired up**:

| Resource | Scope | File | Status |
|---|---|---|---|
| `MirrorTarget` | Cluster | `api/v1alpha1/mirrortarget_types.go` | Types + CRD exist, **not reconciled** |
| `MirrorSelector` | Cluster | `api/v1alpha1/mirrorselector_types.go` | Types + CRD exist, **not reconciled** |

**MirrorTarget** describes *where* mirrored traffic is sent:

```go
type MirrorTargetSpec struct {
    Type            MirrorTargetType               // "l2gre" | "l3gre"
    DestinationIP   string                          // remote collector IP
    TunnelKey       *uint32                         // optional GRE key
    DestinationVrf  string                          // VRF where the GRE tunnel lives
    SourceLoopback  string                          // loopback name within the VRF to use as GRE source
}
```

`SourceLoopback` explicitly selects which loopback interface (and therefore which IPAM pool / allocated IP) within `DestinationVrf` is used as the GRE tunnel source address. This avoids ambiguity when a VRF has multiple loopbacks — without it, the operator would have to guess (e.g. pick the first, or the one with the lowest IP), which is fragile and non-deterministic.

**MirrorSelector** describes *what* traffic to capture and in which direction:

```go
type MirrorSelectorSpec struct {
    TrafficMatch TrafficMatch                    // src/dst prefix, port, protocol
    MirrorTarget corev1.TypedObjectReference     // → MirrorTarget
    MirrorSource corev1.TypedObjectReference     // → Layer2NetworkConfiguration or VRFRouteConfiguration
    Direction    MirrorDirection                 // "ingress" | "egress"
}
```

### 2.2 Existing Data Path — Already Implemented

The full data path from `NodeNetworkConfig` down to the CRA-VSR agent is **already implemented** by the vendor (6WIND). The `pkg/cra-vsr` package must not be modified by us.

#### NodeNetworkConfig Model

`MirrorACLs` are carried in both `Layer2` and `VRF`:

```go
type MirrorACL struct {
    TrafficMatch      TrafficMatch    `json:"trafficMatch"`
    MirrorDestination string          `json:"mirrorDestination"` // name of the GRE interface to mirror to
    Direction         MirrorDirection `json:"direction"`         // "ingress" | "egress"
}
```

`VRF` already supports `GREs` (tunnel interfaces) and `Loopbacks` (per-VRF loopback IPs):

```go
type VRF struct {
    Loopbacks    map[string]Loopback `json:"loopbacks,omitempty"`
    GREs         map[string]GRE      `json:"gres,omitempty"`
    MirrorACLs   []MirrorACL         `json:"mirrorAcls,omitempty"`
    // ... BGPPeers, VRFImports, StaticRoutes, PolicyRoutes, etc.
}

type GRE struct {
    DestinationAddress string  `json:"destinationAddress"`
    SourceAddress      string  `json:"sourceAddress"`
    Layer              GRELayer `json:"layer"`             // "layer2" | "layer3"
    EncapsulationKey   *uint32  `json:"encapsulationKey,omitempty"`
}
```

#### CRA-VSR Agent — Already Handles All of This

The `pkg/cra-vsr` package (vendor-maintained, **must not be modified**) already:

1. **Creates GRE/GRETap interfaces** inside VRFs (`layer3.go` → `setupGRE`).
2. **Creates loopback interfaces** inside VRFs (`layer3.go` → `setupLoopback`).
3. **Programs `<mirror-traffic>` rules** from `MirrorACLs` (`create.go` → `createMirrorTraffic`):
   - For **Layer2**: binds to the bridge interface `l2.<vlan>` (bridge name).
   - For **VRF**: binds to the VXLAN interface `vx.<vrf>` (only when VNI exists or VRF is reserved).
4. Maps `MirrorACL.MirrorDestination` to the `<to>` target (the GRE interface name).
5. Maps `MirrorACL.Direction` to `<direction>` (`ingress` / `egress`).
6. Builds traffic match filters with src/dst prefix/address, ports, and protocol.

### 2.3 What Is Missing (Operator-Side Only)

1. **Config controller** (`controllers/operator/config_controller.go`) does not watch `MirrorSelector` / `MirrorTarget`.
2. **NetworkConfigRevision** does not carry any mirror data — it only stores `Layer2Revision`, `VRFRevision`, and `BGPRevision`.
3. **Config-build step** (`pkg/reconciler/operator/`) never populates `MirrorACLs` on the per-node `NodeNetworkConfig`.
4. **Source IP address management** — each node needs a unique per-VRF source IP for the GRE tunnel, assigned as a loopback and advertised via EVPN export filter. The `VRFRouteConfiguration` will carry loopback definitions with a `poolRef` to a Cluster API IPAM pool; the operator creates `IPAddressClaim`s per node.
5. **The GRE interface and loopback IP** entries are not populated in the `NodeNetworkConfig`.

> **Key insight:** The entire CRA-VSR agent side is done. The only work is in the **operator** (reconciler / config-build step).

## 3. Design Decisions

### 3.1 Interface Binding Rules

Based on the source type referenced by a `MirrorSelector`:

| MirrorSource Kind | Bind Interface | Notes |
|---|---|---|
| `Layer2NetworkConfiguration` (VLAN *n*) | `l2.<n>` (ingress / egress) | Captures traffic to/from secondary-network pods. CRA-VSR binds to the bridge `l2.<n>`. |
| `VRFRouteConfiguration` (VRF *v*) | `vx.<v>` if VNI present, otherwise **skip** | Captures traffic before/after VXLAN encapsulation on the fabric VRF. CRA-VSR only programs mirror-traffic rules when the VRF has a VNI or is a reserved VRF. |

> **Rationale:** We want to see packets *before* encapsulation towards the fabric and *after* decapsulation from the fabric. The `vx.<vrf>` VXLAN interface is the best attach point. If the VRF has no VXLAN (e.g. a local VRF without VNI), the mirror selector is silently ignored on that node.

### 3.2 Mirror Target VRF — User-Defined via VRFRouteConfiguration

The GRE tunnel used to transport mirrored traffic **MUST live in a dedicated fabric VRF** (a "tap" / "mirror" VRF) that is separate from production VRFs. This avoids:

- Routing conflicts with production traffic.
- Accidental leaking of mirror control-plane into fabric VRFs.
- Issues with overlapping address space between collector and workload networks.

**This VRF is defined by the user through a standard `VRFRouteConfiguration`** — the operator does NOT auto-create it. This keeps the mirror VRF lifecycle consistent with all other VRFs in the system.

The VRF needs:
- A **VNI** and **route-target** (so it participates in EVPN and the collector is reachable).
- **Loopbacks with a `poolRef`** — the `VRFRouteConfiguration` owns the loopback definitions, including the reference to a Cluster API IPAM pool from which per-node IPs are allocated (see §3.3).
- An **EVPN export filter** that permits the per-node source IPs (loopback addresses — the operator auto-appends these at build time).

#### Why the loopback pool reference belongs to the VRF, not MirrorTarget

The previous design had `PoolRef` on the `MirrorTarget`. But the loopback is an **interface inside the VRF** and its IP must appear in the VRF's **EVPN export filter**. Having the MirrorTarget "own" the loopback creates a cross-cutting concern: the target would dictate VRF-level infrastructure that the VRF itself should control.

By placing the loopback (with its pool reference) on the `VRFRouteConfiguration`:

- The loopback flows through the **normal revision pipeline** as part of the VRF config.
- The EVPN export filter enrichment is co-located with the VRF that owns it.
- The `MirrorTarget` stays focused on GRE tunnel properties (destination IP, key, type) plus **which VRF and loopback** to bind to.
- Multiple `MirrorTarget`s can share the same mirror VRF and loopback source IP.

#### MirrorTarget → Loopback Binding

A VRF may have **multiple loopbacks** (e.g. one for mirror GRE sources, one for BGP peering). The `MirrorTarget.Spec.SourceLoopback` field explicitly names the loopback within `DestinationVrf` whose allocated IP will be used as the GRE tunnel's `SourceAddress`.

Without this explicit reference, the operator would need a heuristic to pick a loopback (first in map iteration? lowest IP?), which is non-deterministic and error-prone. The explicit binding keeps things unambiguous:

```
  MirrorTarget "collector-prod"
  ├── destinationVrf:  "mirror"
  ├── sourceLoopback:  "lo.mir"     ← picks this loopback's allocated IP
  ├── destinationIP:   10.250.0.100
  ├── type:            l3gre
  └── key:             1001

  VRFRouteConfiguration "mirror"
  └── loopbacks:
      ├── lo.mir   → poolRef: InClusterIPPool/mirror-source-ips  ← used by collector-prod
      └── lo.bgp   → poolRef: InClusterIPPool/bgp-peering-ips    ← used by something else
```

### 3.3 Per-Node Source IP Address Management via Cluster API IPAM

Each node needs a **unique source IP** for the GRE tunnel endpoint. This is required because:

- The remote collector needs to identify which node each mirrored packet originates from.
- GRE tunnels with overlapping source IPs would collide at the collector.
- The source IP must be reachable from the collector via the mirror VRF's EVPN fabric.

#### Cluster API IPAM Integration

We use the **Cluster API IPAM contract** (`ipam.cluster.x-k8s.io`) for source IP allocation. This is a well-established Kubernetes-native IPAM interface with multiple provider implementations:

- **[In-Cluster Provider](https://github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster)** — lightweight, CRD-based, no external dependencies. Good for dev/test.
- **[Infoblox Provider](https://github.com/telekom/cluster-api-ipam-provider-infoblox)** — integrates with Infoblox NIOS for enterprise IPAM. Likely production choice.

The contract works as follows:

1. The user creates an **IP Pool** (provider-specific CRD, e.g. `InClusterIPPool` or `InfobloxIPPool`).
2. The operator creates an **`IPAddressClaim`** per node, referencing the pool.
3. The IPAM provider fulfils the claim by creating an **`IPAddress`** resource with the allocated address.
4. The operator reads the `IPAddress` and uses it as the loopback/GRE source IP.

```yaml
# Provider-specific pool (InClusterIPPool example)
apiVersion: ipam.cluster.x-k8s.io/v1beta1
kind: InClusterIPPool
metadata:
  name: mirror-source-ips
spec:
  addresses:
  - 10.99.0.1-10.99.0.254
  prefix: 32
---
# Infoblox example
apiVersion: ipam.cluster.x-k8s.io/v1beta1
kind: InfobloxIPPool
metadata:
  name: mirror-source-ips
spec:
  networkView: "default"
  subnet: "10.99.0.0/24"
```

#### Pool Reference on the VRF Loopback

The `VRFRouteConfiguration` is extended with a `loopbacks` field that carries both the loopback name and a `poolRef` to a Cluster API IPAM pool:

```go
type VRFRouteConfigurationSpec struct {
    // ... existing fields (VRF, VNI, RouteTarget, Import, Export, etc.) ...

    // Loopbacks defines loopback interfaces for the VRF with optional per-node IP allocation.
    Loopbacks []VRFLoopback `json:"loopbacks,omitempty"`
}

type VRFLoopback struct {
    // Name is the loopback interface name (e.g. "lo.mir").
    Name string `json:"name"`
    // PoolRef references a Cluster API IPAM pool (e.g. InClusterIPPool, InfobloxIPPool).
    // The operator creates an IPAddressClaim per node and uses the allocated IP.
    PoolRef corev1.TypedObjectReference `json:"poolRef"`
}
```

This design means:
- The loopback is **part of the VRF definition** and flows through the normal revision pipeline.
- The EVPN export filter is enriched per-node by the operator with `permit <allocated-ip>/32`.
- The `MirrorTarget` stays focused on tunnel properties — it does **not** own the loopback or pool.

#### Allocation Flow

1. **Revision pipeline** includes the mirror VRF (it's a standard `VRFRouteConfiguration`).
2. During `NodeNetworkConfig` build, for each VRF loopback with a `poolRef`:
   - Create or look up an `IPAddressClaim` named `<vrf>-<loopback>-<node>` with owner ref to the `NodeNetworkConfig`.
   - Wait for the IPAM provider to fulfil it → read the `IPAddress.Spec.Address`.
   - Populate `FabricVRFs["<vrf>"].Loopbacks["<name>"]` with the allocated IP.
   - Auto-append `permit <ip>/32` to the VRF's `EVPNExportFilter`.
3. GRE tunnel interfaces (created by `MirrorTarget` resolution) use the loopback IP as `SourceAddress`.

```
  Mirror VRF (e.g. "mirror")
  ┌──────────────────────────────────────────────────┐
  │  Loopback lo.mir: 10.99.0.<node>/32              │ ← from IPAddressClaim
  │  GRE      gre.mir: src=10.99.0.<node>            │
  │                    dst=10.250.0.100 (collector)   │
  │  EVPN export filter: permit 10.99.0.<node>/32    │ ← auto-appended
  │  VNI: <configured>                               │
  │  RT:  <configured>                               │
  └──────────────────────────────────────────────────┘
```

#### Lifecycle

- **Node added:** Operator creates `IPAddressClaim` → IPAM allocates → IP used in `NodeNetworkConfig`.
- **Node removed:** `IPAddressClaim` is garbage-collected (owner ref to `NodeNetworkConfig`) → IPAM releases the IP.
- **Stable across reconcile:** Claims are deterministically named, so re-reconcile finds the existing claim + address.

> **Note:** The loopback model is already supported — `VRF.Loopbacks` exists in the `NodeNetworkConfig` API and CRA-VSR's `layer3.go` → `setupLoopback` renders them as `<loopback>` interfaces inside the VRF. The GRE model is also already supported — `VRF.GREs` exists and CRA-VSR's `layer3.go` → `setupGRE` renders them as `<gre>` or `<gretap>` interfaces.

### 3.4 Should Mirror Data Live in the NetworkConfigRevision?

This is the central architectural question. There are two options:

#### Option A — Embed in Revision (current pattern)

Add `MirrorRevision` slices to `NetworkConfigRevisionSpec`, exactly like Layer2/VRF/BGP:

```go
type NetworkConfigRevisionSpec struct {
    Layer2 []Layer2Revision `json:"layer2,omitempty"`
    Vrf    []VRFRevision    `json:"vrf,omitempty"`
    BGP    []BGPRevision    `json:"bgp,omitempty"`
    Mirror []MirrorRevision `json:"mirror,omitempty"`  // NEW
}

type MirrorRevision struct {
    SelectorName string             `json:"selectorName"`
    TargetName   string             `json:"targetName"`
    MirrorSelectorSpec `json:",inline"`
    MirrorTargetSpec   `json:",inline"`
}
```

| Pro | Con |
|---|---|
| Single source of truth per revision — full snapshot | Revision CR grows with every mirror rule; can become very large |
| Atomic rollout — mirror rules roll out node-by-node together with L2/L3/BGP changes | Any mirror rule change triggers a full revision bump and node-by-node rollout, even though mirror is a low-risk, additive change |
| Simple: agents only read `NodeNetworkConfig` | - |

#### Option B — Keep Mirrors Out of Revision (recommended) ✅

Mirror selectors and targets are **resolved at NodeNetworkConfig build time only**, but are **not stored** in the `NetworkConfigRevisionSpec`. The revision hash is still computed from L2+VRF+BGP, meaning mirror-only changes do **not** create a new revision. Instead:

1. When a new revision is deployed (or when a `MirrorSelector` / `MirrorTarget` changes), the operator rebuilds `NodeNetworkConfig` objects.
2. The config build step resolves `MirrorSelector` → `MirrorTarget` references, maps `MirrorSource` to the correct interface, and writes `MirrorACLs` into the `NodeNetworkConfig.Spec.Layer2s[*].MirrorACLs` or `.FabricVRFs[*].MirrorACLs`.
3. The per-node `NodeNetworkConfig` is the only place that carries fully-resolved mirror ACLs — agents never see the high-level CRDs.

```
 ┌──────────────┐   ┌──────────────┐
 │MirrorSelector│   │ MirrorTarget │
 └──────┬───────┘   └──────┬───────┘
        │                  │
        └──────┬───────────┘
               │  resolve at build time
               ▼
 ┌─────────────────────────┐
 │  NetworkConfigRevision  │  (unchanged — no mirror data)
 │  L2 + VRF + BGP only    │
 └───────────┬─────────────┘
             │  build NodeNetworkConfig
             ▼
 ┌──────────────────────────────────────────────────────┐
 │      NodeNetworkConfig                               │
 │  .Layer2s[*].MirrorACLs      ← from resolved selectors │
 │  .FabricVRFs[*].MirrorACLs   ← from resolved selectors │
 │  .FabricVRFs["mirror"].GREs  ← GRE tunnel interface    │
 │  .FabricVRFs["mirror"].Loopbacks ← per-node source IP  │
 │  .FabricVRFs["mirror"].EVPNExportFilter ← permit src IP│
 └──────────────────────────────────────────────────────┘
```

| Pro | Con |
|---|---|
| Revision CR stays small; no bloat from potentially many ACL rules | Mirror rules are not snapshotted in the revision — harder to diff "what mirror config was active at revision X" |
| Mirror-only changes skip the heavy node-by-node rollout (they are additive/safe) | Mirror changes update `NodeNetworkConfig` directly, bypassing the gated rollout |
| `MirrorACLs` already exist on the `NodeNetworkConfig` types — minimal API change | Need a separate reconcile trigger for mirror CRD changes |

#### Recommendation

**Option B** is recommended, with one enhancement: the operator should still trigger a `NodeNetworkConfig` update for all affected nodes when a `MirrorSelector` or `MirrorTarget` changes. This can be done via a lightweight reconciler that watches mirror CRDs and re-builds affected node configs without bumping the revision.

The rationale:

- Mirror rules are **additive** and **non-disruptive** — adding/removing a mirror ACL does not affect forwarding of production traffic.
- The `NetworkConfigRevision` CR already risks growing large with many L2/VRF/BGP entries. Adding every mirror rule would make it worse.
- The revision's purpose is **gated, safe rollout of forwarding-plane changes**. Mirroring is an observability feature that does not alter the forwarding plane.
- The mirror destination VRF itself (with VNI + RT + loopback definitions) is a standard `VRFRouteConfiguration`, so it **is** part of the revision already. The loopback spec (name + pool ref) is snapshotted; only the resolved per-node IP, GRE interface, and ACL entries are injected at build time.

If auditability is needed, the operator can record applied mirror state in `MirrorSelector.Status` or in `NodeNetworkConfig` annotations.

## 4. Implementation Plan

> **Scope:** All implementation is in the **operator** only (`controllers/operator/`, `pkg/reconciler/operator/`). The `pkg/cra-vsr` package is vendor-maintained and already handles everything on the agent side.

### Phase 1 — Operator-Side Wiring

1. **Watch MirrorSelector + MirrorTarget** in `config_controller.go`:
   ```go
   Watches(&networkv1alpha1.MirrorSelector{}, h).
   Watches(&networkv1alpha1.MirrorTarget{}, h).
   ```

2. **Add `fetchMirrorData`** in `config_reconciler.go` to list all `MirrorSelector` and `MirrorTarget` objects and resolve references. Mirror data is **not** added to the revision — only used at `NodeNetworkConfig` build time.

### Phase 2 — NodeNetworkConfig Build: Mirror ACL + GRE + Loopback + Export Filter

In `configrevision_reconciler.go` → `CreateNodeNetworkConfig`, after building VRF/L2/BGP:

For each `MirrorSelector`:

1. **Resolve references:**
   - Look up the referenced `MirrorTarget` → get collector IP, GRE key, type (l2gre/l3gre), and source IP pool.
   - Look up the referenced `MirrorSource` → get the `Layer2NetworkConfiguration` or `VRFRouteConfiguration` (including its `NodeSelector`).

2. **Check node eligibility:** Only add mirror config to nodes matched by the source's `NodeSelector`.

3. **Ensure the mirror destination VRF and loopback exist:**
   - The mirror destination VRF is identified by `MirrorTarget.Spec.DestinationVrf` (e.g. `"mirror"`).
   - It must be a **user-created `VRFRouteConfiguration`** (with VNI + RT) and already present in `FabricVRFs` on the node. If not → set error status.
   - The loopback named by `MirrorTarget.Spec.SourceLoopback` (e.g. `"lo.mir"`) must exist in the VRF's `loopbacks` list with a valid `poolRef`. If not → set error status.

4. **Resolve the source loopback IP via CAPI IPAM:**
   - Look up the `VRFLoopback` named by `MirrorTarget.Spec.SourceLoopback` on the mirror VRF's `VRFRouteConfiguration`.
   - Create or find the `IPAddressClaim` named `<vrf>-<loopback>-<node>` (Cluster API IPAM contract) using the loopback's `poolRef`.
   - Read the fulfilled `IPAddress.Spec.Address` from the IPAM provider.
   - If the claim is not yet fulfilled, the `NodeNetworkConfig` build is retried (requeue).

5. **Inject loopback into the mirror VRF:**
   ```go
   fabricVrf.Loopbacks["lo.mir"] = v1alpha1.Loopback{
       IPAddresses: []string{"<allocated-ip>/32"},
   }
   ```

6. **Inject GRE tunnel interface into the mirror VRF:**
   ```go
   greName := "gre.mir"  // or "gretap.mir" for l2gre
   fabricVrf.GREs[greName] = v1alpha1.GRE{
       SourceAddress:      "<allocated-ip>",  // from IPAddress of the sourceLoopback
       DestinationAddress: target.Spec.DestinationIP,
       Layer:              "layer3",  // or "layer2" for l2gre
       EncapsulationKey:   target.Spec.TunnelKey,
   }
   ```

7. **Add source IP to the EVPN export filter** so it is advertised into the fabric:
   ```go
   fabricVrf.EVPNExportFilter.Items = append(fabricVrf.EVPNExportFilter.Items,
       v1alpha1.FilterItem{
           Matcher: v1alpha1.Matcher{
               Prefix: &v1alpha1.PrefixMatcher{
                   Prefix: "<allocated-ip>/32",
               },
           },
           Action: v1alpha1.Action{Type: v1alpha1.Accept},
       },
   )
   ```

8. **Build and attach `MirrorACL`** to the source L2 or VRF:
   - L2 source with VLAN `n` → append to `Layer2s["<n>"].MirrorACLs`.
   - VRF source with VRF `v` → append to `FabricVRFs["<v>"].MirrorACLs`.
   ```go
   MirrorACL{
       TrafficMatch:      selector.Spec.TrafficMatch,
       MirrorDestination: greName,   // e.g. "gre.mir" — the GRE interface name
       Direction:         selector.Spec.Direction,
   }
   ```

### Phase 3 — Cluster API IPAM Integration

1. **Add `ipam.cluster.x-k8s.io/v1beta1` to the scheme** and RBAC for `IPAddressClaim` / `IPAddress` resources.
2. **Watch `IPAddress`** — when an IPAM provider fulfils a claim, the operator must re-reconcile the affected `NodeNetworkConfig`.
3. **Claim lifecycle:**
   - Create: `IPAddressClaim` with deterministic name `<vrf>-<loopback>-<node>`, owner ref to `NodeNetworkConfig`, pool ref from `VRFLoopback.PoolRef`.
   - Read: Fulfilled claim → `Status.Address` → read `IPAddress.Spec.Address`.
   - Delete: Garbage-collected when the `NodeNetworkConfig` is deleted (owner ref).
4. **Provider deployment** is out of scope of this operator but must be documented:
   - Dev/test: deploy `cluster-api-ipam-provider-in-cluster` + create `InClusterIPPool`.
   - Production: deploy Infoblox IPAM provider + create `InfobloxIPPool`.

### Phase 4 — Status Reporting

- Set `MirrorSelector.Status` with conditions:
  - `Resolved` — target and source references are valid.
  - `Applied` — ACLs have been programmed on all matching nodes.
- Set `MirrorTarget.Status` with:
  - `ActiveSelectors` — count of selectors referencing this target.
  - `ActiveNodes` — count of nodes where the tunnel is configured.

## 5. CRD Examples

### Prerequisites — IP Pool (Cluster API IPAM)

Deploy a Cluster API IPAM provider and create a pool. **In-Cluster** example for dev/test:

```yaml
apiVersion: ipam.cluster.x-k8s.io/v1beta1
kind: InClusterIPPool
metadata:
  name: mirror-source-ips
spec:
  addresses:
  - 10.99.0.1-10.99.0.254
  prefix: 32
```

**Infoblox** example for production:

```yaml
apiVersion: ipam.cluster.x-k8s.io/v1beta1
kind: InfobloxIPPool
metadata:
  name: mirror-source-ips
spec:
  networkView: "default"
  subnet: "10.99.0.0/24"
```

### Mirror Destination VRF (user-created, with loopback pool ref)

The mirror VRF is a standard `VRFRouteConfiguration`. The `loopbacks` field references the IPAM pool:

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: VRFRouteConfiguration
metadata:
  name: mirror-vrf-export
spec:
  vrf: mirror
  vni: 99001
  routeTarget: "65000:99001"
  seq: 100
  import: []
  export:
  - cidr: "0.0.0.0/0"
    action: deny
  # EVPN export filter is further enriched per-node by the operator
  # with the allocated source IPs (permit <src-ip>/32)
  loopbacks:
  - name: lo.mir
    poolRef:
      apiGroup: ipam.cluster.x-k8s.io
      kind: InClusterIPPool       # or InfobloxIPPool
      name: mirror-source-ips
```

### MirrorTarget — GRE collector

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: MirrorTarget
metadata:
  name: collector-prod
spec:
  type: l3gre
  destinationIP: 10.250.0.100
  key: 1001
  destinationVrf: mirror    # VRF where the GRE tunnel lives
  sourceLoopback: lo.mir     # loopback within the VRF → determines GRE source IP
```

> **CRD change required:** Add `destinationVrf` and `sourceLoopback` fields to `MirrorTargetSpec`; remove `poolRef`.

### MirrorSelector — Mirror L2 ingress traffic

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: MirrorSelector
metadata:
  name: mirror-vlan100-ingress
spec:
  trafficMatch:
    srcPrefix: "10.100.0.0/16"
    protocol: "tcp"
    dstPort: 443
  mirrorTarget:
    apiGroup: network.t-caas.telekom.com
    kind: MirrorTarget
    name: collector-prod
  mirrorSource:
    apiGroup: network.t-caas.telekom.com
    kind: Layer2NetworkConfiguration
    name: vlan100
  direction: ingress
```

### MirrorSelector — Mirror VRF egress traffic

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: MirrorSelector
metadata:
  name: mirror-ext-egress
spec:
  trafficMatch:
    dstPrefix: "0.0.0.0/0"
  mirrorTarget:
    apiGroup: network.t-caas.telekom.com
    kind: MirrorTarget
    name: collector-prod
  mirrorSource:
    apiGroup: network.t-caas.telekom.com
    kind: VRFRouteConfiguration
    name: external
  direction: egress
```

### Resulting NodeNetworkConfig (excerpt, for node-01)

```yaml
spec:
  fabricVRFs:
    "mirror":
      vni: 99001
      evpnImportRouteTargets: ["65000:99001"]
      evpnExportRouteTargets: ["65000:99001"]
      evpnExportFilter:
        defaultAction:
          type: reject
        items:
        - matcher:
            prefix:
              prefix: "10.99.0.1/32"
          action:
            type: accept
      loopbacks:
        "lo.mir":
          ipAddresses: ["10.99.0.1/32"]
      gres:
        "gre.mir":
          sourceAddress: "10.99.0.1"
          destinationAddress: "10.250.0.100"
          layer: layer3
          encapsulationKey: 1001
    "external":
      # ... normal external VRF config ...
      mirrorAcls:
      - trafficMatch:
          dstPrefix: "0.0.0.0/0"
        mirrorDestination: "gre.mir"
        direction: egress
  layer2s:
    "100":
      vni: 10100
      vlan: 100
      mtu: 9000
      mirrorAcls:
      - trafficMatch:
          srcPrefix: "10.100.0.0/16"
          protocol: "tcp"
          dstPort: 443
        mirrorDestination: "gre.mir"
        direction: ingress
```

## 6. Data Flow Diagram

```
  User creates:
  ┌────────────────────┐    ┌───────────────┐
  │  MirrorSelector    │───▶│  MirrorTarget │
  │  (what + where)    │    │  (collector + │
  └────────┬───────────┘    │   dest VRF)   │
           │                └───────────────┘
           │  references
           ▼
  ┌────────────────────┐    ┌───────────────────────────────────────┐
  │  MirrorSource:     │    │  VRFRouteConfiguration "mirror"      │
  │  L2NetConfig or    │    │  (loopbacks:                         │
  │  VRFRouteConfig    │    │    - name: lo.mir                    │
  └────────────────────┘    │      poolRef: → InClusterIPPool /   │
                            │                 InfobloxIPPool)      │
                            └──────────────────┬────────────────────┘
                                               │ poolRef
                                               ▼
                            ┌───────────────────────────────────────┐
                            │  CAPI IPAM Pool                       │
                            │  (InClusterIPPool / InfobloxIPPool)   │
                            └───────────────────────────────────────┘

  Operator reconcile loop (pkg/reconciler/operator):
  ┌──────────────────────────────────────────────────────────────────┐
  │                                                                  │
  │  1. List MirrorSelectors + MirrorTargets                        │
  │  2. Resolve MirrorTarget → get collector IP, key, type, VRF,    │
  │     and sourceLoopback                                          │
  │  3. Resolve MirrorSource → get L2 VLAN or VRF name             │
  │  4. Match nodes via source's nodeSelector                       │
  │  5. For VRF loopbacks with poolRef:                             │
  │     a) Create/find IPAddressClaim per node (CAPI IPAM)          │
  │     b) Read fulfilled IPAddress → get allocated IP              │
  │  6. Into mirror VRF on NodeNetworkConfig:                       │
  │     a) Add Loopback with allocated IP                           │
  │     b) Add GRE tunnel interface (src=loopback, dst=collector)   │
  │     c) Auto-append source IP to EVPN export filter              │
  │  7. Build MirrorACL entries (mirrorDestination = GRE intf name) │
  │  8. Attach MirrorACLs to source L2 / VRF                       │
  │                                                                  │
  └────────────────────┬─────────────────────────────────────────────┘
                       │
                       ▼
  ┌────────────────────────────────────────────────────────┐
  │      NodeNetworkConfig (per node)                      │
  │  .FabricVRFs["mirror"].Loopbacks["lo.mir"]  = src IP   │
  │  .FabricVRFs["mirror"].GREs["gre.mir"]      = tunnel   │
  │  .FabricVRFs["mirror"].EVPNExportFilter     += src IP  │
  │  .Layer2s["100"].MirrorACLs                 += ACL     │
  │  .FabricVRFs["ext"].MirrorACLs              += ACL     │
  └────────────────────┬───────────────────────────────────┘
                       │
                       ▼
  ┌──────────────────────────────────────────────────────────┐
  │  CRA-VSR Agent (per node, vendor-maintained, no changes) │
  │  Already handles:                                        │
  │  1. Create VRF + VXLAN (from FabricVRFs)                │
  │  2. Create loopback in VRF (from Loopbacks)             │
  │  3. Create GRE/GRETap in VRF (from GREs)               │
  │  4. Program <mirror-traffic> rules (from MirrorACLs):   │
  │     - <rule from="l2.100" direction="ingress"           │
  │            to="gre.mir" filter="..."/>                   │
  │     - <rule from="vx.ext" direction="egress"            │
  │            to="gre.mir" filter="..."/>                   │
  │  5. Build traffic match filters per filter rule          │
  └──────────────────────────────────────────────────────────┘
```

## 7. Open Questions

1. **IPAM provider deployment:** The operator depends on a Cluster API IPAM provider being deployed. This must be documented as a prerequisite. For production the Infoblox provider is the likely choice; for dev/test the in-cluster provider suffices. Should the operator verify the provider is running and surface errors if claims remain unfulfilled?

2. **`IPAddressClaim` requeue strategy:** If the IPAM provider has not yet fulfilled a claim when `NodeNetworkConfig` is being built, the operator must requeue. What is the appropriate backoff? Should there be a timeout after which the `MirrorSelector` status is set to `Error`?

3. **Rate limiting / sampling:** Should `MirrorSelector` support a `sampleRate` field (e.g. 1:1000) to reduce volume? This can be added later.

4. **Multiple targets per selector:** Currently each selector points to exactly one target. Multi-target (e.g. primary + backup collector) can be a future extension.

5. **EVPN export filter merging:** The per-node source IP must be appended to the mirror VRF's EVPN export filter alongside whatever the user configured in the `VRFRouteConfiguration`. The operator must merge these carefully without overwriting user-defined filter items.

6. **CRA-FRR support:** This proposal focuses on the CRA-VSR path. The `cra-frr` agent would need equivalent mirror programming — this can be a follow-up.

7. **GRE interface naming:** With multiple `MirrorTarget`s, each needs a unique GRE interface name inside the same VRF. Naming scheme: `gre.<target-hash>` (or `gretap.<target-hash>` for l2gre), staying within the 15-char Linux interface name limit.

8. **Loopback as a general VRF feature:** The `loopbacks` field with `poolRef` on `VRFRouteConfiguration` is useful beyond mirroring (e.g. BGP peering loopbacks). Should it be designed as a generic feature from the start?

## 8. Decision Record

| # | Decision | Rationale |
|---|---|---|
| D1 | Keep mirror data **out of** `NetworkConfigRevision` | Avoids CR bloat; mirror is additive/non-disruptive |
| D2 | Resolve mirror rules at `NodeNetworkConfig` build time | Agents stay simple — they only read the fully-resolved per-node config |
| D3 | Mirror VRF is a **user-created `VRFRouteConfiguration`** (fabric VRF with VNI + RT) | Consistent with existing VRF lifecycle; enables EVPN reachability to collector |
| D4 | Per-node **source IP as loopback** in the mirror VRF | Each node uniquely identified; GRE source is routable via EVPN |
| D5 | Source IP added to **EVPN export filter** (auto-appended by operator) | Collector can reach the node's GRE endpoint via the fabric |
| D6 | **Do not modify** `pkg/cra-vsr` | Vendor-maintained; already supports MirrorACLs, GREs, loopbacks, `<mirror-traffic>` |
| D7 | Bind L2 mirrors to `l2.<vlan>` bridge interface | CRA-VSR uses bridge name as `<from>` for Layer2 mirror-traffic rules |
| D8 | Bind VRF mirrors to `vx.<vrf>` VXLAN interface (if VNI exists) | CRA-VSR uses VXLAN name as `<from>` for VRF mirror-traffic rules |
| D9 | `MirrorACL.MirrorDestination` = GRE interface name | CRA-VSR maps this to the `<to>` field in `<mirror-traffic>` rules |
| D10 | **Loopback `poolRef` lives on `VRFRouteConfiguration`**, not `MirrorTarget` | Loopback is a VRF interface; its IP must be in the VRF's EVPN export filter; co-locating avoids cross-cutting concerns and flows through the revision pipeline |
| D11 | **Remove `PoolRef` from `MirrorTargetSpec`**; add `destinationVrf` + `sourceLoopback` | MirrorTarget specifies tunnel properties + which VRF and loopback to bind the GRE source to; IP allocation is the VRF's responsibility |
| D13 | **`MirrorTarget.SourceLoopback` explicitly selects the loopback** within the VRF | Avoids ambiguity when a VRF has multiple loopbacks; without it the operator would need a non-deterministic heuristic to pick a source IP |
| D12 | Use **Cluster API IPAM** (`ipam.cluster.x-k8s.io`) for source IP allocation | Industry-standard contract; supports in-cluster (dev) and Infoblox (prod) providers; stable per-node allocation with claim/address lifecycle |
