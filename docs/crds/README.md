# Network Connector CRDs

API group: `network-connector.sylvaproject.org/v1alpha1`

This package defines 13 intent-based CRDs for declarative network configuration
across management and workload clusters.

## CRD Overview

| CRD | Short Name | Purpose |
|-----|-----------|---------|
| **VRF** | `ncvrf` | Backbone VRF identity — name, VNI, route target |
| **Network** | `ncnet` | IP address pool — CIDR, VLAN, VNI |
| **Destination** | `dst` | Routing target — prefixes via VRF or static next-hop |
| **Layer2Attachment** | `l2a` | Attach a Network as L2 segment to nodes |
| **Inbound** | `ib` | Allocate IPs for MetalLB pools (ingress LBs) |
| **Outbound** | `ob` | Allocate IPs for SNAT / Coil egress |
| **PodNetwork** | `pnet` | Additional pod-level networks from a Network |
| **BGPPeering** | `bgpp` | BGP session — listenRange (L2) or loopbackPeer (BGPaaS); always references Inbound pools |
| **Collector** | `col` | GRE collector endpoint + mirror VRF binding |
| **TrafficMirror** | `tmir` | Mirror traffic from an attachment to a Collector |
| **InterfaceConfig** | `ifc` | Node-level interface provisioning (bonds, NICs) |
| **NodeNetworkStatus** | `nns` | Per-node network inventory (agent-populated) |
| **AnnouncementPolicy** | `ap` | Route export steering — host/aggregate communities + aggregate sizing |

## How Networks Get Created

This operator does **not** provision infrastructure. VRF and Network resources are the
**universal contract boundary** — any infrastructure provisioner (BM4X, Netbox, manual)
creates them, and this operator handles everything downstream.

**Recommended pattern with BM4X:**

```
Git repo (SI engineer writes):
├── networkrequest-prod-lb.yaml     # bm4x.t-caas.../NetworkRequest "prod-lb"
│                                    #   configurationType, harmonization, project...
├── inbound-prod.yaml               # networkRef: prod-lb  ← same name
└── bgppeering-prod.yaml            # refs inbound-prod
```

The BM4X operator processes the `NetworkRequest`, calls the BM4X API, and creates a
`network-connector.sylvaproject.org/Network` with the **same name**. Usage CRDs reference
the name the SI engineer chose — normal K8s reconciliation retries until the Network exists.

All BM4X-specific fields (configurationType, harmonization, project, bgpOverSriov) stay
encapsulated in the BM4X operator and never leak into this API group.

## Resource Dependency Graph

```mermaid
graph TD
    subgraph "Infrastructure Provisioning<br/>(external operator, e.g. BM4X)"
        ExtReq["NetworkRequest<br/><i>provider-specific</i>"]
    end

    subgraph "Foundation Layer"
        VRF["<b>VRF</b><br/>vrf, vni, routeTarget"]
        Network["<b>Network</b><br/>ipv4/ipv6, vlan, vni"]
    end

    subgraph "Routing Layer"
        Destination["<b>Destination</b><br/>prefixes, vrfRef | nextHop"]
    end

    subgraph "Usage Layer (Attachments)"
        L2A["<b>Layer2Attachment</b><br/>networkRef, destinations, nodeSelector"]
        Inbound["<b>Inbound</b><br/>networkRef, destinations, MetalLB pools"]
        Outbound["<b>Outbound</b><br/>networkRef, destinations, SNAT/egress"]
        PodNetwork["<b>PodNetwork</b><br/>networkRef, destinations, CNI integration"]
    end

    subgraph "BGP"
        BGPPeering["<b>BGPPeering</b><br/>mode, inboundRefs, attachmentRef, timers, BFD"]
    end

    subgraph "Traffic Mirroring"
        Collector["<b>Collector</b><br/>GRE endpoint, mirrorVRF"]
        TrafficMirror["<b>TrafficMirror</b><br/>source, direction, match"]
    end

    subgraph "Policy"
        AP["<b>AnnouncementPolicy</b><br/>vrfRef, hostRoutes, aggregate"]
    end

    subgraph "Node Layer"
        IFC["<b>InterfaceConfig</b><br/>bonds, ethernets, SR-IOV, nodeSelector"]
        NNS["<b>NodeNetworkStatus</b><br/>interfaces, routes"]
    end

    ExtReq -- "creates (ownerRef)" --> VRF
    ExtReq -- "creates (ownerRef)" --> Network

    Destination -- "vrfRef" --> VRF

    L2A -- "networkRef" --> Network
    Inbound -- "networkRef" --> Network
    Outbound -- "networkRef" --> Network
    PodNetwork -- "networkRef" --> Network

    L2A -- "destinations (selector)" --> Destination
    Inbound -- "destinations (selector)" --> Destination
    Outbound -- "destinations (selector)" --> Destination
    PodNetwork -- "destinations (selector)" --> Destination

    BGPPeering -- "attachmentRef (listenRange)" --> L2A
    BGPPeering -- "inboundRefs (both modes)" --> Inbound

    TrafficMirror -- "collector" --> Collector
    TrafficMirror -- "source" --> L2A
    TrafficMirror -- "source" --> Inbound
    TrafficMirror -- "source" --> Outbound

    Collector -- "mirrorVRF" --> VRF

    AP -- "vrfRef" --> VRF
    AP -- "selector" --> L2A
    AP -- "selector" --> Inbound
    AP -- "selector" --> Outbound
    AP -- "selector" --> PodNetwork

    style ExtReq fill:#f9f,stroke:#333,stroke-dasharray: 5 5
    style VRF fill:#e1f5fe
    style Network fill:#e1f5fe
    style Destination fill:#fff9c4
    style L2A fill:#e8f5e9
    style Inbound fill:#e8f5e9
    style Outbound fill:#e8f5e9
    style PodNetwork fill:#e8f5e9
    style BGPPeering fill:#fce4ec
    style Collector fill:#f3e5f5
    style TrafficMirror fill:#f3e5f5
    style AP fill:#fff3e0
    style IFC fill:#eceff1
    style NNS fill:#eceff1
```

