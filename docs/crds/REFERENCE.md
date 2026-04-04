# CRD Field Reference

> **API Group:** `network-connector.sylvaproject.org/v1alpha1`
>
> This document provides per-CRD, field-level documentation for every Network
> Connector custom resource. For architecture, dependency graphs, finalizers,
> and cross-cluster design see [README.md](README.md).

---

**Table of Contents**

- [Immutability](#immutability)
- **Foundation** — [VRF](#vrf) · [Network](#network)
- **Routing** — [Destination](#destination)
- **Usage** — [Layer2Attachment](#layer2attachment) · [Inbound](#inbound) · [Outbound](#outbound) · [PodNetwork](#podnetwork)
- **BGP** — [BGPPeering](#bgppeering)
- **Mirroring** — [Collector](#collector) · [TrafficMirror](#trafficmirror)
- **Policy** — [AnnouncementPolicy](#announcementpolicy)
- **Node** — [InterfaceConfig](#interfaceconfig) · [NodeNetworkStatus](#nodenetworkstatus)

---

## Immutability

Several fields across CRDs are marked **immutable** (enforced via CEL
`x-kubernetes-validations` comparing `self` to `oldSelf`). Once set at
creation, the value cannot be changed — the resource must be deleted and
recreated.

| Immutable Field | CRD | Reason |
|-----------------|-----|--------|
| `spec.vrf` | VRF | Backbone VRF name maps 1:1 to a VXLAN-based L3 domain; changing it would orphan every Destination and fabric-side binding. |
| `spec.networkRef` | L2A, Inbound, Outbound, PodNetwork | IP allocations, VXLAN tunnels, and MetalLB pools are derived from the referenced Network. Rebinding would require full teardown. |
| `spec.interfaceName` | Layer2Attachment | Linux interface name is embedded in netlink state, BPF maps, and FRR config. Renaming would break live traffic. |
| `spec.sriov.enabled` | Layer2Attachment | Toggling SR-IOV reallocates VFs on the NIC — disruptive to every pod already using the interface. |
| `spec.mode` | BGPPeering | `listenRange` and `loopbackPeer` produce fundamentally different BGP sessions (L2-peered vs. ULA loopback). Switching mode is a full session rebuild. |
| `spec.protocol` | Collector | GRE encapsulation type (L2 vs. L3) determines tunnel interface configuration. Changing protocol requires tunnel teardown and recreation. |

---

## Foundation Layer

### VRF

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `ncvrf`
**Scope:** Namespaced

Backbone VRF identity — maps a human-readable name to a VXLAN Network Identifier
and BGP route target. Every VRF is shared across Destinations, Collectors, and
AnnouncementPolicies.

#### Spec

| Field | Type | Required | Immutable | Description |
|-------|------|:--------:|:---------:|-------------|
| `vrf` | string | ✅ | 🔒 | VRF name in the backbone fabric. Max 12 characters. |
| `vni` | \*int32 | | | VXLAN Network Identifier. Range: 1–16 777 215. |
| `routeTarget` | \*string | | | BGP route target. Format: `ASN:value` (e.g. `65000:100`). Validated against `^\d+:\d+$`. |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | int64 | Last `.metadata.generation` reconciled. |
| `referenceCount` | int32 | Number of Destinations currently referencing this VRF. |
| `conditions` | []Condition | Standard conditions. Types: **Ready**, **DuplicateVRF**. |

#### Validation Rules

- `vrf` is immutable after creation (CEL: `self.vrf == oldSelf.vrf`).
- `vni` must be > 0 and ≤ 16 777 215.
- `routeTarget` must match the pattern `^\d+:\d+$` (webhook).
- Two VRFs in the same namespace with the same `spec.vrf` value produce a
  **DuplicateVRF** condition.

#### Finalizers

| Finalizer | Set By |
|-----------|--------|
| `vrf-in-use` | Destination, Collector controllers |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: VRF
metadata:
  name: prod
  namespace: cluster-a
spec:
  vrf: PROD
  vni: 100
  routeTarget: "65000:100"
```

---

### Network

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `ncnet`
**Scope:** Namespaced

IP address pool definition. At least one of `ipv4`, `ipv6`, or `vlan` must be
specified. Referenced by all usage CRDs (L2A, Inbound, Outbound, PodNetwork).

#### Spec

| Field | Type | Required | Description |
|-------|------|:--------:|-------------|
| `ipv4` | \*IPNetwork | | IPv4 pool. |
| `ipv4.cidr` | string | ✅ | IPv4 CIDR (validated via CEL `isCIDR()`). |
| `ipv4.prefixLength` | \*int32 | | Allocation prefix length. Range: 1–128. |
| `ipv6` | \*IPNetwork | | IPv6 pool. |
| `ipv6.cidr` | string | ✅ | IPv6 CIDR (validated via CEL `isCIDR()`). |
| `ipv6.prefixLength` | \*int32 | | Allocation prefix length. Range: 1–128. |
| `vlan` | \*int32 | | VLAN ID. Range: 1–4094. |
| `vni` | \*int32 | | VXLAN Network Identifier. Range: 1–16 777 215. |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | int64 | Last `.metadata.generation` reconciled. |
| `referenceCount` | int32 | Number of usage CRDs referencing this Network. |
| `conditions` | []Condition | Standard conditions. Type: **Ready**. |

#### Validation Rules

- At least one of `ipv4`, `ipv6`, or `vlan` must be specified (CEL).
- `cidr` fields are validated with `isCIDR()`.
- `vni` must be > 0 and ≤ 16 777 215.
- `vlan` must be 1–4094.

#### Finalizers

| Finalizer | Set By |
|-----------|--------|
| `network-in-use` | L2A, Inbound, Outbound, PodNetwork controllers |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Network
metadata:
  name: prod-lb
  namespace: cluster-a
spec:
  ipv4:
    cidr: "198.51.100.0/24"
    prefixLength: 28
  ipv6:
    cidr: "2001:db8:1::/48"
    prefixLength: 64
  vlan: 100
  vni: 10100
```

---

## Routing Layer

### Destination

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `dst`
**Scope:** Namespaced

Routing target — defines prefixes reachable either via a VRF or via a static
next-hop. Usage CRDs select Destinations with label selectors to build their
routing tables.

#### Spec

| Field | Type | Required | Description |
|-------|------|:--------:|-------------|
| `vrfRef` | \*string | | VRF name. **Mutually exclusive** with `nextHop`. |
| `prefixes` | []string | | CIDRs reachable via this destination (e.g. `10.0.0.0/8`). |
| `nextHop` | \*NextHopConfig | | Static next-hop. **Mutually exclusive** with `vrfRef`. |
| `nextHop.ipv4` | \*string | | IPv4 next-hop address. |
| `nextHop.ipv6` | \*string | | IPv6 next-hop address. |
| `ports` | []DestinationPort | | Port-level restrictions (firewall rules). |
| `ports[].protocol` | string | ✅ | `TCP`, `UDP`, or `SCTP`. |
| `ports[].port` | \*int32 | | Single port (1–65 535). **Mutually exclusive** with `portRange`. |
| `ports[].portRange` | \*PortRange | | Port range. **Mutually exclusive** with `port`. |
| `ports[].portRange.start` | int32 | ✅ | Start port (1–65 535). |
| `ports[].portRange.end` | int32 | ✅ | End port (1–65 535). Must be ≥ `start`. |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | int64 | Last `.metadata.generation` reconciled. |
| `referenceCount` | int32 | Number of usage CRDs selecting this Destination. |
| `conditions` | []Condition | Standard conditions. Type: **Ready**. |

#### Validation Rules

- Exactly one of `vrfRef` or `nextHop` must be set (CEL).
- When `nextHop` is specified, at least one of `ipv4` or `ipv6` must be set (CEL).
- `prefixes` entries must be valid CIDRs.
- Within each port entry, exactly one of `port` or `portRange` may be set.
- `portRange.start` must be ≤ `portRange.end` (CEL).

#### Finalizers

| Finalizer | Set By |
|-----------|--------|
| `destination-in-use` | L2A, Inbound, Outbound, PodNetwork controllers |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Destination
metadata:
  name: corp-dc
  namespace: cluster-a
  labels:
    env: prod
spec:
  vrfRef: prod
  prefixes:
    - "10.0.0.0/8"
    - "172.16.0.0/12"
  ports:
    - protocol: TCP
      portRange:
        start: 443
        end: 443
```

---

## Usage Layer

### Layer2Attachment

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `l2a`
**Scope:** Namespaced

Attaches a Network as an L2 segment to nodes. Supports **HBN mode** (VXLAN +
VRF overlay) and **non-HBN mode** (physical interface via `interfaceRef`).

#### Spec

| Field | Type | Required | Immutable | Description |
|-------|------|:--------:|:---------:|-------------|
| `networkRef` | string | ✅ | 🔒 | Network name (MinLength=1). |
| `destinations` | \*LabelSelector | | | Selects Destinations for VRF plumbing. |
| `nodeSelector` | \*LabelSelector | | | Target nodes for the L2 segment. |
| `interfaceRef` | \*string | | | Existing host interface (non-HBN mode). |
| `interfaceName` | \*string | | 🔒 | Interface name suffix. Immutable once set. |
| `mtu` | \*int32 | | | Interface MTU. Range: 1000–9000. |
| `disableAnycast` | \*bool | | | Disable anycast gateway on this segment. |
| `disableNeighborSuppression` | \*bool | | | Disable ARP/ND suppression in VXLAN. |
| `disableSegmentation` | \*bool | | | Disable TX/RX segmentation offload. |
| `sriov` | \*SRIOVConfig | | | SR-IOV configuration. |
| `sriov.enabled` | bool | ✅ | 🔒 | Enable SR-IOV. Immutable after creation. |
| `nodeIPs` | \*NodeIPConfig | | | Node IP assignment from the network. |
| `nodeIPs.enabled` | bool | ✅ | | Assign node IPs. |
| `nodeIPs.reservedForPods` | \*int32 | | | IPs to reserve for pods (Min: 0). |
| `routes` | []AdditionalRoute | | | Extra static routes. |
| `routes[].prefixes` | []string | ✅ | | Prefixes for this route (MinItems=1). |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | int64 | Last `.metadata.generation` reconciled. |
| `sriovVlanID` | \*int32 | Allocated SR-IOV VLAN ID. |
| `anycast` | \*AnycastStatus | Anycast gateway details. |
| `anycast.mac` | string | Anycast MAC address. |
| `anycast.gateway` | string | Anycast gateway IPv6 address. |
| `anycast.gatewayV4` | \*string | Anycast gateway IPv4 address. |
| `nodeAddresses` | map[string]AddressAllocation | Per-node IP allocations. |
| `conditions` | []Condition | Types: **Ready**, **Resolved**, **Applied**, **InterfaceNotFound**. |

#### Validation Rules

- `networkRef` is immutable (CEL: `self.networkRef == oldSelf.networkRef`).
- `interfaceName` is immutable once set (CEL: `!has(oldSelf.interfaceName) || self.interfaceName == oldSelf.interfaceName`).
- `sriov.enabled` is immutable (CEL: `self.sriov.enabled == oldSelf.sriov.enabled`).
- `mtu` must be between 1000 and 9000.

#### Finalizers

| Finalizer | Set By |
|-----------|--------|
| `network-in-use` | Own controller (on referenced Network) |
| `destination-in-use` | Own controller (on selected Destinations) |
| `cleanup` | Own controller |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Layer2Attachment
metadata:
  name: prod-l2
  namespace: cluster-a
spec:
  networkRef: prod-lb
  destinations:
    matchLabels:
      env: prod
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
  mtu: 9000
  nodeIPs:
    enabled: true
    reservedForPods: 2
  routes:
    - prefixes:
        - "10.100.0.0/16"
```

---

### Inbound

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `ib`
**Scope:** Namespaced

Allocates IPs from a Network for MetalLB `IPAddressPool` resources and
configures BGP or L2 advertisement. Used for ingress load-balancer VIPs.

#### Spec

| Field | Type | Required | Immutable | Description |
|-------|------|:--------:|:---------:|-------------|
| `networkRef` | string | ✅ | 🔒 | Network name (MinLength=1). |
| `destinations` | \*LabelSelector | | | Selects Destinations for HBN-mode routing. |
| `count` | \*int32 | | | Number of IPs to allocate (Min: 1). **Mutually exclusive** with `addresses`. |
| `addresses` | \*AddressAllocation | | | Explicit IP allocation. **Mutually exclusive** with `count`. |
| `addresses.ipv4` | []string | | | IPv4 CIDRs. |
| `addresses.ipv6` | []string | | | IPv6 CIDRs. |
| `poolName` | \*string | | | Override the MetalLB IPAddressPool name. |
| `tenantLoadBalancerClass` | \*string | | | LoadBalancerClass for tenant services. |
| `advertisement` | AdvertisementConfig | ✅ | | Advertisement configuration. |
| `advertisement.type` | string | ✅ | | `bgp` or `l2`. |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | int64 | Last `.metadata.generation` reconciled. |
| `addresses` | \*AddressAllocation | Resolved IP allocation (from `count` or echoed from spec). |
| `poolName` | \*string | Resolved MetalLB pool name. |
| `conditions` | []Condition | Types: **Ready**, **Resolved**, **Applied**. |

#### Validation Rules

- `networkRef` is immutable (CEL).
- Exactly one of `count` or `addresses` must be set (CEL).
- Address CIDRs must be valid.
- `count` must be ≥ 1.

#### Finalizers

| Finalizer | Set By |
|-----------|--------|
| `network-in-use` | Own controller (on referenced Network) |
| `destination-in-use` | Own controller (on selected Destinations) |
| `cleanup` | Own controller |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Inbound
metadata:
  name: prod-ingress
  namespace: cluster-a
spec:
  networkRef: prod-lb
  destinations:
    matchLabels:
      env: prod
  count: 4
  advertisement:
    type: bgp
```

---

### Outbound

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `ob`
**Scope:** Namespaced

Enables egress via SNAT by allocating IPs from a Network for Coil Egress
gateways and Calico IP pools.

#### Spec

| Field | Type | Required | Immutable | Description |
|-------|------|:--------:|:---------:|-------------|
| `networkRef` | string | ✅ | 🔒 | Network name (MinLength=1). |
| `destinations` | \*LabelSelector | | | Selects Destinations for routing. |
| `count` | \*int32 | | | Number of IPs to allocate (Min: 1). **Mutually exclusive** with `addresses`. |
| `addresses` | \*AddressAllocation | | | Explicit IP allocation. **Mutually exclusive** with `count`. |
| `addresses.ipv4` | []string | | | IPv4 CIDRs. |
| `addresses.ipv6` | []string | | | IPv6 CIDRs. |
| `replicas` | \*int32 | | | Coil egress pod replicas (Min: 1). |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | int64 | Last `.metadata.generation` reconciled. |
| `addresses` | \*AddressAllocation | Resolved IP allocation. |
| `conditions` | []Condition | Types: **Ready**, **Resolved**, **Applied**. |

#### Validation Rules

- `networkRef` is immutable (CEL).
- Exactly one of `count` or `addresses` must be set (CEL).
- Address CIDRs must be valid.
- `count` must be ≥ 1.
- `replicas` must be ≥ 1.

#### Finalizers

| Finalizer | Set By |
|-----------|--------|
| `network-in-use` | Own controller (on referenced Network) |
| `destination-in-use` | Own controller (on selected Destinations) |
| `cleanup` | Own controller |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Outbound
metadata:
  name: prod-egress
  namespace: cluster-a
spec:
  networkRef: prod-lb
  destinations:
    matchLabels:
      env: prod
  count: 2
  replicas: 3
```

---

### PodNetwork

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `pnet`
**Scope:** Namespaced

Provides additional pod-level networks from a Network for CNI integration
(e.g. Multus secondary interfaces).

#### Spec

| Field | Type | Required | Immutable | Description |
|-------|------|:--------:|:---------:|-------------|
| `networkRef` | string | ✅ | 🔒 | Network name (MinLength=1). |
| `destinations` | \*LabelSelector | | | Selects Destinations for routing. |
| `routes` | []AdditionalRoute | | | Extra static routes. |
| `routes[].prefixes` | []string | ✅ | | Route prefixes (MinItems=1). |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | int64 | Last `.metadata.generation` reconciled. |
| `conditions` | []Condition | Types: **Ready**, **Resolved**, **Applied**. |

#### Validation Rules

- `networkRef` is immutable (CEL: `self.networkRef == oldSelf.networkRef`).
- `networkRef` must not be empty (MinLength=1).

#### Finalizers

| Finalizer | Set By |
|-----------|--------|
| `network-in-use` | Own controller (on referenced Network) |
| `destination-in-use` | Own controller (on selected Destinations) |
| `cleanup` | Own controller |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: PodNetwork
metadata:
  name: prod-secondary
  namespace: cluster-a
spec:
  networkRef: prod-lb
  destinations:
    matchLabels:
      env: prod
  routes:
    - prefixes:
        - "10.200.0.0/16"
```

---

## BGP

### BGPPeering

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `bgpp`
**Scope:** Namespaced

Configures a BGP session in one of two modes:

- **`listenRange`** — L2 attachment–based BGP peering (requires `attachmentRef`)
- **`loopbackPeer`** — BGPaaS with auto-generated ULA IPv6 addresses (forbids `attachmentRef`)

Both modes always reference one or more Inbound resources for IP pool
advertisement.

#### Spec

| Field | Type | Required | Immutable | Description |
|-------|------|:--------:|:---------:|-------------|
| `mode` | string | ✅ | 🔒 | `listenRange` or `loopbackPeer`. |
| `ref` | BGPPeeringRef | ✅ | | Reference configuration. |
| `ref.attachmentRef` | \*string | | | Layer2Attachment name. **Required** for `listenRange`, **forbidden** for `loopbackPeer`. |
| `ref.inboundRefs` | []string | ✅ | | Inbound names (MinItems=1). |
| `advertiseTransferNetwork` | \*bool | | | Advertise the transfer network prefix. |
| `holdTime` | \*Duration | | | BGP hold timer. |
| `keepaliveTime` | \*Duration | | | BGP keepalive timer. |
| `maximumPrefixes` | \*int32 | | | Max accepted prefixes (Min: 1). |
| `workloadAS` | \*int64 | | | Workload ASN (1–4 294 967 295). Auto-generated if omitted. |
| `addressFamilies` | []BGPAddressFamily | | | Address families (MinItems=1). Values: `ipv4Unicast`, `ipv6Unicast`. |
| `enableBFD` | \*bool | | | Enable Bidirectional Forwarding Detection. |
| `bfdProfile` | \*BFDProfile | | | BFD tuning parameters. |
| `bfdProfile.minInterval` | uint32 | ✅ | | BFD min interval in ms (50–60 000). |
| `authSecretRef` | \*LocalObjectReference | | | Secret containing a `password` key for BGP session authentication. |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | int64 | Last `.metadata.generation` reconciled. |
| `asNumber` | \*int64 | Platform-side autonomous system number. |
| `neighborIPs` | []string | BGP session peer IP addresses. |
| `neighborASNumber` | \*int64 | Remote peer ASN (fabric/site). |
| `workloadASNumber` | \*int64 | Resolved workload ASN (from spec or auto-generated). |
| `conditions` | []Condition | Type: **Ready**. |

#### Validation Rules

- `mode` is immutable (CEL: `self.mode == oldSelf.mode`).
- `attachmentRef` is **required** when `mode == listenRange` and **forbidden**
  when `mode == loopbackPeer` (CEL).
- `ref.inboundRefs` must contain at least 1 item.
- `workloadAS` range: 1–4 294 967 295.
- `bfdProfile.minInterval` range: 50–60 000 ms.

#### Finalizers

| Finalizer | Set By |
|-----------|--------|
| `cleanup` | Own controller |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: BGPPeering
metadata:
  name: prod-bgp
  namespace: cluster-a
spec:
  mode: listenRange
  ref:
    attachmentRef: prod-l2
    inboundRefs:
      - prod-ingress
  holdTime: 90s
  keepaliveTime: 30s
  maximumPrefixes: 1000
  workloadAS: 65100
  addressFamilies:
    - ipv4Unicast
    - ipv6Unicast
  enableBFD: true
  bfdProfile:
    minInterval: 300
```

---

## Traffic Mirroring

### Collector

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `col`
**Scope:** Namespaced

GRE collector endpoint for traffic mirroring. Establishes a GRE tunnel to a
remote collector and binds it to a mirror VRF with a loopback interface for
source addressing.

#### Spec

| Field | Type | Required | Immutable | Description |
|-------|------|:--------:|:---------:|-------------|
| `address` | string | ✅ | | Collector IP address. |
| `protocol` | string | ✅ | 🔒 | GRE encapsulation: `l3gre` or `l2gre`. |
| `key` | \*uint32 | | | GRE tunnel key. |
| `mirrorVRF` | MirrorVRFRef | ✅ | | Mirror VRF binding. |
| `mirrorVRF.name` | string | ✅ | | VRF name (MinLength=1). |
| `mirrorVRF.loopback` | LoopbackConfig | ✅ | | Loopback interface for tunnel source. |
| `mirrorVRF.loopback.name` | string | ✅ | | Loopback interface name (MinLength=1). |
| `mirrorVRF.loopback.poolRef` | TypedLocalObjectReference | ✅ | | CAPI IPAM pool reference. |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | int64 | Last `.metadata.generation` reconciled. |
| `greInterface` | \*string | Generated GRE interface name on nodes. |
| `referenceCount` | int32 | Number of TrafficMirrors referencing this Collector. |
| `activeNodes` | int32 | Nodes where this Collector is active. |
| `conditions` | []Condition | Type: **Ready**. |

#### Validation Rules

- `protocol` is immutable (CEL: `self.protocol == oldSelf.protocol`).
- `address` must be a valid IP.
- `mirrorVRF.name` and `mirrorVRF.loopback.name` must not be empty.

#### Finalizers

| Finalizer | Set By |
|-----------|--------|
| `collector-in-use` | TrafficMirror controller |
| `cleanup` | Own controller |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: Collector
metadata:
  name: mirror-collector
  namespace: cluster-a
spec:
  address: "198.51.100.50"
  protocol: l3gre
  key: 42
  mirrorVRF:
    name: MIRROR
    loopback:
      name: lo-mirror
      poolRef:
        apiGroup: ipam.cluster.x-k8s.io
        kind: InClusterIPPool
        name: mirror-loopback-pool
```

---

### TrafficMirror

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `tmir`
**Scope:** Namespaced

Mirrors traffic from a Layer2Attachment, Inbound, or Outbound to a Collector.
Optional traffic matching narrows the mirrored flow.

#### Spec

| Field | Type | Required | Description |
|-------|------|:--------:|-------------|
| `source` | MirrorSource | ✅ | Traffic source to mirror. |
| `source.kind` | string | ✅ | `Layer2Attachment`, `Inbound`, or `Outbound`. |
| `source.name` | string | ✅ | Source resource name (MinLength=1). |
| `collector` | string | ✅ | Collector name (MinLength=1). |
| `direction` | string | ✅ | `ingress`, `egress`, or `both`. |
| `trafficMatch` | \*TrafficMatch | | Optional filter to narrow mirrored traffic. |
| `trafficMatch.srcPrefix` | \*string | | Source IP CIDR. |
| `trafficMatch.dstPrefix` | \*string | | Destination IP CIDR. |
| `trafficMatch.protocol` | \*string | | `TCP`, `UDP`, or `ICMP`. |
| `trafficMatch.srcPort` | \*int32 | | Source port (1–65 535). Requires `protocol` = TCP or UDP. |
| `trafficMatch.dstPort` | \*int32 | | Destination port (1–65 535). Requires `protocol` = TCP or UDP. |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | int64 | Last `.metadata.generation` reconciled. |
| `activeNodes` | int32 | Nodes where mirroring is active. |
| `conditions` | []Condition | Type: **Ready**. |

#### Validation Rules

- `source.name` and `collector` must not be empty.
- `srcPort` and `dstPort` require `protocol` to be `TCP` or `UDP` (CEL).
- CIDR prefixes must be valid.

#### Finalizers

| Finalizer | Set By |
|-----------|--------|
| `cleanup` | Own controller |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: TrafficMirror
metadata:
  name: mirror-prod-ingress
  namespace: cluster-a
spec:
  source:
    kind: Inbound
    name: prod-ingress
  collector: mirror-collector
  direction: both
  trafficMatch:
    dstPrefix: "198.51.100.0/24"
    protocol: TCP
    dstPort: 443
```

---

## Policy

### AnnouncementPolicy

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `ap`
**Scope:** Namespaced

Controls route export behaviour for a VRF — attaches BGP communities to
host routes and optionally advertises an aggregate covering prefix.

#### Spec

| Field | Type | Required | Description |
|-------|------|:--------:|-------------|
| `vrfRef` | string | ✅ | VRF name (MinLength=1). |
| `selector` | \*LabelSelector | | Matches usage CRDs (L2A, Inbound, Outbound, PodNetwork) by label. |
| `hostRoutes` | \*RouteAnnouncementConfig | | Host route announcement settings. |
| `hostRoutes.communities` | []string | | BGP communities attached to host routes. |
| `aggregate` | \*AggregateConfig | | Aggregate route announcement settings. |
| `aggregate.enabled` | \*bool | | Export aggregate route (default: `true`). |
| `aggregate.communities` | []string | | BGP communities attached to the aggregate. |
| `aggregate.prefixLengthV4` | \*int32 | | Override IPv4 aggregate prefix length (1–32). |
| `aggregate.prefixLengthV6` | \*int32 | | Override IPv6 aggregate prefix length (1–128). |

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | int64 | Last `.metadata.generation` reconciled. |
| `matchedUsageCRDs` | int32 | Count of usage CRDs matched by `selector`. |
| `conditions` | []Condition | Type: **Ready**. |

#### Validation Rules

- `vrfRef` must not be empty (MinLength=1).
- `aggregate.prefixLengthV4`: 1–32.
- `aggregate.prefixLengthV6`: 1–128.

#### Finalizers

| Finalizer | Set By |
|-----------|--------|
| `cleanup` | Own controller |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: AnnouncementPolicy
metadata:
  name: prod-communities
  namespace: cluster-a
spec:
  vrfRef: prod
  selector:
    matchLabels:
      env: prod
  hostRoutes:
    communities:
      - "65000:1000"
  aggregate:
    enabled: true
    communities:
      - "65000:2000"
    prefixLengthV4: 24
    prefixLengthV6: 48
```

---

## Node Layer

### InterfaceConfig

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `ifc`
**Scope:** Namespaced

Node-level interface provisioning — configure bonds, ethernet interfaces, and
SR-IOV virtual functions on targeted nodes.

#### Spec

| Field | Type | Required | Description |
|-------|------|:--------:|-------------|
| `nodeSelector` | LabelSelector | ✅ | Target nodes. |
| `ethernets` | map[string]EthernetConfig | | Ethernet interfaces keyed by name. |
| `ethernets[].mtu` | \*int32 | | MTU (1000–9000). |
| `ethernets[].virtualFunctionCount` | \*int32 | | SR-IOV virtual function count (Min: 1). |
| `bonds` | map[string]BondConfig | | Bond interfaces keyed by name. |
| `bonds[].interfaces` | []string | ✅ | Member interfaces (MinItems=1). |
| `bonds[].mtu` | \*int32 | | MTU (1000–9000). |
| `bonds[].parameters` | \*BondParameters | | Bonding parameters. |
| `bonds[].parameters.mode` | string | ✅ | Bond mode (see values below). |
| `bonds[].parameters.miiMonitorInterval` | \*int32 | | MII monitor interval in ms (Min: 0). |
| `bonds[].parameters.lacpRate` | \*string | | LACP rate: `fast` or `slow` (802.3ad only). |
| `bonds[].parameters.upDelay` | \*int32 | | Link up delay in ms (Min: 0). |
| `bonds[].parameters.downDelay` | \*int32 | | Link down delay in ms (Min: 0). |
| `bonds[].parameters.transmitHashPolicy` | \*string | | Hash policy (see values below). |

**Bond modes:** `active-backup`, `802.3ad`, `balance-rr`, `balance-xor`,
`broadcast`, `balance-tlb`, `balance-alb`

**Transmit hash policies:** `layer2`, `layer3+4`, `layer2+3`, `encap2+3`,
`encap3+4`

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | int64 | Last `.metadata.generation` reconciled. |
| `conditions` | []Condition | Types: **Ready**, **Applied**. |
| `nodeStatuses` | []InterfaceConfigNodeStatus | Per-node application status. |
| `nodeStatuses[].node` | string | Node name. |
| `nodeStatuses[].applied` | bool | Whether config was successfully applied. |

#### Validation Rules

- At least one of `ethernets` or `bonds` should be specified.
- Bond `interfaces` must have at least 1 member (MinItems=1).
- MTU must be between 1000 and 9000.
- `virtualFunctionCount` must be ≥ 1.

#### Finalizers

| Finalizer | Set By |
|-----------|--------|
| `cleanup` | Own controller |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: InterfaceConfig
metadata:
  name: worker-bonds
  namespace: cluster-a
spec:
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
  bonds:
    bond0:
      interfaces:
        - enp3s0f0
        - enp3s0f1
      mtu: 9000
      parameters:
        mode: 802.3ad
        lacpRate: fast
        miiMonitorInterval: 100
        transmitHashPolicy: "layer3+4"
  ethernets:
    enp4s0f0:
      mtu: 9000
      virtualFunctionCount: 8
```

---

### NodeNetworkStatus

**API Version:** `network-connector.sylvaproject.org/v1alpha1`
**Short Name:** `nns`
**Scope:** Namespaced

Per-node network inventory populated by CRA agents running on workload-cluster
nodes. **Read-only** — no webhook, no user-configurable spec fields.

#### Spec

_Empty. This resource is agent-populated._

#### Status

| Field | Type | Description |
|-------|------|-------------|
| `interfaces` | []NodeInterface | Network interfaces on the node. |
| `interfaces[].name` | string | Interface name. |
| `interfaces[].mac` | \*string | MAC address. |
| `interfaces[].mtu` | \*int32 | MTU. |
| `interfaces[].state` | string | `up`, `down`, or `unknown`. |
| `interfaces[].type` | \*string | `physical`, `bond`, `vlan`, `bridge`, `vxlan`, `loopback`, or `virtual`. |
| `interfaces[].members` | []string | Bond member interfaces. |
| `interfaces[].parent` | \*string | Parent interface (for VLANs). |
| `interfaces[].vlanID` | \*int32 | VLAN ID. |
| `interfaces[].addresses` | []string | IP addresses assigned to this interface. |
| `routes` | []NodeRoute | Routing table entries. |
| `routes[].destination` | string | Destination CIDR. |
| `routes[].gateway` | \*string | Gateway address. |
| `routes[].interface` | string | Egress interface. |
| `routes[].table` | \*string | Routing table name. |
| `lastUpdated` | \*Time | Timestamp of last agent report. |
| `conditions` | []Condition | Type: **Ready**. |

#### Example

```yaml
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: NodeNetworkStatus
metadata:
  name: worker-01
  namespace: schiff-network
status:
  lastUpdated: "2025-01-15T10:30:00Z"
  interfaces:
    - name: bond0
      mac: "aa:bb:cc:dd:ee:01"
      mtu: 9000
      state: up
      type: bond
      members:
        - enp3s0f0
        - enp3s0f1
      addresses:
        - "10.0.1.10/24"
        - "2001:db8::10/64"
    - name: enp3s0f0
      mac: "aa:bb:cc:dd:ee:02"
      mtu: 9000
      state: up
      type: physical
  routes:
    - destination: "0.0.0.0/0"
      gateway: "10.0.1.1"
      interface: bond0
    - destination: "10.100.0.0/16"
      interface: vxlan100
      table: PROD
  conditions:
    - type: Ready
      status: "True"
      lastTransitionTime: "2025-01-15T10:30:00Z"
```
