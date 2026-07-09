---
title: Layer2Attachment
description: >-
  Attach a Network to a set of nodes as a Layer 2 segment — HBN mode (VXLAN +
  VRF overlay) or non-HBN mode (a plain VLAN sub-interface on a NIC/bond).
---

# Layer2Attachment

A `Layer2Attachment` (short name `l2a`) attaches a
[`Network`](../getting-started/concepts.md#foundation-vrf-and-network) to a set
of nodes as a Layer 2 segment. It is the resource that lands an L2 segment on
specific hosts — either as an automatically created VXLAN overlay plumbed into
VRFs (HBN mode) or as a plain VLAN sub-interface on an existing NIC or bond
(non-HBN mode).

You declare which `Network` to attach, which nodes receive it, and — for the
routed overlay — which `Destination`s bind it into VRFs. The operator derives
the per-node interface configuration and rolls it out via `NodeNetworkConfig`
and `NodeNetplanConfig`.

## How it works

There is no mode switch. Which data-plane mode you get is decided by **field
presence** — specifically whether `spec.interfaceRef` is set:

| | **HBN mode** | **non-HBN mode** (pure L2) |
|---|---|---|
| `interfaceRef` | **omitted** | **set** (name of a NIC/bond) |
| Interface created | VXLAN interface (automatic) | VLAN sub-interface on that NIC/bond |
| `Network` `vni` | **required** | must **not** be set |
| `destinations` | set → plumbed into VRFs | typically omitted → pure L2 bridge |
| Data plane | VXLAN tunnel + VRF overlay (BGP-EVPN) | plain VLAN sub-interface |
| Applied by | CRA agents (`NodeNetworkConfig`) | netplan / hbn-l2 agent (`NodeNetplanConfig`) |

The two levers are:

- **`interfaceRef`** — the HBN/non-HBN toggle. Nil means "create a VXLAN"; a
  value means "create a VLAN sub-interface on that named interface".
- **`vni` on the referenced `Network`** — required for HBN (the VXLAN needs a
  VNI), must be absent for non-HBN (pure L2 has no overlay).

`destinations` is an independent, orthogonal choice: it plumbs the segment into
VRFs. It is usual in HBN mode and usually omitted in non-HBN mode, but the mode
itself is decided only by `interfaceRef`.

!!! note "Mode is decided by field presence"
    See
    [Deployment modes: HBN vs. non-HBN (pure L2 / netplan)](../getting-started/concepts.md#deployment-modes-hbn-vs-non-hbn-pure-l2-netplan)
    for the full model, including how the L3 overlay (`NodeNetworkConfig` → CRA
    agents) and host L2 plumbing (`NodeNetplanConfig` → netplan/hbn-l2 agent)
    are split in *both* modes.

## Prerequisites

Before creating a `Layer2Attachment` you need:

- A **`Network`** referenced by `spec.networkRef`, in the same namespace.
    - For **HBN mode**, the `Network` must carry a `vni` (and typically a
      `vlan`).
    - For **non-HBN mode**, the `Network` must **not** carry a `vni` — it is a
      pure L2 segment.
- For **HBN mode** with routing: one or more **`Destination`** CRDs carrying
  labels your `spec.destinations` selector matches, each bound to a **VRF** via
  `vrfRef`.
- **Nodes labeled** so your `spec.nodeSelector` can select the hosts that should
  receive the attachment.
- For **non-HBN mode**: the interface named in `spec.interfaceRef` (for example
  `bond0`) must already exist on every selected node.

## HBN mode (VXLAN + VRF)

Omit `interfaceRef`. A VXLAN interface is created automatically, and
`destinations` plumb the segment into one or more VRFs. The referenced `Network`
must have a `vni`.

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Layer2Attachment
metadata:
  name: l2a-vlan501
  namespace: default
spec:
  networkRef: "net-vlan501"
  destinations:
    matchLabels:
      type: gateway
```

This references a `Network` `net-vlan501` (which has `vlan: 501` and
`vni: 4000002`) and selects every `Destination` labeled `type: gateway`. Those
Destinations resolve to a VRF via `vrfRef`, so the segment is imported into that
VRF as a VXLAN + VRF overlay. The resulting VRF names appear in
`status.vrfs`.

### Multiple attachments in the same VRF

Attaching several VLANs to the **same** VRF is just several `Layer2Attachment`s
that select the same `Destination` label. Here VLAN 501 and VLAN 502 both land
in the `type: gateway` VRF:

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Layer2Attachment
metadata:
  name: l2a-l3-vlan501
spec:
  networkRef: "net-vlan501"
  destinations:
    matchLabels:
      type: gateway
---
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Layer2Attachment
metadata:
  name: l2a-l3-vlan502
spec:
  networkRef: "net-vlan502"
  destinations:
    matchLabels:
      type: gateway
```

!!! tip "Anycast gateway"
    In HBN mode the operator provisions an anycast gateway for the segment; its
    MAC and gateway addresses are surfaced in `status.anycast`. Disable it with
    `spec.disableAnycast: true` when the gateway lives elsewhere.

## non-HBN mode (pure L2 / VLAN sub-interface)

Set `interfaceRef` to the name of an existing NIC or bond. The agent creates a
**VLAN sub-interface** on it — a plain L2 segment with no overlay. Omit
`destinations` for a pure L2 bridge with no VRF plumbing. The referenced
`Network` must **not** have a `vni`.

```yaml
# Network for a pure-L2 segment: vlan only, NO vni
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Network
metadata:
  name: net-vlan700
spec:
  vlan: 700
---
# non-HBN: VLAN sub-interface on an existing bond, no VRF plumbing
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Layer2Attachment
metadata:
  name: l2a-vlan700
spec:
  networkRef: "net-vlan700"
  interfaceRef: "bond0"
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
```

Because `interfaceRef` is set, the agent creates `bond0.700` — a VLAN
sub-interface on `bond0` — on every node matched by the `nodeSelector`. Because
`destinations` is omitted, there is no VRF plumbing: a pure L2 bridge only. The
`Network` carries only a `vlan` and no `vni`, as required in this mode.

!!! warning "The interface must exist on the node"
    `bond0` (or whichever interface you name) must already exist on each
    selected node. If it does not, the attachment reports the
    `InterfaceNotFound` condition. On VMs the hypervisor provides the NIC; on
    bare metal it is a physical NIC or bond.

## Field reference

Key `spec` fields — see the
[CRD Reference](../reference/crd-reference.md#layer2attachment) for the complete,
authoritative list.

| Field | Type | Notes |
|---|---|---|
| `networkRef` | string | **Required, immutable.** Name of the `Network` to attach. |
| `destinations` | label selector | Selects `Destination`s by label. Omitted → no VRF plumbing. |
| `nodeSelector` | label selector | Which nodes receive the attachment. |
| `interfaceRef` | string | Nil → HBN VXLAN. Set → VLAN sub-interface on that NIC/bond (non-HBN). The mode toggle. |
| `interfaceName` | string | **Immutable.** Interface name suffix. Defaults to `vlan.<vlan>`. |
| `mtu` | int32 | Interface MTU, `1000`–`9000`. |
| `disableAnycast` | bool | Disable the anycast gateway. |
| `disableNeighborSuppression` | bool | Disable neighbor suppression. |
| `disableSegmentation` | bool | Disable TX/RX segmentation offload on the interface. |
| `sriov.enabled` | bool | **Immutable.** SR-IOV VF passthrough; skips VXLAN/VLAN bridge setup. |
| `nodeIPs.enabled` | bool | Assign per-node IPs from the referenced `Network`. |
| `nodeIPs.reservedRanges` | []CIDR | Ranges within the Network reserved for pods, never allocated to nodes. |

!!! warning "Immutable fields"
    `networkRef`, `interfaceName` and `sriov.enabled` are immutable — the
    tunnels and allocations derived from them cannot be rebound in place. To
    change them, delete and recreate the resource.

## Common tasks

### Set the MTU

```yaml
spec:
  networkRef: "net-vlan501"
  mtu: 9000
```

`mtu` accepts values from `1000` to `9000`.

### Target specific nodes

Use `nodeSelector` to land the segment only on the intended hosts:

```yaml
spec:
  networkRef: "net-vlan501"
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
```

An L2 segment must land on specific nodes; without a `nodeSelector` the
attachment's node scope follows the operator's defaults.

### Assign node IPs

Allocate per-node addresses from the referenced `Network`, reserving part of the
range for pod use:

```yaml
spec:
  networkRef: "net-vlan501"
  destinations:
    matchLabels:
      type: gateway
  nodeIPs:
    enabled: true
    reservedRanges:
      - "10.250.40.0/26"
```

Each `reservedRanges` entry must be a valid CIDR that falls within the parent
`Network`'s address space; those addresses are never allocated to nodes. The
resulting per-node addresses appear in `status.nodeAddresses`.

### SR-IOV passthrough

Enable SR-IOV to perform VF passthrough instead of creating a VXLAN/VLAN bridge:

```yaml
spec:
  networkRef: "net-vlan501"
  sriov:
    enabled: true
```

!!! warning "SR-IOV skips bridge setup and is immutable"
    When `sriov.enabled` is `true` the agent skips VXLAN/VLAN bridge setup
    entirely and configures VF passthrough. `sriov.enabled` is immutable — to
    turn it on or off, delete and recreate the attachment.

### Disable anycast, neighbor suppression, or segmentation

Three independent booleans tune the interface behaviour:

```yaml
spec:
  networkRef: "net-vlan501"
  disableAnycast: true
  disableNeighborSuppression: true
  disableSegmentation: true
```

- `disableAnycast` — do not provision the anycast gateway (use when the gateway
  lives elsewhere).
- `disableNeighborSuppression` — turn off ARP/ND suppression on the segment.
- `disableSegmentation` — turn off TX/RX segmentation offload on the interface.

## Verify

List your attachments — the short name is `l2a`, and the printer columns show
the referenced Network, interface name, VRFs, MTU, SR-IOV and readiness:

```bash
kubectl get l2a
kubectl get layer2attachment l2a-vlan501 -o wide
```

Inspect what was derived via `status`:

```bash
# Interface the agent creates (spec.interfaceName override or "vlan.<vlan>")
kubectl get l2a l2a-vlan501 -o jsonpath='{.status.interfaceName}'

# VRFs the segment was plumbed into (HBN mode; empty in non-HBN)
kubectl get l2a l2a-vlan501 -o jsonpath='{.status.vrfs}' | jq

# Anycast gateway MAC and addresses (HBN mode)
kubectl get l2a l2a-vlan501 -o jsonpath='{.status.anycast}' | jq
```

Check the conditions:

```bash
kubectl get l2a l2a-vlan501 -o jsonpath='{.status.conditions}' | jq
```

A healthy attachment reports `Ready=True`. The `InterfaceNotFound` condition
signals that a referenced interface is missing on a node. See
[Status and conditions](../getting-started/concepts.md#status-and-conditions).

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `InterfaceNotFound` condition | `spec.interfaceRef` names a NIC/bond that does not exist on a selected node. | Correct `interfaceRef`, or ensure the interface exists on every matched node. |
| Attachment lands on the wrong hosts / no hosts | `spec.nodeSelector` matches the wrong nodes or none. | Fix the selector labels; label the intended nodes. |
| HBN: `status.vrfs` empty | `spec.destinations` matched no `Destination`, or the matched Destination has no `vrfRef`. | Check the selector labels and the Destination's `vrfRef`. |
| VXLAN not created in HBN mode | The referenced `Network` has no `vni`. | Add a `vni` to the `Network` (HBN requires it). |
| non-HBN attachment rejected / misbehaving | The referenced `Network` carries a `vni`, but pure L2 must not. | Use a `Network` without a `vni` for non-HBN mode. |
| Change to `networkRef`, `interfaceName` or `sriov.enabled` rejected | These fields are **immutable**. | Delete and recreate the attachment. |
| Attachment stuck `Terminating` | Another resource (for example a `BGPPeering` via `attachmentRef`) still references it. | Delete the referencing resource first; see the [deletion order](../getting-started/concepts.md#lifecycle-and-deletion-order). |

## Related

- [Inbound](inbound.md) — allocate ingress LoadBalancer IPs from a Network.
- [BGPPeering](bgp-peering.md) — listenRange mode opens a BGP listen-range on an
  L2 segment via `attachmentRef`.
- [Traffic Mirroring](traffic-mirroring.md) — a `Layer2Attachment` can be a
  mirror source.
- [CRD Reference](../reference/crd-reference.md#layer2attachment) — every field
  and constraint.
