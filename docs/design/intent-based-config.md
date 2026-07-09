---
title: Intent-Based Config
description: >-
  Background and rationale for the intent-based network-connector API group.
---

# Intent-Based Configuration

The user-facing API group `network-connector.sylvaproject.org/v1alpha1` is an
**intent-based** layer: you declare *what* connectivity a workload needs, and the
operator derives and rolls out the low-level per-node configuration. This page
summarizes the design; the full proposal lives in the repository.

!!! info "Full design proposal"
    The authoritative, in-depth design (all resources, decisions D1–D39,
    behavioral tables, cross-cluster sync) is maintained in the repository at
    [`docs/proposals/02-intent-based-config/`](https://github.com/telekom/das-schiff-network-operator/tree/main/docs/proposals/02-intent-based-config).

## Motivation

The original API (`network.t-caas.telekom.com`, see
[Legacy API](../advanced/legacy-api.md)) exposed low-level, per-node primitives:
`VRFRouteConfiguration`, `Layer2NetworkConfiguration`, and hand-authored BGP
peering. That model required users to understand FRR, VXLAN, VRFs and MetalLB and
to keep many resources consistent by hand.

The intent model raises the abstraction level:

- A small **foundation** (`VRF`, `Network`) is the contract boundary with
  infrastructure provisioning.
- A **routing layer** (`Destination`) names reachable prefixes.
- A **usage layer** (`Inbound`, `Outbound`, `Layer2Attachment`, `PodNetwork`,
  `BGPPeering`, traffic mirroring) expresses workload needs by *reference*, not
  by duplicating fabric detail.

The operator resolves references, renders `NetworkConfigRevision` snapshots and
per-node `NodeNetworkConfig` / `NodeNetplanConfig`, and rolls them out node by
node — see [Debugging](../advanced/debugging.md).

## Key design properties

- **Dual-mode**: the same CRDs support HBN (VXLAN/VRF overlay) and non-HBN
  (physical VLAN / MetalLB-only) deployments, selected by field presence rather
  than a mode switch. See
  [Concepts → Deployment modes](../getting-started/concepts.md#deployment-modes-hbn-vs-non-hbn-pure-l2-netplan).
- **Loose coupling via selectors**: usage resources select `Destination`s by
  label instead of referencing them by name.
- **Provisioner-agnostic foundation**: `VRF` and `Network` can be created by any
  provisioner (BM4X, Netbox, or by hand); provider-specific fields never leak
  into this API group.
- **Safe lifecycle**: finalizers enforce a deletion order so referenced
  resources cannot be removed while in use
  ([Concepts → Lifecycle](../getting-started/concepts.md#lifecycle-and-deletion-order)).
- **Cross-cluster**: resources are authored in a management cluster and synced
  into workload clusters.

## Related

- [Concepts](../getting-started/concepts.md)
- [CRD Reference](../reference/crd-reference.md)
- [Legacy API](../advanced/legacy-api.md)
