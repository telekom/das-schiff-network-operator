# Proposal 01 — Traffic Mirroring

- **Status:** Implemented
- **Date:** 2026-02-07 (proposal); implemented in the `api/mirror-extensions` work
- **Authors:** das-schiff network-operator team

> **Note:** This document started as a pre-implementation proposal. It has been
> updated to describe the design **as implemented**. Two notable deviations from
> the original draft: (1) per-node source IPs are allocated **deterministically
> from a subnet** declared on the `VRFRouteConfiguration` loopback — there is no
> Cluster API IPAM dependency; and (2) the GRE/GRETap/loopback/`<mirror-traffic>`
> rendering was added **in-repo** to `pkg/cra-vsr` (and the equivalent netlink
> rendering to the CRA-FRR path), rather than relying on a pre-existing vendor
> implementation.

## 1. Summary

This proposal describes how to implement end-to-end traffic mirroring in the das-schiff network operator.
The goal is to allow users to declaratively mirror selected traffic flows from Layer 2 (secondary network) or VRF (fabric) interfaces towards a remote GRE-encapsulated collector, while keeping the operator's revision-based, node-by-node rollout model intact.

## 2. Starting Point

### 2.1 Existing CRDs

At the start of this work the API types were scaffolded but **not wired up**:

| Resource | Scope | File | Starting status |
|---|---|---|---|
| `MirrorTarget` | Cluster | `api/v1alpha1/mirrortarget_types.go` | Types + CRD existed, **not reconciled** |
| `MirrorSelector` | Cluster | `api/v1alpha1/mirrorselector_types.go` | Types + CRD existed, **not reconciled** |

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

`SourceLoopback` explicitly selects which loopback interface (and therefore which per-node allocated IP) within `DestinationVrf` is used as the GRE tunnel source address. This avoids ambiguity when a VRF has multiple loopbacks — without it, the operator would have to guess (e.g. pick the first, or the one with the lowest IP), which is fragile and non-deterministic.

**MirrorSelector** describes *what* traffic to capture and in which direction:

```go
type MirrorSelectorSpec struct {
    TrafficMatch TrafficMatch                    // src/dst prefix, port, protocol
    MirrorTarget corev1.TypedObjectReference     // → MirrorTarget
    MirrorSource corev1.TypedObjectReference     // → Layer2NetworkConfiguration or VRFRouteConfiguration
    Direction    MirrorDirection                 // "ingress" | "egress"
}
```

### 2.2 Data Path — CRA-VSR Rendering

The data path from `NodeNetworkConfig` down to the CRA-VSR agent is provided by the `pkg/cra-vsr` package. Historically this package was treated as vendor-maintained (6WIND) and left untouched, but the GRE/GRETap, VRF loopback and `<mirror-traffic>` rendering needed for traffic mirroring **was added to `pkg/cra-vsr` in-repo as part of this change** (see `layer2.go`, `layer3.go`, `create.go`, `types.go`). The model and rendering described below therefore reflect the post-change state of the package.

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

#### CRA-VSR Agent — Rendering Added In-Repo

The `pkg/cra-vsr` package (extended in-repo by this change):

1. **Creates GRE/GRETap interfaces** inside VRFs (`layer3.go` → `setupGRE`).
2. **Creates loopback interfaces** inside VRFs (`layer3.go` → `setupLoopback`).
3. **Programs `<mirror-traffic>` rules** from `MirrorACLs` (`create.go` → `createMirrorTraffic`):
   - For **Layer2**: binds to the VLAN access port `vlan.<vlan>` of the `l2.<vlan>` bridge (workload-facing, so the direction is inverted).
   - For **VRF**: binds to the VXLAN interface `vx.<vrf>` (only when VNI exists or VRF is reserved).
4. Maps `MirrorACL.MirrorDestination` to the `<to>` target (the GRE interface name).
5. Maps `MirrorACL.Direction` to `<direction>` (`ingress` = to-workload / `egress` = from-workload, inverted to the interface-relative direction on workload-facing ports).
6. Builds traffic match filters with src/dst prefix/address, ports, and protocol.

### 2.3 What Was Missing (Operator-Side) — Now Implemented

At the start, the operator side was unwired. The following were added:

