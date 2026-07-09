---
title: Inbound
description: >-
  Allocate ingress LoadBalancer IPs from a Network for MetalLB, with optional
  /32 host-route export into VRFs (HBN mode).
---

# Inbound

An `Inbound` allocates IP addresses from a [`Network`](../getting-started/concepts.md#foundation-vrf-and-network)
and publishes them as a MetalLB `IPAddressPool` plus a BGP or L2 advertisement.
These are the ingress **LoadBalancer** IPs your Services claim.

Use an `Inbound` whenever a workload needs a stable, externally reachable IP —
for example a tenant gateway, an API endpoint, or a shared machine-to-machine
pool. You declare intent (which `Network`, how many IPs, how to advertise them)
and the operator programs MetalLB and, in HBN mode, the VRF host routes.

## How it works

Every `Inbound` produces two things:

1. A MetalLB `IPAddressPool` containing the allocated addresses.
2. A MetalLB advertisement — either a `BGPAdvertisement` or an
   `L2Advertisement`, selected by `spec.advertisement.type`.

Whether the IPs are also injected into the routing overlay depends **solely** on
whether `spec.destinations` is set:

- **HBN mode** — `destinations` **is set**. The matched
  [`Destination`](../getting-started/concepts.md#routing-destination) CRDs
  resolve to one or more VRFs, and each allocated IP is exported as a `/32`
  host route into those VRFs **in addition to** the MetalLB pool and
  advertisement.
- **non-HBN mode** — `destinations` **is omitted**. Only the MetalLB pool and
  advertisement are created; there is no VRF plumbing.

!!! note "Mode is decided by field presence"
    There is no mode switch. See
    [Deployment modes: HBN vs. non-HBN](../getting-started/concepts.md#deployment-modes-hbn-vs-non-hbn-pure-l2-netplan)
    for the full comparison.

`Inbound` has no `nodeSelector`: its node scope is inherited from the matched
`Destination` / VRF.

## Prerequisites

Before creating an `Inbound` you need:

- A **`Network`** referenced by `spec.networkRef`, in the same namespace. The
  `Inbound` allocates its addresses from this Network's `ipv4` / `ipv6` range.
- For **HBN mode**, one or more **`Destination`** CRDs carrying labels your
  `spec.destinations` selector matches, each bound to a **VRF** via `vrfRef`.

The examples below assume a `Network` named `net-vlan501` and a `Destination`
labeled `type: gateway` bound to a VRF already exist. See
[Concepts](../getting-started/concepts.md) for the model and the
[Quick Start](../getting-started/quick-start.md) for creating these foundation
resources by hand.

## Minimal example

A BGP-advertised ingress pool exported into the `type: gateway` VRF. The
operator allocates `count` addresses from the `Network` — the recommended model:

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Inbound
metadata:
  name: ib-gateway
  namespace: default
spec:
  networkRef: "net-vlan501"
  destinations:
    matchLabels:
      type: gateway
  count: 2
  advertisement:
    type: bgp
```

Apply it:

```bash
kubectl apply -f ib-gateway.yaml
```

!!! tip "Prefer `count`"
    Allocating from the `Network` with `count` is the predominant model — you let
    the operator pick free addresses from `networkRef`. Only use explicit
    `addresses` when you must pin specific IPs (see
    [Pinning specific addresses](#pinning-specific-addresses)).

## Field reference

| Field | Required | Description |
|---|:---:|---|
| `networkRef` | ✅ | Name of the `Network` to allocate from. **Immutable.** |
| `advertisement.type` | ✅ | MetalLB advertisement mode. Enum: `bgp` or `l2`. |
| `destinations` | — | Label selector over `Destination` CRDs. Its **presence** enables HBN mode (VRF host-route export); omit it for non-HBN (pool only). |
| `count` | — | **Recommended.** Number of IPs to allocate from the `Network` (`≥ 1`). Mutually exclusive with `addresses`. |
| `addresses` | — | Explicit `ipv4` / `ipv6` address lists. Use only when you must pin specific IPs. Mutually exclusive with `count`. |
| `poolName` | — | Override the generated MetalLB `IPAddressPool` name. |
| `tenantLoadBalancerClass` | — | `LoadBalancerClass` for tenant-managed load balancing. |

!!! warning "Exactly one of `count` or `addresses`"
    The admission webhook requires **exactly one** of `count` or `addresses` to
    be set. Setting both, or neither, is rejected. Prefer `count`.

See the full [CRD Reference](../reference/crd-reference.md#inbound) for every
field.

## Common tasks

### Allocate with `count` (recommended)

Use `count` to let the operator pick N free addresses from the Network — the
predominant model, where the exact IPs do not matter:

```yaml
spec:
  networkRef: "net-vlan501"
  count: 4
  advertisement:
    type: bgp
```

### Pinning specific addresses

Only when you need specific, well-known IPs, use `addresses` (mutually exclusive
with `count`):

```yaml
spec:
  networkRef: "net-vlan501"
  destinations:
    matchLabels:
      type: gateway
  addresses:
    ipv4:
      - "10.250.4.10/32"
  advertisement:
    type: bgp
```

### L2 vs. BGP advertisement

Set `advertisement.type: l2` to have MetalLB announce the pool via L2 (ARP/NDP)
instead of BGP. This example pins an explicit L2-advertised address:

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Inbound
metadata:
  name: ib-lb
  namespace: default
spec:
  networkRef: "net-vlan501"
  destinations:
    matchLabels:
      type: gateway
  addresses:
    ipv4:
      - "10.250.40.20/32"
  advertisement:
    type: l2
```

!!! tip "L2 vs. BGP"
    Use `l2` for simple single-subnet setups where clients live on the same L2
    segment. Use `bgp` when the IPs must be advertised to an upstream router /
    fabric — the typical choice in HBN mode.

### Naming the pool with `poolName`

By default the MetalLB `IPAddressPool` name is generated. Set `poolName` to give
it a stable, predictable name — useful when other resources (for example a
`BGPPeering` in loopbackPeer mode, via `inboundRefs`) reference the pool, or when
carving out a larger shared block:

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Inbound
metadata:
  name: ib-m2m-base
spec:
  networkRef: "net-vlan501"
  poolName: "m2m-pool"
  destinations:
    matchLabels:
      type: gateway
  addresses:
    ipv4:
      - "10.250.4.0/24"
    ipv6:
      - "fdbb:6b17:90ba::/64"
  advertisement:
    type: bgp
```

### Dual-stack allocation

Populate both `ipv4` and `ipv6` under `addresses` (as in the `ib-m2m-base`
example above) to allocate a dual-stack pool. The referenced `Network` must
provide the corresponding `ipv4` / `ipv6` ranges.

## Verify

List your `Inbound` resources — the printer columns show the referenced Network,
pool, advertisement, VRFs and readiness:

```bash
kubectl get inbound
kubectl get inbound ib-gateway -o wide
```

Inspect what was allocated and resolved via `status`:

```bash
# Allocated addresses
kubectl get inbound ib-gateway -o jsonpath='{.status.addresses}' | jq

# Resolved MetalLB pool name
kubectl get inbound ib-gateway -o jsonpath='{.status.poolName}'

# VRFs the IPs were exported into (HBN mode; empty in non-HBN)
kubectl get inbound ib-gateway -o jsonpath='{.status.vrfs}' | jq
```

Check the conditions — `Resolved`, `Applied` and `Ready`:

```bash
kubectl get inbound ib-gateway -o jsonpath='{.status.conditions}' | jq
```

A healthy `Inbound` reports `Resolved=True` (all references found),
`Applied=True` (MetalLB objects and any VRF exports programmed) and
`Ready=True`. See
[Status and conditions](../getting-started/concepts.md#status-and-conditions).

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Stuck with `Ready=False`, `Resolved=False` | The referenced `Network` does not exist, or (HBN) no `Destination` matches `spec.destinations`. | Create the `Network` / label a matching `Destination` bound to a VRF. |
| `status.vrfs` is empty in HBN mode | `spec.destinations` matched no `Destination`, or the matched Destination has no `vrfRef`. | Check the selector labels and the Destination's `vrfRef`. |
| Rejected on apply: *exactly one of count or addresses* | Both `count` and `addresses` set, or neither. | Set exactly one. |
| Change to `networkRef` rejected | `networkRef` is **immutable**. | Delete and recreate the `Inbound` with the new `networkRef`. |
| `Inbound` stuck `Terminating` | Another resource (for example a `BGPPeering` via `inboundRefs`) still references it. | Delete the referencing resource first; see the [deletion order](../getting-started/concepts.md#lifecycle-and-deletion-order). |

!!! warning "`networkRef` is immutable"
    The allocations and host routes derived from the Network cannot be rebound
    in place. To move an `Inbound` to a different Network, delete and recreate
    it.

## Related

- [Outbound](outbound.md) — egress / SNAT IP allocation.
- [BGPPeering](bgp-peering.md) — advertise `Inbound` pools from tenant workloads
  (loopbackPeer mode via `inboundRefs`).
- [Layer2Attachment](layer2-attachment.md) — attach a Network to nodes as an L2
  segment (HBN VXLAN or netplan VLAN).
- [CRD Reference](../reference/crd-reference.md#inbound) — every field and
  constraint.
