---
title: BGPPeering
description: >-
  Define a BGP session — either a listen-range on an L2 attachment, or tenant
  BGPaaS over auto-generated ULA loopback addresses.
---

# BGPPeering

A `BGPPeering` declares a BGP session between a node and a workload. It comes in
two mutually exclusive **modes** selected by `spec.mode`:

- **`listenRange`** — the node opens a BGP listen-range on an
  [`Layer2Attachment`](layer2-attachment.md) and peers with BGP clients living
  on that L2 segment. Prefixes those clients announce (constrained by an
  allow-list of `Network`s) are re-exported into the EVPN fabric.
- **`loopbackPeer`** (BGPaaS) — a tenant workload (for example kube-vip) speaks
  BGP directly to the node over auto-generated ULA IPv6 loopback addresses, and
  advertises the VIP pools of one or more [`Inbound`](inbound.md) resources.

You declare only the intent; the controller resolves the concrete peering IPs,
AS numbers and VRFs and reports them in `status`.

!!! warning "`spec.mode` is immutable"
    The mode is fixed at creation. To switch a peering between `listenRange` and
    `loopbackPeer`, delete and recreate the resource.

## Two modes

| | `listenRange` | `loopbackPeer` (BGPaaS) |
|---|---|---|
| Purpose | Peer with BGP clients on an L2 segment | Tenant workload speaks BGP to the node |
| Required `ref` fields | `attachmentRef` + `networkRefs` | `inboundRefs` |
| Forbidden `ref` fields | `inboundRefs` | `attachmentRef`, `networkRefs` |
| Where the session runs | Listen-range on the L2A's Network | Auto-generated ULA IPv6 loopbacks |
| `status.neighborIPs` source | The L2A's transfer network | ULA IPv6 addresses the controller assigns |
| What clients may announce | Prefixes within `networkRefs` (le 32 / le 128), re-exported into EVPN | The referenced `Inbound` VIP pools |
| `export.communities` | Applies (tags re-exported routes) | Ignored |

## Prerequisites

For **`listenRange`**, in the same namespace:

- A **`Layer2Attachment`** referenced by `ref.attachmentRef`. The listen-range
  CIDR is taken from that L2A's `Network`.
- One or more **`Network`** resources referenced by `ref.networkRefs`. Their
  CIDRs form the import allow-list — clients may only announce prefixes
  contained within them, and those prefixes are re-exported into the EVPN
  fabric.

For **`loopbackPeer`**, in the same namespace:

- One or more **`Inbound`** resources referenced by `ref.inboundRefs`, whose VIP
  pools the tenant advertises.

## listenRange (peer with L2 clients)

A `listenRange` peering opens a BGP listen-range on an existing
`Layer2Attachment`. The example below defines the L2A, a `Network` allow-list,
and the peering together:

```yaml
# The L2A the listen-range is opened on
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Layer2Attachment
metadata:
  name: l2a-bgp
  namespace: default
spec:
  networkRef: "net-vlan501"
  destinations:
    matchLabels:
      type: gateway
---
# Network whose CIDRs L2 clients may announce (import allow-list + EVPN export)
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Network
metadata:
  name: net-bgp-clients
  namespace: default
spec:
  ipv4:
    cidr: "10.250.3.0/24"
  ipv6:
    cidr: "fd75:2d70:f7f7::/64"
---
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: BGPPeering
metadata:
  name: bgpp-e2e
  namespace: default
spec:
  mode: listenRange
  ref:
    attachmentRef: "l2a-bgp"
    networkRefs:
      - "net-bgp-clients"
  workloadAS: 65100
```

How the allow-list and re-export work:

- The node listens for BGP sessions from clients on the L2 segment described by
  `l2a-bgp`. The neighbor addresses come from that L2A's transfer network and
  appear in `status.neighborIPs`; the node's own listen addresses (the IRB
  anycast gateway IPs, one per address family) appear in `status.localIPs`.
- A client may only announce prefixes **contained within** the CIDRs of the
  referenced `Network`s, matched with `le 32` (IPv4) / `le 128` (IPv6). Anything
  outside the allow-list is rejected.
