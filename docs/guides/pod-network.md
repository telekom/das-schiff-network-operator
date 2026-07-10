---
title: PodNetwork
description: >-
  Provision a Calico IPPool (natOutgoing=false) for a pod Network and redistribute
  its CIDR into a VRF (BGP-EVPN) so pods can use it and it is reachable across the
  fabric.
---

# PodNetwork

A `PodNetwork` makes an additional pod network **usable by pods and reachable
across the fabric**. It provisions a
[Calico](https://docs.tigera.io/calico/latest/reference/) `IPPool` (with
`natOutgoing: false`) for the referenced
[`Network`](../getting-started/concepts.md#foundation-vrf-and-network)'s CIDR so
pods can be allocated addresses from it, and — via a `destinations` selector —
redistributes that CIDR into the matched VRFs so the pod traffic is routed across
the BGP-EVPN backbone.

Use a `PodNetwork` when pods need a **secondary / additional network** that is
routable through a specific VRF backbone — for example a tenant overlay reachable
via the fabric. The operator provisions the Calico `IPPool` and the L3 fabric
routing; you opt pods into the pool via a Calico annotation (see
[Using the pool on pods](#using-the-pool-on-pods)).

## How it works

A `PodNetwork` does three things on the operator side:

1. **Resolves the Network** referenced by `spec.networkRef` and surfaces its
   CIDRs in status as `networkIPv4` (`Network.spec.ipv4.cidr`) and `networkIPv6`
   (`Network.spec.ipv6.cidr`). These are empty when the Network has no pool for
   that family or cannot be resolved.
2. **Provisions a Calico `IPPool` per address family** covering the Network's
   full CIDR, with:
      - `natOutgoing: false` — the **fabric** performs SNAT (via the VRF), so pod
        addresses stay routable instead of being masqueraded behind the node IP;
      - `nodeSelector: "!all()"` — the pool is **not** auto-assigned to ordinary
        pods, so it never steals addresses from your default pod network; pods
        opt in explicitly (see below);
      - `allowedUses: [Workload]` — usable for pod (workload) addressing.

    The generated pool names are reported in `status.ipPools` (sorted). This is
    handled by the **`platform-coil`** controller.
3. **Redistributes the network into VRFs.** When `spec.destinations` is set, the
   matched [`Destination`](../getting-started/concepts.md#routing-destination)
   CRDs resolve to a VRF (`Destination.spec.vrfRef`). The operator adds a
   **redistribute-connected** filter for the Network's CIDR into that FabricVRF
   (so the pod network's connected routes enter BGP-EVPN), plus any **aggregate
   routes** from a matching `AnnouncementPolicy`. The VRF names are reported in
   `status.vrfs` (sorted, de-duplicated). If `destinations` is omitted, no VRF
   plumbing is performed.

!!! note "PodNetwork provisions the IPPool, not IPAM or attachment"
    A `PodNetwork` creates the Calico `IPPool` and the L3 routing, but it does
    **not** run IPAM or attach interfaces itself: **address allocation** from the
    pool and **interface attachment** are still performed by **Calico** (the CNI)
    at pod-creation time, driven by the pod annotation below. It allocates no
    MetalLB pools and no Coil `Egress`.

`PodNetwork` has no `nodeSelector`; the routing contribution applies to **all
nodes**, and pod placement is the scheduler's job.

## Using the pool on pods

Because the generated `IPPool` uses `nodeSelector: "!all()"`, Calico will **not**
hand out its addresses to pods automatically — you opt a pod (or its namespace /
workload) in with the
[`cni.projectcalico.org/ipv4pools`](https://docs.tigera.io/calico/latest/networking/ipam/use-specific-ip)
/ `ipv6pools` annotation, referencing the pool names from `status.ipPools`.

First read the provisioned pool names:

```bash
kubectl get podnetwork tenant-pods -o jsonpath='{.status.ipPools}'
# e.g. ["pn-tenant-pods-v4-1a2b3c4d","pn-tenant-pods-v6-9f8e7d6c"]
```

Then reference them on the pod template (values are JSON arrays of pool names):

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: app
  annotations:
    cni.projectcalico.org/ipv4pools: '["pn-tenant-pods-v4-1a2b3c4d"]'
    cni.projectcalico.org/ipv6pools: '["pn-tenant-pods-v6-9f8e7d6c"]'
spec:
  containers:
    - name: app
      image: nginx
```

The pod then receives its address(es) from the PodNetwork's pool and — provided a
`destinations` selector plumbs the network into a VRF — is reachable across the
fabric with the fabric handling SNAT (`natOutgoing: false`).

## Prerequisites

Before creating a `PodNetwork` you need:

- A **`Network`** referenced by `spec.networkRef`, in the same namespace, with an
  `ipv4` and/or `ipv6` CIDR. The `PodNetwork` surfaces these CIDRs in its status
  and provisions a Calico `IPPool` for each.
- For **VRF routing**, one or more **`Destination`** CRDs carrying labels your
  `spec.destinations` selector matches, each bound to a **VRF** via `vrfRef`.
- **Calico** as the (or a) CNI, and the **`platform-coil`** controller running in
  the cluster (it provisions the `IPPool`s). Pods opt into the pool via the
  `cni.projectcalico.org/ipv4pools` / `ipv6pools` annotation.

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

The print columns show `NetworkRef`, `IPv4`, `VRFs` and `Ready` (with `IPv6` and
`IPPools` available at higher verbosity). Inspect the resolved status:

```bash
kubectl get podnetwork tenant-pods -o jsonpath='{.status.networkIPv4}'
kubectl get podnetwork tenant-pods -o jsonpath='{.status.networkIPv6}'
kubectl get podnetwork tenant-pods -o jsonpath='{.status.ipPools}'
kubectl get podnetwork tenant-pods -o jsonpath='{.status.vrfs}'
kubectl get podnetwork tenant-pods -o jsonpath='{.status.conditions}' | jq
```

A healthy `PodNetwork` reports the Network's CIDRs in `status.networkIPv4` /
`status.networkIPv6`, the provisioned Calico `IPPool` names in `status.ipPools`,
the resolved VRF names in `status.vrfs`, and a `Ready` condition with status
`True`. You can confirm the pools directly:

```bash
kubectl get ippools.crd.projectcalico.org \
  -l network-connector.sylvaproject.org/podnetwork=tenant-pods
```

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
    Network's CIDRs and provisions the Calico `IPPool`(s), but `status.vrfs` stays
    empty and no VRF routing is programmed. Add a `destinations` selector matching
    a VRF-bound `Destination` if you need the network plumbed into a VRF.

!!! warning "`status.ipPools` stays empty"
    The `IPPool`s are provisioned by the **`platform-coil`** controller. If
    `status.ipPools` is empty while the Network resolves, check that
    `platform-coil` is running and that the Calico `IPPool` CRD
    (`ippools.crd.projectcalico.org`) is installed — the controller waits for the
    CRD before creating pools.

## Related

- [Layer2Attachment](layer2-attachment.md) — attach a Network as an L2 segment.
- [Inbound](inbound.md) — allocate ingress LoadBalancer IPs.
- [CRD Reference](../reference/crd-reference.md#podnetwork) — full schema.