### nodeSelector

Only CRDs with physical node-level concerns carry `nodeSelector`:

| CRD | nodeSelector | Reason |
|-----|:---:|--------|
| **Layer2Attachment** | ✅ | L2 segment must be placed on specific nodes |
| **InterfaceConfig** | ✅ | Physical interface config targets specific nodes |
| Inbound, Outbound, PodNetwork | ❌ | Node scoping inherited from Destination/VRF; pod placement is the scheduler's job |

## Ownership & Finalizer Structure

### OwnerRef Relationships (cascading GC)

Only the external infrastructure provisioning operator sets ownerRefs:

```mermaid
graph LR
    ExtReq["NetworkRequest<br/>(e.g. BM4X)"] -- "ownerRef" --> VRF
    ExtReq -- "ownerRef" --> Network

    style ExtReq fill:#f9f,stroke:#333,stroke-dasharray: 5 5
```

- Multiple provisioning CRDs can co-own a shared VRF via multiple ownerRefs
- K8s GC deletes the VRF only when **all** owners are removed
- VRF + Network can also be created manually (no ownerRef needed)

### Finalizer Protection (prevents premature deletion)

Finalizers prevent deletion of resources that are still referenced:

```mermaid
graph LR
    subgraph "Finalizer: vrf-in-use"
        D["Destination"] -- "vrfRef" --> VRF
        Col["Collector"] -- "mirrorVRF" --> VRF2["VRF"]
    end

    subgraph "Finalizer: network-in-use"
        L2A["L2A"] -- "networkRef" --> Net["Network"]
        IB["IB"] -- "networkRef" --> Net
        OB["OB"] -- "networkRef" --> Net
        PN["PN"] -- "networkRef" --> Net
    end

    subgraph "Finalizer: destination-in-use"
        L2A2["L2A"] -- "selector" --> Dst["Destination"]
        IB2["IB"] -- "selector" --> Dst
        OB2["OB"] -- "selector" --> Dst
        PN2["PN"] -- "selector" --> Dst
    end

    subgraph "Finalizer: collector-in-use"
        TM["TrafficMirror"] -- "collector" --> Col2["Collector"]
    end
```

| Finalizer | Set On | Set By | Prevents |
|-----------|--------|--------|----------|
| `vrf-in-use` | VRF | Destination, Collector controllers | VRF deletion while referenced |
| `network-in-use` | Network | L2A/IB/OB/PN controllers | Network deletion while referenced |
| `destination-in-use` | Destination | L2A/IB/OB/PN controllers | Destination deletion while selected |
| `collector-in-use` | Collector | TrafficMirror controller | Collector deletion while mirrored |
| `cleanup` | Usage CRDs | Own controller | Deletion before platform resources cleaned up |

### Required Deletion Order

Finalizers enforce this ordering — resources stuck in `Terminating` indicate
out-of-order deletion:

