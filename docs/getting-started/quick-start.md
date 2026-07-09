---
title: Quick Start
description: >-
  A minimal end-to-end example — attach a network and expose an ingress
  LoadBalancer IP with the intent CRDs.
---

# Quick Start

This walkthrough creates a minimal **HBN** setup: a VRF and Network foundation, a
Destination describing reachable prefixes, and an `Inbound` that allocates an
ingress LoadBalancer IP and advertises it via BGP.

!!! note "Before you begin"
    - The operator is [installed](installation.md).
    - You understand the [layers and vocabulary](concepts.md) (VRF → Network →
      Destination → usage resource).
    - Adjust the CIDRs, VNIs, VLAN and route target to values valid in your
      fabric. The values below are illustrative.

## 1. Foundation: VRF + Network

In many environments these are created by an infrastructure provisioner. For a
trial, create them directly:

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: VRF
metadata:
  name: prod
spec:
  vrf: PROD
  vni: 100
  routeTarget: "65000:100"
---
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Network
metadata:
  name: prod-lb
spec:
  ipv4:
    cidr: "198.51.100.0/24"
    prefixLength: 28
  vlan: 100
  vni: 4000100
```

## 2. Routing: Destination

A `Destination` names the prefixes reachable through the VRF and carries a label
that usage resources select on:

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Destination
metadata:
  name: corp-dc
  labels:
    env: prod
spec:
  vrfRef: prod
  prefixes:
    - "10.0.0.0/8"
    - "172.16.0.0/12"
```

## 3. Usage: Inbound (ingress LoadBalancer IPs)

`Inbound` allocates IPs from the Network for a MetalLB pool and, because
`destinations` is set, exports them as host routes into the `prod` VRF (HBN
mode):

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Inbound
metadata:
  name: prod-ingress
spec:
  networkRef: prod-lb
  destinations:
    matchLabels:
      env: prod
  count: 4
  advertisement:
    type: bgp
```

## 4. Apply and verify

```bash
kubectl apply -f foundation.yaml -f destination.yaml -f inbound.yaml

# Watch the Inbound become Ready
kubectl get inbound prod-ingress -w

# Inspect allocation + conditions
kubectl get inbound prod-ingress -o yaml | yq '.status'
```

When `status.conditions` shows `Ready=True`, the pool exists and the IPs are
advertised. The allocated addresses appear under `status.addresses`.

## 5. Clean up

Delete in reverse dependency order (finalizers enforce this — see
[Concepts → Lifecycle](concepts.md#lifecycle-and-deletion-order)):

```bash
kubectl delete inbound prod-ingress
kubectl delete destination corp-dc
kubectl delete network prod-lb
kubectl delete vrf prod
```

## Where to go next

- [Inbound guide](../guides/inbound.md) — LoadBalancer IPs in depth (BGP vs L2,
  explicit addresses, pool naming).
- [Layer2Attachment guide](../guides/layer2-attachment.md) — attach a network to
  nodes (HBN and non-HBN).
- [Outbound](../guides/outbound.md), [BGPPeering](../guides/bgp-peering.md),
  [PodNetwork](../guides/pod-network.md),
  [Traffic Mirroring](../guides/traffic-mirroring.md).