- Accepted prefixes are **re-exported into the EVPN fabric** so the rest of the
  overlay can reach them.

!!! tip "Harden a listenRange session"
    Enable BFD, set an authentication password, tune timers, cap the accepted
    prefix count, and tag the re-exported routes with communities:

    ```yaml
    spec:
      mode: listenRange
      ref:
        attachmentRef: "l2a-bgp"
        networkRefs: ["net-bgp-clients"]
      workloadAS: 65100
      enableBFD: true
      bfdProfile:
        minInterval: 300
      holdTime: 9s
      keepaliveTime: 3s
      maximumPrefixes: 100
      authSecretRef:
        name: bgp-password
      export:
        communities:
          - "65000:100"
    ```

    The `authSecretRef` Secret must carry a `password` key — see
    [Authenticate the session](#authenticate-the-session-with-a-secret).

## loopbackPeer / BGPaaS (tenant speaks BGP)

In `loopbackPeer` mode a tenant workload runs its own BGP speaker and peers with
the node. There is no L2 attachment and no `Network` allow-list; instead the
peering references the [`Inbound`](inbound.md) pools the tenant advertises:

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: BGPPeering
metadata:
  name: bgpaas-kubevip
  namespace: default
spec:
  mode: loopbackPeer
  ref:
    inboundRefs:
      - "prod-ingress"
  workloadAS: 65200
```

How it works:

- The controller assigns **ULA IPv6 loopback addresses** for the session. The
  neighbor address(es) appear in `status.neighborIPs`; the platform-side
  address(es) appear in `status.localIPs`.
- The tenant workload (for example kube-vip) peers with the node over those
  loopback addresses using `workloadAS` as its ASN, and advertises the VIP
  pools of the referenced `Inbound` resources.

!!! note "`export.communities` and the L2 fields do not apply here"
    `attachmentRef`, `networkRefs` and `export` are only meaningful in
    `listenRange` mode. Setting `attachmentRef` or `networkRefs` in
    `loopbackPeer` mode is rejected by validation; `export` is ignored.

## Field reference

Only the most-used fields are listed here. For the complete, generated schema
see the [CRD reference](../reference/crd-reference.md#bgppeering).

| Field | Type | Notes |
|---|---|---|
| `mode` | enum `listenRange` \| `loopbackPeer` | Required. **Immutable.** |
| `ref.attachmentRef` | string | `listenRange` only — required there, forbidden for `loopbackPeer`. |
| `ref.networkRefs` | string array (min 1) | `listenRange` only — the import allow-list. Required there, forbidden for `loopbackPeer`. |
| `ref.inboundRefs` | string array (min 1) | `loopbackPeer` only — required there, forbidden for `listenRange`. |
| `workloadAS` | integer 1–4294967295 | Required. Workload/tenant-side ASN, asplain notation. |
| `advertiseTransferNetwork` | bool | Advertise the transfer-network prefix to the peer. |
| `holdTime` | duration | BGP hold timer (e.g. `9s`). |
| `keepaliveTime` | duration | BGP keepalive timer (e.g. `3s`). |
| `maximumPrefixes` | integer ≥ 1 | Cap on prefixes accepted from the peer. |
| `addressFamilies` | enum array `ipv4Unicast` \| `ipv6Unicast` | Defaults to dual-stack if omitted. |
| `enableBFD` | bool | Enable BFD for fast failure detection. |
| `bfdProfile.minInterval` | integer 50–60000 (ms) | BFD minimum interval; used when `enableBFD` is true. |
| `authSecretRef.name` | string | Secret (key `password`) with the BGP session password. |
| `export.communities` | string array | `listenRange` only — communities added to re-exported prefixes. |

!!! note "Reference exclusivity is enforced per mode"
    The CEL validation rules require exactly the `ref` fields for the chosen
    mode and forbid the others. See [Troubleshooting](#troubleshooting).

## Common tasks

### Set the workload ASN

`workloadAS` is required and is the ASN of the peer (the workload/tenant side).
Use asplain notation; for a 4-byte ASN use the full 32-bit integer:

```yaml
spec:
  workloadAS: 4200000000
```

### Restrict address families

By default both IPv4 and IPv6 unicast are negotiated. Pin a single family:

```yaml
spec:
  addressFamilies:
    - ipv6Unicast
```

### Limit prefixes accepted from the peer

```yaml
spec:
  maximumPrefixes: 100
```

### Enable BFD

```yaml
spec:
  enableBFD: true
  bfdProfile:
    minInterval: 300
```

### Authenticate the session with a Secret

Create a Secret with a `password` key, then reference it:

```bash
kubectl create secret generic bgp-password \
  --namespace default \
  --from-literal=password=s3cr3t
```

```yaml
spec:
  authSecretRef:
    name: bgp-password
```

The controller reads the Secret and propagates the password to nodes via
`NodeNetworkConfig`; node agents never need direct Secret RBAC.

### Tag re-exported routes with communities (listenRange only)

```yaml
spec:
  export:
    communities:
      - "65000:100"
```

Communities are attached additively to the prefixes re-exported into the EVPN
fabric. This field is ignored in `loopbackPeer` mode.

### Tune hold and keepalive timers

```yaml
spec:
  holdTime: 9s
  keepaliveTime: 3s
```

## Verify

List peerings (short name `bgpp`):

```bash
kubectl get bgpp -n default
```

Inspect the controller-resolved status:

```bash
kubectl get bgppeering bgpp-e2e -n default -o jsonpath='{.status}' | jq
```

Key status fields:

| Field | Meaning |
|---|---|
| `asNumber` | Platform-side AS number. |
| `neighborASNumber` | Remote peer AS number. |
| `neighborIPs` | Session neighbor IPs (L2A transfer network for `listenRange`; ULA IPv6 for `loopbackPeer`). |
| `localIPs` | Local platform-side peering IPs. |
| `workloadASNumber` | Mirrors `spec.workloadAS`. |
| `vrfs` | VRFs this peering relates to (from the L2A / Inbounds). |
| `conditions` | Look for `Ready=True`. |

```bash
kubectl get bgppeering bgpp-e2e -n default \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
```

## Troubleshooting

**CEL validation errors on the `ref` fields.** The wrong `ref` field for the
mode is rejected at admission. The exact messages are:

- `attachmentRef is required for listenRange mode and forbidden for loopbackPeer mode`
- `networkRefs is required for listenRange mode and forbidden for loopbackPeer mode`
- `inboundRefs is required for loopbackPeer mode and forbidden for listenRange mode`

Set exactly `attachmentRef` + `networkRefs` for `listenRange`, or exactly
`inboundRefs` for `loopbackPeer`.

**`mode is immutable`.** You cannot change `spec.mode` on an existing object.
Delete and recreate the `BGPPeering`.

**Referenced resource missing.** A `listenRange` peering needs its
`Layer2Attachment` and every `Network` in `networkRefs` to exist in the same
namespace; a `loopbackPeer` peering needs every `Inbound` in `inboundRefs`. If a
reference cannot be resolved the peering will not become `Ready` — check
`status.conditions` and confirm the referenced names.

**Session not establishing.** If both ends are configured but the session stays
down, check for a mismatch between peers:

- `authSecretRef` set on one side only, or a different password — the Secret
  must exist and carry a `password` key.
- Mismatched `holdTime` / `keepaliveTime`.
- BFD enabled on only one side, or incompatible `bfdProfile.minInterval`.
- For `listenRange`, the client announcing prefixes outside the `networkRefs`
  allow-list — those are silently rejected.

## Related

- [Layer2Attachment guide](layer2-attachment.md) — the L2 segment `listenRange`
  peers on.
- [Inbound guide](inbound.md) — the VIP pools a `loopbackPeer` tenant advertises.
- [CRD reference: BGPPeering](../reference/crd-reference.md#bgppeering) — full
  field schema.
- [Concepts](../getting-started/concepts.md) — the intent model and immutable
  fields.
