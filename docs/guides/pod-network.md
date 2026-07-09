---
title: PodNetwork
description: >-
  Redistribute a pod Network's CIDR into a VRF (BGP-EVPN) so an additional pod
  network is reachable across the fabric.
---

# PodNetwork

A `PodNetwork` makes an additional pod network **reachable across the fabric** by
redistributing the referenced
[`Network`](../getting-started/concepts.md#foundation-vrf-and-network)'s CIDR into
a VRF. It surfaces the Network's IPv4/IPv6 CIDRs in status and, via a
`destinations` selector, plumbs the network into the matched VRFs so pod traffic
on this network is routed there.

Use a `PodNetwork` when pods have a **secondary / additional network** (assigned
by your CNI) that must be advertised through a specific VRF backbone — for
example a tenant overlay reachable via the fabric. `PodNetwork` handles only the
L3 fabric side (route redistribution); your CNI/IPAM owns the pod addresses.

## How it works

A `PodNetwork` does two things on the operator side:

1. **Resolves the Network** referenced by `spec.networkRef` and surfaces its
   CIDRs in status as `networkIPv4` (`Network.spec.ipv4.cidr`) and `networkIPv6`
   (`Network.spec.ipv6.cidr`). These are empty when the Network has no pool for
   that family or cannot be resolved.
2. **Redistributes the network into VRFs.** When `spec.destinations` is set, the
   matched [`Destination`](../getting-started/concepts.md#routing-destination)
   CRDs resolve to a VRF (`Destination.spec.vrfRef`). The operator adds a
   **redistribute-connected** filter for the Network's CIDR into that FabricVRF
   (so the pod network's connected routes enter BGP-EVPN), plus any **aggregate
   routes** from a matching `AnnouncementPolicy`. The VRF names are reported in
   `status.vrfs` (sorted, de-duplicated). If `destinations` is omitted, no VRF
   plumbing is performed.

!!! note "PodNetwork does not allocate IPs or attach pods"
    Unlike [`Inbound`](inbound.md) / [`Outbound`](outbound.md), a `PodNetwork`
    allocates **no** addresses and creates **no** MetalLB pools, Calico
    `IPPool`s or Coil `Egress` — there is no platform controller behind it. It
    only contributes L3 routing (redistribute + aggregate) to the per-node
    `NodeNetworkConfig`. Pod **IP address management** and **interface
    attachment** are handled entirely by your **CNI** (for example the primary
    CNI, or Multus via a `NetworkAttachmentDefinition`) — those resources are
    external to this operator.

`PodNetwork` has no `nodeSelector`; the contribution applies to **all nodes**,
and pod placement is the scheduler's job.

## Prerequisites

Before creating a `PodNetwork` you need:

- A **`Network`** referenced by `spec.networkRef`, in the same namespace, with an
  `ipv4` and/or `ipv6` CIDR. The `PodNetwork` surfaces these CIDRs in its status.
- For **VRF routing**, one or more **`Destination`** CRDs carrying labels your
  `spec.destinations` selector matches, each bound to a **VRF** via `vrfRef`.
- A **CNI** capable of consuming an additional network (e.g. the primary CNI, or
  Multus). This is external to the operator.

## Minimal example

The example below assumes the `Network` `net-pods` and one or more
`Destination` CRDs labeled `type: gateway` (bound to a VRF) exist. See
[Concepts](../getting-started/concepts.md) for the model and the
[Quick Start](../getting-started/quick-start.md) for creating these foundation
resources by hand.

```yaml
# Foundation (created earlier / by a provisioner)
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Network
metadata:
  name: net-pods
spec:
  vlan: 601
  vni: 4000601
  ipv4:
    cidr: "10.244.0.0/16"
  ipv6:
    cidr: "fd00:10:244::/48"
---
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: PodNetwork
metadata:
  name: tenant-pods
spec:
  networkRef: "net-pods"
  destinations:
    matchLabels:
      type: gateway
```

Apply it:

```bash
kubectl apply -f pod-network.yaml
```

## Field reference

`PodNetworkSpec` is intentionally minimal — it has only two fields.

| Field | Type | Required | Description |
|---|---|:---:|---|
| `networkRef` | string | ✅ | Name of the `Network` whose CIDR this PodNetwork redistributes into the VRF. **Immutable** — to change it, delete and recreate the resource. |
| `destinations` | label selector | ❌ | Selects `Destination` CRDs that may use this pod network. Determines the VRFs it is plumbed into. If omitted, no VRF plumbing is performed. |

See the [CRD Reference](../reference/crd-reference.md#podnetwork) for the full,
auto-generated schema including status fields.

## Verify

List your PodNetworks (short name `pnet`):

```bash
kubectl get podnetwork
# or
kubectl get pnet
```

The print columns show `NetworkRef`, `IPv4`, `VRFs` and `Ready` (with `IPv6`
available at higher verbosity). Inspect the resolved status:

```bash
kubectl get podnetwork tenant-pods -o jsonpath='{.status.networkIPv4}'
kubectl get podnetwork tenant-pods -o jsonpath='{.status.networkIPv6}'
kubectl get podnetwork tenant-pods -o jsonpath='{.status.vrfs}'
kubectl get podnetwork tenant-pods -o jsonpath='{.status.conditions}' | jq
```

A healthy `PodNetwork` reports the Network's CIDRs in `status.networkIPv4` /
`status.networkIPv6`, the resolved VRF names in `status.vrfs`, and a `Ready`
condition with status `True`.

## Troubleshooting

!!! warning "Network missing or not resolvable"
    If `spec.networkRef` points to a Network that does not exist (or has no IP
    pool), the `PodNetwork` does not become `Ready` and `status.networkIPv4` /
    `status.networkIPv6` stay empty. Create the `Network` first and confirm it is
    itself `Ready`.

!!! warning "`networkRef` is immutable"
    `spec.networkRef` cannot be changed in place — the admission webhook rejects
    edits. To point a PodNetwork at a different `Network`, delete and recreate the
    resource.

!!! note "No `destinations` means no VRF plumbing"
    Omitting `spec.destinations` is valid: the `PodNetwork` still surfaces the
    Network's CIDRs, but `status.vrfs` stays empty and no VRF routing is
    programmed. Add a `destinations` selector matching a VRF-bound `Destination`
    if you need the network plumbed into a VRF.

## Related

- [Layer2Attachment](layer2-attachment.md) — attach a Network as an L2 segment.
- [Inbound](inbound.md) — allocate ingress LoadBalancer IPs.
- [CRD Reference](../reference/crd-reference.md#podnetwork) — full schema.