1. **Config controller** (`controllers/operator/config_controller.go`) now watches `MirrorSelector` / `MirrorTarget`.
2. **NetworkConfigRevision** now carries mirror data (`MirrorTargets`, `MirrorSelectors`) and includes it in the revision hash.
3. **Config-build step** (`pkg/reconciler/operator/`) now populates `MirrorACLs` on the per-node `NodeNetworkConfig`.
4. **Source IP address management** — each node gets a unique per-VRF source IP for the GRE tunnel, assigned as a loopback and advertised via the EVPN export filter. The `VRFRouteConfiguration` carries loopback definitions with a `subnet`; the operator allocates a deterministic per-node IP from that subnet at build time (no external IPAM).
5. **GRE interface and loopback IP** entries are populated in the `NodeNetworkConfig`, and the CRA-VSR / CRA-FRR agents render them.

> **Key insight:** mirroring reuses the existing revision → `NodeNetworkConfig` → agent pipeline end-to-end; the operator resolves `MirrorSelector`/`MirrorTarget` into per-node config, and both agents render the resulting GRE/loopback/mirror-traffic.

## 3. Design Decisions

### 3.1 Interface Binding Rules

Based on the source type referenced by a `MirrorSelector`:

| MirrorSource Kind | Bind Interface | Notes |
|---|---|---|
| `Layer2NetworkConfiguration` (VLAN *n*) | `vlan.<n>` (the bridge access port) | Captures traffic to/from secondary-network pods. Both the FRR and CRA-VSR paths attach to the VLAN access port `vlan.<n>` of the `l2.<n>` bridge — not the bridge master — so port-to-port (east-west) traffic between the workload side and the L2VNI overlay (`vx.<vni>`) is captured. |
| `VRFRouteConfiguration` (VRF *v*) | `vx.<v>` if VNI present, otherwise **skip** | Captures traffic before/after VXLAN encapsulation on the fabric VRF. CRA-VSR only programs mirror-traffic rules when the VRF has a VNI or is a reserved VRF. |

> **Rationale:** We want to see packets *before* encapsulation towards the fabric and *after* decapsulation from the fabric. For an L2 source, the `vlan.<n>` access port is the right attach point: a hook on the bridge *master* only sees frames the bridge exchanges with the host stack (SVI/IRB-routed/locally-terminated), whereas port-to-port bridged frames are switched in the fast path and bypass the master. Attaching to the access port captures that east-west traffic. For a VRF source, the `vx.<vrf>` VXLAN interface is the attach point; if the VRF has no VXLAN (e.g. a local VRF without VNI), the mirror selector is silently ignored on that node.

> **Direction semantics:** `direction` is expressed from the workload's point of view — `ingress` = traffic **to** the workload, `egress` = traffic **from** the workload. On the workload-facing `vlan.<n>` access port the underlying interface hooks are inverted accordingly (to-workload = the port's egress hook, from-workload = its ingress hook); on the fabric-facing `vx.<vrf>` port the mapping is natural. This is handled by the agents so the API meaning stays consistent across sources.

### 3.2 Mirror Target VRF — User-Defined via VRFRouteConfiguration

The GRE tunnel used to transport mirrored traffic **MUST live in a dedicated fabric VRF** (a "tap" / "mirror" VRF) that is separate from production VRFs. This avoids:

- Routing conflicts with production traffic.
- Accidental leaking of mirror control-plane into fabric VRFs.
- Issues with overlapping address space between collector and workload networks.

**This VRF is defined by the user through a standard `VRFRouteConfiguration`** — the operator does NOT auto-create it. This keeps the mirror VRF lifecycle consistent with all other VRFs in the system.

The VRF needs:
- A **VNI** and **route-target** (so it participates in EVPN and the collector is reachable).
- **Loopbacks with a `subnet`** — the `VRFRouteConfiguration` owns the loopback definitions, including the CIDR from which the operator allocates a deterministic per-node IP (see §3.3).
- An **EVPN export filter** that permits the per-node source IPs (loopback addresses — the operator auto-appends these at build time).

#### Why the loopback subnet belongs to the VRF, not MirrorTarget

