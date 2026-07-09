---
title: Legacy API
description: >-
  The low-level network.t-caas.telekom.com CRDs for advanced users who author
  VRF, Layer 2 and mirroring config directly instead of via intent.
---

# Legacy API (network.t-caas.telekom.com)

The `network.t-caas.telekom.com/v1alpha1` API group is the original, low-level
interface to the network operator. Its resources map almost directly onto FRR,
VXLAN and host-interface concepts, so they are powerful but verbose and easy to
misconfigure.

!!! warning "Use the intent API for new deployments"
    New deployments should use the intent API in the
    `network-connector.sylvaproject.org` group — see
    [Concepts](../getting-started/concepts.md) and the
    [Inbound guide](../guides/inbound.md). Intent resources are the recommended,
    user-facing API. The low-level resources on this page remain **supported** as
    a direct-use escape hatch for existing and advanced setups, but you have to
    manage VNIs, route targets and node selection yourself.

All resources below are **cluster-scoped**.

## VRFRouteConfiguration

Defines a VRF and the routes leaked between it and the cluster VRF. Multiple
`VRFRouteConfiguration` objects may target the **same** `vrf`; they are merged by
`seq` at revision-build time.

| Field | Type | Notes |
|-------|------|-------|
| `vrf` | string | VRF name. Max length 12. |
| `routeTarget` | string | Route target for the VRF. |
| `vni` | int | VXLAN VNI for the VRF. |
| `import` | list | Prefix items leaked from this VRF into the cluster VRF (required). |
| `export` | list | Prefix items leaked from the cluster VRF into this VRF (required). |
| `aggregate` | list of string | Aggregate routes to announce. |
| `sbrPrefixes` | list of string | Traffic from these prefixes is sent straight to this VRF (source-based routing). |
| `seq` | int | **Required.** Sequence of the generated route-map (1–65534). |
| `communities` | list of string | Communities set on export. Replaces the deprecated `community`. |
| `nodeSelector` | label selector | Nodes to create the VRF on. |
| `loopbacks` | list | Per-node loopbacks; each has `name` and a `subnet` from which a per-node IP is allocated. |

Each entry in `import` / `export` is a prefix item with `cidr`, `action`
(`permit` or `deny`), an optional `seq`, and optional `ge` / `le` prefix-length
bounds.

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: VRFRouteConfiguration
metadata:
  name: example-vrf
spec:
  vrf: example
  vni: 100100
  routeTarget: "64512:100100"
  seq: 10
  import:
    - cidr: 10.0.0.0/8
      action: permit
  export:
    - cidr: 192.168.10.0/24
      action: permit
      le: 32
  communities:
    - "64512:100"
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
```

!!! note
    `seq` is required. It orders the generated route-map; the maximum is 65534
    because an explicit default-deny is sometimes appended.

## Layer2NetworkConfiguration

Defines a Layer 2 (VLAN/VXLAN) segment and, optionally, its integrated routing
and bridging into a VRF.

| Field | Type | Notes |
|-------|------|-------|
| `id` | int | **Required.** VLAN ID of the Layer 2 network. |
| `mtu` | int | **Required.** Interface MTU (1000–9000). |
| `vni` | int | **Required.** VXLAN VNI (1–16777215). |
| `anycastMac` | string | Anycast gateway MAC address (if anycast is desired). |
| `anycastGateways` | list of string | Anycast gateways to configure on the bridge. |
| `advertiseNeighbors` | bool | Advertise host routes for local neighbors. |
| `createMacVLANInterface` | bool | Create a MACVLAN attach interface. |
| `neighSuppression` | bool | Enable ARP / ND suppression. |
| `vrf` | string | VRF to attach the Layer 2 network to (default VRF if unset). |
| `nodeSelector` | label selector | Nodes to create the Layer 2 network on. |

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: Layer2NetworkConfiguration
metadata:
  name: example-l2
spec:
  id: 100
  mtu: 9000
  vni: 100200
  anycastMac: "02:00:00:00:00:01"
  anycastGateways:
    - 192.168.100.1/24
  neighSuppression: true
  vrf: example
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
```

## Legacy traffic mirroring (MirrorTarget + MirrorSelector)

Legacy mirroring is expressed as a pair: a **`MirrorTarget`** describes where
mirrored traffic goes (a remote GRE-encapsulated collector), and a
**`MirrorSelector`** describes what traffic to capture and from which source.

!!! note "Superseded by intent-based mirroring"
    The intent-based [Traffic Mirroring](../guides/traffic-mirroring.md)
    (`Collector` + `TrafficMirror` in the `network-connector.sylvaproject.org`
    group) supersedes `MirrorTarget` / `MirrorSelector`. Prefer it for new
    configurations.

**MirrorTarget** (short name `mirrortarget`):

| Field | Type | Notes |
|-------|------|-------|
| `type` | string | **Required.** GRE encapsulation type: `l2gre` or `l3gre`. |
| `destinationIP` | string | **Required.** Remote collector IP the tunnel points to. |
| `key` | int | Optional GRE encapsulation key. |
| `destinationVrf` | string | **Required.** VRF the GRE tunnel lives in (a user-created `VRFRouteConfiguration` with VNI + route target). Max length 12. |
| `sourceLoopback` | string | **Required.** Name of the loopback (defined on the destination VRF) whose per-node IP is the tunnel source. |

**MirrorSelector** (short name `mirrorselector`):

| Field | Type | Notes |
|-------|------|-------|
| `trafficMatch` | object | Which packets to mirror. Empty matches all traffic on the source. |
| `mirrorTarget` | object reference | **Required.** References the `MirrorTarget` (collector). |
| `mirrorSource` | object reference | **Required.** References the `Layer2NetworkConfiguration` or `VRFRouteConfiguration` whose traffic is captured. |
| `direction` | string | **Required.** Direction to mirror: `ingress` or `egress`. |

```yaml
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: MirrorTarget
metadata:
  name: example-collector
spec:
  type: l3gre
  destinationIP: 203.0.113.10
  destinationVrf: example
  sourceLoopback: lo.mir
---
apiVersion: network.t-caas.telekom.com/v1alpha1
kind: MirrorSelector
metadata:
  name: example-mirror
spec:
  direction: ingress
  mirrorTarget:
    kind: MirrorTarget
    name: example-collector
  mirrorSource:
    kind: Layer2NetworkConfiguration
    name: example-l2
```

The `sourceLoopback` must reference a loopback defined under `loopbacks` on the
destination VRF's `VRFRouteConfiguration`; the operator allocates a per-node IP
from that loopback's subnet for the GRE tunnel source.

## Operator-internal resources

The following resources also live in `network.t-caas.telekom.com/v1alpha1` but
are **generated by the operator** — do not author them directly:

- **`BGPPeering`** (this group's variant) — legacy BGP peering config resolved
  into the revision.
- **`NodeNetworkConfig`** (`nnc`) — resolved per-node FRR/VXLAN/VRF/BGP config.
- **`NodeNetplanConfig`** — desired per-node host interface state.
- **`NetworkConfigRevision`** (`ncr`) — cluster-wide config snapshot and rollout
  status.

These are read-only from a user's perspective. For how to inspect them when
debugging, see [Debugging](debugging.md).

## Related

- [Debugging](debugging.md) — inspecting the generated per-node resources.
- [Traffic Mirroring](../guides/traffic-mirroring.md) — the intent-based
  replacement for `MirrorTarget` / `MirrorSelector`.
- [Concepts](../getting-started/concepts.md) and the
  [Inbound guide](../guides/inbound.md) — the recommended intent API.
- [CRD Reference](../reference/crd-reference.md) — full field reference.