```mermaid
graph TD
    Step1["1. Delete TrafficMirror"] --> Step2["2. Delete BGPPeering"]
    Step2 --> Step3["3. Delete L2A / Inbound / Outbound / PodNetwork"]
    Step3 --> Step4["4. Delete Collector, AnnouncementPolicy"]
    Step4 --> Step5["5. Delete Destination"]
    Step5 --> Step6["6. Delete VRF, Network<br/>(or let ownerRef GC handle it)"]
```

## Cross-Cluster Architecture

```mermaid
graph LR
    subgraph mgmt["Management Cluster (per-cluster namespace)"]
        MgmtVRF["VRF"]
        MgmtNet["Network"]
        MgmtL2A["Layer2Attachment"]
        MgmtIB["Inbound"]
        MgmtAP["AnnouncementPolicy"]
        MgmtNR["NetworkRequest\n(external operator)"]
    end

    subgraph sync["Sync"]
        SyncMech["Sync Mechanism\n(strips ownerRefs)"]
    end

    subgraph wk["Workload Cluster (hardcoded namespace)"]
        WkVRF["VRF"]
        WkL2A["Layer2Attachment"]
        WkIB["Inbound"]
        WkAP["AnnouncementPolicy"]
        WkNNS["NodeNetworkStatus"]
        WkIFC["InterfaceConfig"]
    end

    MgmtVRF --> SyncMech
    MgmtNet --> SyncMech
    MgmtL2A --> SyncMech
    MgmtIB --> SyncMech
    MgmtAP --> SyncMech
    SyncMech --> WkVRF
    SyncMech --> WkL2A
    SyncMech --> WkIB
    SyncMech --> WkAP

    style MgmtNR fill:#f9f,stroke:#333,stroke-dasharray: 5 5
    style SyncMech fill:#fff9c4
```

- **Management cluster**: All CRDs live in a per-cluster namespace (e.g., `cluster-a`)
  - OwnerRefs work natively (same namespace)
  - SI engineers create/manage resources here
- **Workload cluster**: CRDs synced into a hardcoded namespace (e.g., `schiff-network`)
  - OwnerRefs are stripped (management-cluster UIDs don't exist here)
  - Lifecycle managed by sync mechanism, not K8s GC
  - `NodeNetworkStatus` and `InterfaceConfig` are workload-cluster-only

## Condition Types

| Condition | Used By | Meaning |
|-----------|---------|---------|
| `Ready` | All CRDs | Successfully reconciled end-to-end |
| `Resolved` | Usage CRDs | All references (networkRef, vrfRef) resolved |
| `Applied` | Usage CRDs, InterfaceConfig | Configuration applied to target nodes |
| `InterfaceNotFound` | Layer2Attachment | Referenced interface missing on node |
| `DuplicateVRF` | VRF | Another VRF in same namespace has same `spec.vrf` |

## Key Status Fields

### BGPPeering Status (controller-resolved, not user-configured)

| Field | Source | Meaning |
|-------|--------|---------|
| `asNumber` | Platform config | Platform-side autonomous system number |
| `neighborASNumber` | Platform config | Remote peer ASN (e.g., site/fabric ASN) |
| `neighborIPs` | Auto-derived per mode | listenRange: from L2A transfer network; loopbackPeer: ULA IPv6; SR-IOV VTEP_NODE: route reflector IPs |
| `workloadASNumber` | Spec or auto-generated | Workload-side ASN (echoed from spec or deterministically generated) |

### Layer2Attachment Key Spec Fields

| Field | Purpose |
|-------|---------|
| `disableAnycast` | Disables anycast gateway |
| `disableNeighborSuppression` | Disables ARP suppression in VXLAN |
| `disableSegmentation` | Disables TX/RX segmentation offload on the interface |
| `sriov.enabled` | Enables SR-IOV (immutable) |
| `mtu` | Interface MTU (1000–9000) |
| `nodeIPs.enabled` | Assigns node IPs from the network |

## Quick Start Example

A minimal HBN setup with an L2 attachment and ingress LB pool:

```yaml
# 1. Foundation: VRF + Network (created by infra provisioning or manually)
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
---
# 2. Routing: Destination defines reachable prefixes via VRF
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
---
# 3. Usage: Inbound allocates LB IPs from Network, exports into VRF
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
---
# 4. Policy: Attach communities to host-route exports
apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: AnnouncementPolicy
metadata:
  name: prod-communities
spec:
  vrfRef: prod
  hostRoutes:
    communities:
      - "65000:1000"
  aggregate:
    communities:
      - "65000:2000"
```