An earlier draft put the source-IP pool on the `MirrorTarget`. But the loopback is an **interface inside the VRF** and its IP must appear in the VRF's **EVPN export filter**. Having the MirrorTarget "own" the loopback creates a cross-cutting concern: the target would dictate VRF-level infrastructure that the VRF itself should control.

By placing the loopback (with its subnet) on the `VRFRouteConfiguration`:

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
      ├── lo.mir   → subnet: 10.99.0.0/24    ← per-node IP allocated here, used by collector-prod
      └── lo.bgp   → subnet: 10.99.1.0/24    ← used by something else
```

### 3.3 Per-Node Source IP Address Management — Deterministic Subnet Allocation

Each node needs a **unique source IP** for the GRE tunnel endpoint. This is required because:

- The remote collector needs to identify which node each mirrored packet originates from.
- GRE tunnels with overlapping source IPs would collide at the collector.
- The source IP must be reachable from the collector via the mirror VRF's EVPN fabric.

#### Deterministic allocation from the loopback subnet

The per-node IP is allocated **deterministically from the CIDR declared on the VRF loopback** — there is no external IPAM dependency. The operator's `loopbackAllocator` computes a stable `node → IP` map for each `<vrf>/<loopback>` subnet:

- Nodes are sorted by name; each gets the lowest free host address in the subnet.
- The IPv4 network and broadcast addresses are skipped.
- **Existing allocations are preserved** — a node keeps the IP already present in its `NodeNetworkConfig`, so re-reconcile is stable. An address that no longer falls inside the (possibly changed) subnet is dropped and the node is re-allocated a valid one.
- Addresses still held by out-of-scope (e.g. temporarily NotReady) nodes are **reserved**, so they are never handed to a different node (no duplicate source IPs).
- The allocation is computed once per `(revision, ready-node set)` and reused across the per-node builds of a rollout.

Because the loopback subnet is part of the `VRFRouteConfiguration` (and thus the revision), and the result is written into each `NodeNetworkConfig`, the allocation flows through the normal gated pipeline with no extra control-plane components.

#### Subnet on the VRF Loopback

The `VRFRouteConfiguration` carries a `loopbacks` field with the loopback name and the subnet to allocate from:

```go
type VRFRouteConfigurationSpec struct {
    // ... existing fields (VRF, VNI, RouteTarget, Import, Export, etc.) ...

    // Loopbacks defines loopback interfaces for the VRF with per-node IP allocation.
    Loopbacks []VRFLoopback `json:"loopbacks,omitempty"`
}

type VRFLoopback struct {
    // Name is the loopback interface name (e.g. "lo.mir"), max 15 chars.
    Name string `json:"name"`
    // Subnet is the CIDR from which a unique per-node loopback IP is allocated.
    Subnet string `json:"subnet"`
}
```

This design means:
- The loopback is **part of the VRF definition** and flows through the normal revision pipeline.
- The EVPN export filter is enriched per-node by the operator with `permit <allocated-ip>/32` (or `/128`).
- The `MirrorTarget` stays focused on tunnel properties — it does **not** own the loopback or its subnet.

> **Subnet sizing:** every node reserves an address from the subnet (whether or not it currently participates in mirroring), so the subnet must be large enough for the whole cluster plus the network/broadcast addresses.

#### Allocation Flow

1. **Revision pipeline** includes the mirror VRF (it's a standard `VRFRouteConfiguration`, with its loopback subnet snapshotted).
2. During `NodeNetworkConfig` build, for each referenced VRF loopback:
   - Compute the deterministic per-node IP from the subnet (preserving any existing allocation).
   - Populate `FabricVRFs["<vrf>"].Loopbacks["<name>"]` with `<ip>/32` (or `/128`).
   - Auto-append `permit <ip>/32` to the VRF's `EVPNExportFilter`.
3. GRE tunnel interfaces (created by `MirrorTarget` resolution) use the loopback IP as `SourceAddress` and bind to the loopback as their source interface.

```
  Mirror VRF (e.g. "mirror")
  ┌──────────────────────────────────────────────────┐
  │  Loopback lo.mir: 10.99.0.<node>/32              │ ← deterministic from subnet
  │  GRE      gre.<hash>: src=10.99.0.<node>          │
  │                       dst=10.250.0.100 (collector)│
  │  EVPN export filter: permit 10.99.0.<node>/32    │ ← auto-appended
  │  VNI: <configured>                               │
  │  RT:  <configured>                               │
  └──────────────────────────────────────────────────┘
```

#### Lifecycle

- **Node added:** the next build includes the new ready node in the allocation; it gets the lowest free address.
- **Node removed:** once its `NodeNetworkConfig` is garbage-collected, its address frees up for reuse.
- **Stable across reconcile:** allocation is deterministic and preserves existing per-node IPs.

> **Note:** The loopback model is supported end-to-end — `VRF.Loopbacks` exists in the `NodeNetworkConfig` API; CRA-VSR's `layer3.go` → `setupLoopback` renders them as `<loopback>` interfaces and the CRA-FRR path (`pkg/nl`) creates dummy loopbacks inside the VRF. The GRE model is likewise supported — `VRF.GREs` is rendered as `<gre>`/`<gretap>` by CRA-VSR and as netlink GRE/GRETAP (incl. IP6GRE) by CRA-FRR.

### 3.4 Should Mirror Data Live in the NetworkConfigRevision?

This is the central architectural question. There are two options:

#### Option A — Embed in Revision (current pattern) ✅ (implemented)

Add mirror snapshots to `NetworkConfigRevisionSpec`, exactly like Layer2/VRF/BGP. This is what the implementation does, via `MirrorTargets` and `MirrorSelectors`:

```go
type NetworkConfigRevisionSpec struct {
    Layer2          []Layer2Revision         `json:"layer2,omitempty"`
    Vrf             []VRFRevision            `json:"vrf,omitempty"`
    BGP             []BGPRevision            `json:"bgp,omitempty"`
    MirrorTargets   []MirrorTargetRevision   `json:"mirrorTargets,omitempty"`   // NEW
    MirrorSelectors []MirrorSelectorRevision `json:"mirrorSelectors,omitempty"` // NEW
}

type MirrorTargetRevision struct {
    Name             string `json:"name"`
    MirrorTargetSpec `json:",inline"`
}

type MirrorSelectorRevision struct {
    Name               string `json:"name"`
    MirrorSelectorSpec `json:",inline"`
}
```

These snapshots are included in the revision hash computed by `NewRevision`, so any mirror change produces a new revision and rolls out through the normal gated, node-by-node pipeline.

| Pro | Con |
|---|---|
| Single source of truth per revision — full snapshot | Revision CR grows with every mirror rule; can become very large |
| Atomic rollout — mirror rules roll out node-by-node together with L2/L3/BGP changes | Any mirror rule change triggers a full revision bump and node-by-node rollout, even though mirror is a low-risk, additive change |
| Simple: agents only read `NodeNetworkConfig` | - |

#### Option B — Keep Mirrors Out of Revision (considered, not adopted)

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

**Option A** was implemented: mirror selectors and targets are snapshotted into `NetworkConfigRevisionSpec` (`MirrorTargets` / `MirrorSelectors`) and contribute to the revision hash in `NewRevision`. Mirror changes therefore bump the revision and roll out through the normal gated, node-by-node pipeline, alongside L2/VRF/BGP changes.

The rationale for choosing Option A over Option B:

- **Single source of truth and auditability:** the revision is a full snapshot, so "what mirror config was active at revision X" is answerable directly from the revision CR.
- **Atomic, gated rollout:** mirror config rolls out node-by-node together with the rest of the config, reusing the existing rollout/gating machinery instead of a separate, ungated update path for `NodeNetworkConfig`.
- **Minimal moving parts:** no separate reconcile/trigger mechanism is needed for mirror CRD changes; the existing revision pipeline already reacts to them.

The mirror destination VRF itself (with VNI + RT + loopback definitions) is a standard `VRFRouteConfiguration`, so it is part of the revision already. The loopback spec (name + subnet) is snapshotted; only the resolved per-node IP, GRE interface, and ACL entries are injected at `NodeNetworkConfig` build time.

The main trade-off accepted is that the revision CR grows with the number of mirror selectors/targets, and that additive mirror changes still incur a full revision bump and rollout.

## 4. Implementation Plan

> **Scope:** The operator side (`controllers/operator/`, `pkg/reconciler/operator/`) resolves the CRDs into per-node config. Both agent rendering paths were extended in-repo: `pkg/cra-vsr` (6WIND vSR XML) and `pkg/nl` + `pkg/reconciler/agent-cra-frr` (Linux netlink/tc for CRA-FRR).

### Phase 1 — Operator-Side Wiring

1. **Watch MirrorSelector + MirrorTarget** in `config_controller.go`:
   ```go
   Watches(&networkv1alpha1.MirrorSelector{}, h).
   Watches(&networkv1alpha1.MirrorTarget{}, h).
   ```

2. **Add `fetchMirrorTargets`/`fetchMirrorSelectors`** in `config_reconciler.go` to list all `MirrorTarget` and `MirrorSelector` objects; their sorted snapshots are added to the `NetworkConfigRevision` (and hashed) and resolved at `NodeNetworkConfig` build time.

### Phase 2 — NodeNetworkConfig Build: Mirror ACL + GRE + Loopback + Export Filter

In `configrevision_reconciler.go` → `CreateNodeNetworkConfig`, after building VRF/L2/BGP:

For each `MirrorSelector`:

1. **Resolve references:**
   - Look up the referenced `MirrorTarget` → get collector IP, GRE key, type (l2gre/l3gre), destination VRF and source loopback.
   - Look up the referenced `MirrorSource` → get the `Layer2NetworkConfiguration` or `VRFRouteConfiguration` (including its `NodeSelector`).

2. **Check node eligibility:** Only add mirror config to nodes matched by the source's `NodeSelector`.

3. **Ensure the mirror destination VRF and loopback exist:**
   - The mirror destination VRF is identified by `MirrorTarget.Spec.DestinationVrf` (e.g. `"mirror"`).
   - It must be a **user-created `VRFRouteConfiguration`** (with VNI + RT) and already present in `FabricVRFs` on the node. If not → skip on this node (status reflects it).
   - The loopback named by `MirrorTarget.Spec.SourceLoopback` (e.g. `"lo.mir"`) must be defined in the VRF's `loopbacks` list with a `subnet`. If not → skip.

4. **Allocate the source loopback IP from the subnet:**
   - Look up the `VRFLoopback` named by `MirrorTarget.Spec.SourceLoopback` on the mirror VRF's `VRFRouteConfiguration`.
   - Compute the deterministic per-node IP from its `subnet` via `loopbackAllocator` (preserving any existing allocation; reserving out-of-scope nodes' addresses).

5. **Inject loopback into the mirror VRF:**
   ```go
   fabricVrf.Loopbacks["lo.mir"] = v1alpha1.Loopback{
       IPAddresses: []string{"<allocated-ip>/32"},
   }
   ```

6. **Inject GRE tunnel interface into the mirror VRF:**
   ```go
   greName := greInterfaceName(target.Name, target.Spec.Type) // "gre-<hash>" (l3gre) or "gtap-<hash>" (l2gre), <=15 chars
   fabricVrf.GREs[greName] = v1alpha1.GRE{
       SourceAddress:      "<allocated-ip>",       // the sourceLoopback's per-node IP
       SourceInterface:    target.Spec.SourceLoopback, // bind tunnel to the loopback (l3mdev/IP6GRE)
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
       MirrorDestination: greName,   // the GRE interface name (gre-<hash>/gtap-<hash>)
       Direction:         selector.Spec.Direction,
   }
   ```

### Phase 3 — Per-Node Source IP Allocation

No external IPAM is used. During `NodeNetworkConfig` build the operator's
`loopbackAllocator` computes a deterministic `node → IP` map per `<vrf>/<loopback>`
subnet:

1. Sort nodes by name; assign the lowest free host address, skipping the IPv4
   network and broadcast addresses.
2. Preserve existing per-node allocations; drop and re-allocate any address that
   no longer falls inside the (possibly changed) subnet.
3. Reserve addresses still held by out-of-scope (e.g. NotReady) nodes so they are
   never duplicated.
4. The map is computed once per `(revision, ready-node set)` and reused for the
   per-node builds of a rollout.

### Phase 4 — Status Reporting

- Set `MirrorSelector.Status` with conditions:
  - `Resolved` — target and source references are valid.
  - `Applied` — ACLs have been programmed on all matching nodes.
- Set `MirrorTarget.Status` with:
  - `ActiveSelectors` — count of selectors referencing this target.
  - `ActiveNodes` — count of nodes where the tunnel is configured.

## 5. CRD Examples

### Mirror Destination VRF (user-created, with loopback subnet)

The mirror VRF is a standard `VRFRouteConfiguration`. The `loopbacks` field declares the subnet the operator allocates per-node source IPs from (no IPAM provider required):

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
    subnet: 10.99.0.0/24   # per-node source IPs allocated deterministically from here
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

> **API note:** `MirrorTargetSpec` carries `destinationVrf` and `sourceLoopback` (and no IP pool reference); the source-IP subnet lives on the `VRFRouteConfiguration` loopback.

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
        "gre-1a2b3c4d":
          sourceAddress: "10.99.0.1"
          destinationAddress: "10.250.0.100"
          layer: layer3
          encapsulationKey: 1001
    "external":
      # ... normal external VRF config ...
      mirrorAcls:
      - trafficMatch:
          dstPrefix: "0.0.0.0/0"
        mirrorDestination: "gre-1a2b3c4d"
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
        mirrorDestination: "gre-1a2b3c4d"
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
  │  MirrorSource:     │    │  VRFRouteConfiguration "mirror"       │
  │  L2NetConfig or    │    │  (loopbacks:                          │
  │  VRFRouteConfig    │    │    - name: lo.mir                     │
  └────────────────────┘    │      subnet: 10.99.0.0/24)            │
                            └───────────────────────────────────────┘

  Operator reconcile loop (pkg/reconciler/operator):
  ┌──────────────────────────────────────────────────────────────────┐
  │                                                                  │
  │  1. Snapshot MirrorTargets + MirrorSelectors into the revision   │
  │  2. Resolve MirrorTarget → get collector IP, key, type, VRF,     │
  │     and sourceLoopback                                           │
  │  3. Resolve MirrorSource → get L2 VLAN or VRF name               │
  │  4. Match nodes via source's nodeSelector                        │
  │  5. Allocate per-node loopback IP deterministically from the     │
  │     loopback subnet (loopbackAllocator)                          │
  │  6. Into mirror VRF on NodeNetworkConfig:                        │
  │     a) Add Loopback with allocated IP                            │
  │     b) Add GRE tunnel interface (src=loopback, dst=collector)    │
  │     c) Auto-append source IP to EVPN export filter              │
  │  7. Build MirrorACL entries (mirrorDestination = GRE intf name)  │
  │  8. Attach MirrorACLs to source L2 / VRF                         │
  │                                                                  │
  └────────────────────┬─────────────────────────────────────────────┘
                       │
                       ▼
  ┌────────────────────────────────────────────────────────┐
  │      NodeNetworkConfig (per node)                       │
  │  .FabricVRFs["mirror"].Loopbacks["lo.mir"]   = src IP   │
  │  .FabricVRFs["mirror"].GREs["gre-1a2b3c4d"]  = tunnel   │
  │  .FabricVRFs["mirror"].EVPNExportFilter     += src IP   │
  │  .Layer2s["100"].MirrorACLs                 += ACL      │
  │  .FabricVRFs["ext"].MirrorACLs              += ACL      │
  └────────────────────┬───────────────────────────────────┘
                       │
                       ▼
  ┌──────────────────────────────────────────────────────────┐
  │  Agent (per node) — CRA-VSR (XML) or CRA-FRR (netlink)   │
  │  Both render, from the NodeNetworkConfig:                │
  │  1. VRF + VXLAN (from FabricVRFs)                        │
  │  2. Loopback in VRF (from Loopbacks)                     │
  │  3. GRE/GRETap in VRF (from GREs)                        │
  │  4. Mirror rules (from MirrorACLs), bound to the L2      │
  │     access port vlan.<id> or the VRF's vx.<vrf>:         │
  │     - from="vlan.100" direction (interface-relative)     │
  │            to="gre-1a2b3c4d"                             │
  │     - from="vx.ext"   direction (interface-relative)     │
  │            to="gre-1a2b3c4d"                             │
  │  5. Traffic match filters per rule                       │
  └──────────────────────────────────────────────────────────┘
```

## 7. Open Questions / Future Work

1. **Subnet sizing & exhaustion:** every node reserves an address from the loopback subnet. Should the operator surface a clear status/condition when the subnet is too small for the cluster (allocation fails for some nodes)?

2. **Rate limiting / sampling:** Should `MirrorSelector` support a `sampleRate` field (e.g. 1:1000) to reduce volume? This can be added later.

3. **Multiple targets per selector:** Currently each selector points to exactly one target. Multi-target (e.g. primary + backup collector) can be a future extension.

4. **EVPN export filter merging:** The per-node source IP is appended to the mirror VRF's EVPN export filter alongside whatever the user configured in the `VRFRouteConfiguration`. The operator merges these without overwriting user-defined filter items (idempotently).

5. **L2 east-west completeness:** mirroring attaches to the `vlan.<id>` access port, so same-node/same-VLAN pod-to-pod traffic that is switched inside the macvlan driver (macvlan bridge mode, same parent) is not seen. Is that in scope?

6. **vSR mirror-traffic direction semantics:** the L2 direction inversion assumes the 6WIND `<mirror-traffic>` `direction` is interface-relative (same rx/tx convention as Linux tc). Worth validating against actual vSR capture behavior.

## 8. Decision Record

| # | Decision | Rationale |
|---|---|---|
| D1 | Embed mirror snapshots (`MirrorTargets`/`MirrorSelectors`) **in** `NetworkConfigRevision` and include them in the revision hash | Mirror changes bump the revision and roll out through the normal gated, node-by-node pipeline; gives a full per-revision snapshot for auditability (implemented — "Option A") |
| D2 | Resolve mirror selectors/targets into per-node `MirrorACLs`/GRE/loopback at `NodeNetworkConfig` build time | Agents stay simple — they only read the fully-resolved per-node config |
| D3 | Mirror VRF is a **user-created `VRFRouteConfiguration`** (fabric VRF with VNI + RT) | Consistent with existing VRF lifecycle; enables EVPN reachability to collector |
| D4 | Per-node **source IP as loopback** in the mirror VRF | Each node uniquely identified; GRE source is routable via EVPN |
| D5 | Source IP added to **EVPN export filter** (auto-appended by operator) | Collector can reach the node's GRE endpoint via the fabric |
| D6 | Add GRE/GRETap, loopback and `<mirror-traffic>` rendering to `pkg/cra-vsr` (and the equivalent netlink rendering to the FRR path) **in-repo** as part of this change | Earlier the package was treated as vendor-maintained and untouched, but the mirroring data path required extending it; the rendering now lives in-repo alongside the rest of the config generation |
| D7 | Bind L2 mirrors to the `vlan.<id>` bridge access port | Both paths use the workload-facing access port (not the bridge master) as `<from>`/source so east-west (port-to-port) traffic is captured; the workload-perspective direction is inverted to the port's interface-relative direction |
| D8 | Bind VRF mirrors to `vx.<vrf>` VXLAN interface (if VNI exists) | CRA-VSR uses VXLAN name as `<from>` for VRF mirror-traffic rules |
| D9 | `MirrorACL.MirrorDestination` = GRE interface name | CRA-VSR maps this to the `<to>` field in `<mirror-traffic>` rules |
| D10 | **Loopback subnet lives on `VRFRouteConfiguration`** (`VRFLoopback.Subnet`), not `MirrorTarget` | Loopback is a VRF interface; its IP must be in the VRF's EVPN export filter; co-locating avoids cross-cutting concerns and flows through the revision pipeline |
| D11 | `MirrorTargetSpec` carries `destinationVrf` + `sourceLoopback` (no IP-pool reference) | MirrorTarget specifies tunnel properties + which VRF and loopback to bind the GRE source to; IP allocation is the VRF's responsibility |
| D13 | **`MirrorTarget.SourceLoopback` explicitly selects the loopback** within the VRF | Avoids ambiguity when a VRF has multiple loopbacks; without it the operator would need a non-deterministic heuristic to pick a source IP |
| D12 | Allocate the per-node source IP **deterministically from the loopback `Subnet`** (operator-side `loopbackAllocator`), preserving existing per-node allocations | No external IPAM dependency; stable, deterministic per-node addresses computed at build time and snapshotted into the revision |
