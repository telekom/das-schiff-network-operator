---
title: CRD Reference
description: Auto-generated field reference for the network-connector.sylvaproject.org/v1alpha1 API group. Do not edit by hand; run `make docs-api`.
---

# API Reference

## Packages
- [network-connector.sylvaproject.org/v1alpha1](#network-connectorsylvaprojectorgv1alpha1)


## network-connector.sylvaproject.org/v1alpha1

Package networkconnector contains API types for the network-connector.sylvaproject.org API group.
These types implement intent-based network configuration CRDs that span management
and workload clusters. See docs/proposals/02-intent-based-config/README.md for the
full design proposal.


### Resource Types
- [AnnouncementPolicy](#announcementpolicy)
- [BGPPeering](#bgppeering)
- [Collector](#collector)
- [Destination](#destination)
- [Inbound](#inbound)
- [InterfaceConfig](#interfaceconfig)
- [Layer2Attachment](#layer2attachment)
- [Network](#network)
- [NodeAttachment](#nodeattachment)
- [NodeNetworkStatus](#nodenetworkstatus)
- [Outbound](#outbound)
- [PodNetwork](#podnetwork)
- [TrafficMirror](#trafficmirror)
- [VRF](#vrf)



#### AddressAllocation



AddressAllocation holds allocated IP addresses in status.



_Appears in:_
- [InboundSpec](#inboundspec)
- [InboundStatus](#inboundstatus)
- [Layer2AttachmentStatus](#layer2attachmentstatus)
- [OutboundSpec](#outboundspec)
- [OutboundStatus](#outboundstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ipv4` _string array_ | IPv4 lists allocated IPv4 addresses. |  | Optional: \{\} <br /> |
| `ipv6` _string array_ | IPv6 lists allocated IPv6 addresses. |  | Optional: \{\} <br /> |


#### AdvertisementConfig



AdvertisementConfig configures MetalLB advertisement mode.



_Appears in:_
- [InboundSpec](#inboundspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type is the advertisement mode. |  | Enum: [bgp l2] <br />Required: \{\} <br /> |


#### AggregateConfig



AggregateConfig controls aggregate (covering prefix) route export behavior.



_Appears in:_
- [AnnouncementPolicySpec](#announcementpolicyspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether an aggregate route is exported alongside host routes.<br />Default: true (auto-computed covering prefix from allocated IPs).<br />Set to false to export only host routes. | true | Optional: \{\} <br /> |
| `communities` _string array_ | Communities attached to the aggregate route. |  | Optional: \{\} <br /> |
| `prefixLengthV4` _integer_ | PrefixLengthV4 overrides the auto-computed IPv4 aggregate size.<br />Must be between the Network CIDR prefix length and 32.<br />If omitted, controller auto-computes the smallest covering prefix. |  | Maximum: 32 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `prefixLengthV6` _integer_ | PrefixLengthV6 overrides the auto-computed IPv6 aggregate size.<br />Must be between the Network CIDR prefix length and 128.<br />If omitted, controller auto-computes the smallest covering prefix. |  | Maximum: 128 <br />Minimum: 1 <br />Optional: \{\} <br /> |


#### AnnouncementPolicy



AnnouncementPolicy controls how routes are exported into a VRF: communities
on host routes and whether to also export an aggregate covering prefix.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `AnnouncementPolicy` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[AnnouncementPolicySpec](#announcementpolicyspec)_ |  |  |  |
| `status` _[AnnouncementPolicyStatus](#announcementpolicystatus)_ |  |  |  |


#### AnnouncementPolicySpec



AnnouncementPolicySpec defines the desired state of AnnouncementPolicy.
Host routes (/32, /128) are always exported — the DC fabric needs them as
more-specifics. This policy controls communities on host routes and whether
to also export an aggregate covering prefix.



_Appears in:_
- [AnnouncementPolicy](#announcementpolicy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `vrfRef` _string_ | VRFRef is the VRF this policy governs exports into. Required. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `selector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#labelselector-v1-meta)_ | Selector matches usage CRDs (Inbound, Outbound, Layer2Attachment, PodNetwork)<br />by label. The policy applies to exports from matched usage CRDs into the<br />specified VRF. If omitted, applies to ALL usage CRDs exporting into this VRF. |  | Optional: \{\} <br /> |
| `hostRoutes` _[RouteAnnouncementConfig](#routeannouncementconfig)_ | HostRoutes configures communities for host routes (/32, /128).<br />Host routes are always exported; this controls their community tags. |  | Optional: \{\} <br /> |
| `aggregate` _[AggregateConfig](#aggregateconfig)_ | Aggregate configures the aggregate (covering prefix) route.<br />Default: enabled with auto-computed prefix from allocated IPs. |  | Optional: \{\} <br /> |


#### AnnouncementPolicyStatus



AnnouncementPolicyStatus defines the observed state of AnnouncementPolicy.



_Appears in:_
- [AnnouncementPolicy](#announcementpolicy)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `matchedUsageCRDs` _integer_ | MatchedUsageCRDs is the number of usage CRDs matched by the selector. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the resource's state. |  | Optional: \{\} <br /> |


#### AnycastStatus



AnycastStatus holds anycast gateway information written by the controller.



_Appears in:_
- [Layer2AttachmentStatus](#layer2attachmentstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mac` _string_ | MAC is the anycast gateway MAC address. |  |  |
| `gateways` _string array_ | Gateways lists the anycast gateway addresses (IPv4 and/or IPv6). |  |  |


#### BFDProfile



BFDProfile represents BFD timer configuration.
Consistent with the existing BFDProfile in network.t-caas.telekom.com/v1alpha1.



_Appears in:_
- [BGPPeeringSpec](#bgppeeringspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `minInterval` _integer_ | MinInterval is the minimum interval for BFD packets in milliseconds. |  | Maximum: 60000 <br />Minimum: 50 <br /> |


#### BGPAddressFamily

_Underlying type:_ _string_

BGPAddressFamily specifies a BGP address family for session negotiation.

_Validation:_
- Enum: [ipv4Unicast ipv6Unicast]

_Appears in:_
- [BGPPeeringSpec](#bgppeeringspec)

| Field | Description |
| --- | --- |
| `ipv4Unicast` | BGPAddressFamilyIPv4Unicast negotiates IPv4 unicast routes.<br /> |
| `ipv6Unicast` | BGPAddressFamilyIPv6Unicast negotiates IPv6 unicast routes.<br /> |


#### BGPPeering



BGPPeering is the Schema for the bgppeerings API.
It defines a BGP session — either for an L2 attachment (listenRange mode)
or for tenant BGPaaS (loopbackPeer mode with auto-generated ULA addresses).





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `BGPPeering` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[BGPPeeringSpec](#bgppeeringspec)_ |  |  |  |
| `status` _[BGPPeeringStatus](#bgppeeringstatus)_ |  |  |  |


#### BGPPeeringExport



BGPPeeringExport configures how routes re-exported into the EVPN fabric are
tagged. It only applies to listenRange mode, where prefixes announced by L2
clients (matched against networkRefs) are re-exported into the fabric; the
configured communities are attached additively to those re-exported prefixes.
It has no effect for loopbackPeer mode and is ignored there.



_Appears in:_
- [BGPPeeringSpec](#bgppeeringspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `communities` _string array_ | Communities lists BGP community strings attached (additively) to the<br />prefixes re-exported into the EVPN fabric. Follows the same free-form<br />convention as AnnouncementPolicy communities (e.g. "65000:100"). |  | Optional: \{\} <br /> |


#### BGPPeeringMode

_Underlying type:_ _string_

BGPPeeringMode describes what this peering session is for.

_Validation:_
- Enum: [listenRange loopbackPeer]

_Appears in:_
- [BGPPeeringSpec](#bgppeeringspec)

| Field | Description |
| --- | --- |
| `listenRange` | BGPPeeringModeListenRange creates a BGP listen-range session for an L2 attachment.<br />The node peers with workloads on the L2 segment.<br /> |
| `loopbackPeer` | BGPPeeringModeLoopbackPeer creates a loopback peer BGP session (BGPaaS).<br />A tenant workload (e.g., kube-vip) speaks BGP directly through auto-generated<br />ULA IPv6 loopback addresses.<br /> |


#### BGPPeeringRef



BGPPeeringRef identifies the resources this peering session relates to.
The reference kind is mode-specific and mutually exclusive:
  - listenRange requires attachmentRef (the L2 segment to listen on) and
    networkRefs (the Networks whose prefixes L2 clients may announce);
    inboundRefs must not be set.
  - loopbackPeer requires inboundRefs (the allocated VIP pools the tenant
    advertises); attachmentRef and networkRefs must not be set.



_Appears in:_
- [BGPPeeringSpec](#bgppeeringspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `attachmentRef` _string_ | AttachmentRef references a Layer2Attachment by name.<br />Required for listenRange mode — identifies the L2 segment the BGP<br />listen-range is opened on (the listen-range CIDR comes from the L2A's<br />Network). Must not be set for loopbackPeer mode. |  | Optional: \{\} <br /> |
| `networkRefs` _string array_ | NetworkRefs references Network resources by name. For listenRange mode<br />their CIDRs form the import allow-list: L2 clients may only announce<br />prefixes contained within these Networks (matched with le 32 / le 128),<br />and those prefixes are re-exported into the EVPN fabric.<br />Required for listenRange mode; must not be set for loopbackPeer mode. |  | MinItems: 1 <br />Optional: \{\} <br /> |
| `inboundRefs` _string array_ | InboundRefs references Inbound resources whose IP pools the tenant<br />advertises (BGPaaS). Required for loopbackPeer mode; must not be set<br />for listenRange mode. |  | MinItems: 1 <br />Optional: \{\} <br /> |


#### BGPPeeringSpec



BGPPeeringSpec defines the desired state of BGPPeering.



_Appears in:_
- [BGPPeering](#bgppeering)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _[BGPPeeringMode](#bgppeeringmode)_ | Mode selects the peering type: listenRange (L2 attachment BGP) or loopbackPeer (BGPaaS).<br />Immutable after creation. |  | Enum: [listenRange loopbackPeer] <br />Required: \{\} <br /> |
| `ref` _[BGPPeeringRef](#bgppeeringref)_ | Ref identifies what this peering session is for. |  | Required: \{\} <br /> |
| `advertiseTransferNetwork` _boolean_ | AdvertiseTransferNetwork controls whether the transfer network prefix<br />is advertised to the BGP peer. |  | Optional: \{\} <br /> |
| `holdTime` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#duration-v1-meta)_ | HoldTime is the BGP hold timer duration. |  | Optional: \{\} <br /> |
| `keepaliveTime` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#duration-v1-meta)_ | KeepaliveTime is the BGP keepalive timer duration. |  | Optional: \{\} <br /> |
| `maximumPrefixes` _integer_ | MaximumPrefixes limits the number of prefixes accepted from the peer. |  | Minimum: 1 <br />Optional: \{\} <br /> |
| `workloadAS` _integer_ | WorkloadAS is the autonomous system number for the workload/tenant side.<br />Uses asplain notation; for 4-byte ASNs use the full 32-bit integer. |  | Maximum: 4.294967295e+09 <br />Minimum: 1 <br />Required: \{\} <br /> |
| `addressFamilies` _[BGPAddressFamily](#bgpaddressfamily) array_ | AddressFamilies specifies which BGP address families to negotiate.<br />If omitted, defaults to dual-stack (both IPv4 and IPv6 unicast). |  | Enum: [ipv4Unicast ipv6Unicast] <br />MinItems: 1 <br />Optional: \{\} <br /> |
| `enableBFD` _boolean_ | EnableBFD enables Bidirectional Forwarding Detection for fast link-failure<br />detection on this BGP session. |  | Optional: \{\} <br /> |
| `bfdProfile` _[BFDProfile](#bfdprofile)_ | BFDProfile configures BFD timer parameters. Only relevant when EnableBFD is true. |  | Optional: \{\} <br /> |
| `authSecretRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#localobjectreference-v1-core)_ | AuthSecretRef references a Secret containing the BGP session password (key: "password").<br />The controller reads the Secret and propagates the password to nodes via<br />NodeNetworkConfig — node agents never need direct Secret RBAC. |  | Optional: \{\} <br /> |
| `export` _[BGPPeeringExport](#bgppeeringexport)_ | Export configures BGP communities attached to the routes this peering<br />re-exports into the EVPN fabric. It only applies to listenRange mode: the<br />prefixes announced by L2 clients (constrained by networkRefs) are<br />re-exported into the fabric and, when set, tagged additively with the<br />configured communities. It is ignored for loopbackPeer mode. |  | Optional: \{\} <br /> |


#### BGPPeeringStatus



BGPPeeringStatus defines the observed state of BGPPeering.



_Appears in:_
- [BGPPeering](#bgppeering)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `asNumber` _integer_ | ASNumber is the autonomous system number of the platform side (observed). |  |  |
| `neighborIPs` _string array_ | NeighborIPs lists the IP addresses used for the BGP session.<br />For loopbackPeer: auto-generated ULA IPv6 addresses.<br />For listenRange: derived from the L2 attachment's transfer network.<br />For SR-IOV with VTEP_NODE: route reflector IPs from infrastructure provisioning. |  | Optional: \{\} <br /> |
| `neighborASNumber` _integer_ | NeighborASNumber is the AS number of the remote peer (observed). |  |  |
| `localIPs` _string array_ | LocalIPs lists the local peering IP addresses on the platform side.<br />For listenRange mode these are the IRB anycast gateway addresses (bare<br />IPs, no prefix) the node listens on, derived from the referenced<br />Layer2Attachment's Network (one per address family). |  | Optional: \{\} <br /> |
| `workloadASNumber` _integer_ | WorkloadASNumber is the AS number assigned to the workload (observed; mirrors spec.workloadAS). |  |  |
| `vrfs` _string array_ | VRFs lists the VRF names this peering relates to, derived from the<br />referenced Layer2Attachment (listenRange mode: ref.attachmentRef →<br />Layer2Attachment.spec.destinations) and/or referenced Inbounds<br />(loopbackPeer mode: ref.inboundRefs → Inbound.spec.destinations). Sorted<br />and de-duplicated. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the<br />BGPPeering's current state. |  | Optional: \{\} <br /> |


#### BondConfig



BondConfig defines configuration for a bond interface.



_Appears in:_
- [InterfaceConfigSpec](#interfaceconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `interfaces` _string array_ | Interfaces lists member ethernet interfaces. |  | MinItems: 1 <br /> |
| `mtu` _integer_ | Mtu is the maximum transmission unit. |  | Maximum: 9000 <br />Minimum: 1000 <br />Optional: \{\} <br /> |
| `parameters` _[BondParameters](#bondparameters)_ | Parameters are the bonding driver parameters. |  | Optional: \{\} <br /> |


#### BondParameters



BondParameters defines bonding driver parameters.



_Appears in:_
- [BondConfig](#bondconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _string_ | Mode is the bonding mode. |  | Enum: [active-backup 802.3ad balance-rr balance-xor broadcast balance-tlb balance-alb] <br /> |
| `miiMonitorInterval` _integer_ | MiiMonitorInterval is the MII monitoring interval in milliseconds. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `lacpRate` _string_ | LacpRate is the LACP rate (802.3ad only). |  | Enum: [fast slow] <br />Optional: \{\} <br /> |
| `upDelay` _integer_ | UpDelay is the delay before enabling a recovered member in milliseconds. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `downDelay` _integer_ | DownDelay is the delay before disabling a failed member in milliseconds. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `transmitHashPolicy` _string_ | TransmitHashPolicy is the hash policy for load balancing. |  | Enum: [layer2 layer3+4 layer2+3 encap2+3 encap3+4] <br />Optional: \{\} <br /> |


#### Collector



Collector defines a GRE collector endpoint and mirror VRF binding.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `Collector` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[CollectorSpec](#collectorspec)_ |  |  |  |
| `status` _[CollectorStatus](#collectorstatus)_ |  |  |  |


#### CollectorSpec



CollectorSpec defines the desired state of Collector.



_Appears in:_
- [Collector](#collector)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `address` _string_ | Address is the remote collector IP address. |  | Required: \{\} <br /> |
| `protocol` _string_ | Protocol is the GRE encapsulation type. |  | Enum: [l3gre l2gre] <br />Required: \{\} <br /> |
| `key` _integer_ | Key is the GRE tunnel key. The full unsigned 32-bit range is permitted;<br />stored as int64 so values above the signed 32-bit range round-trip cleanly. |  | Maximum: 4.294967295e+09 <br />Minimum: 0 <br />Optional: \{\} <br /> |
| `mirrorVRF` _[MirrorVRFRef](#mirrorvrfref)_ | MirrorVRF references a VRF for the mirror VRF. |  | Required: \{\} <br /> |


#### CollectorStatus



CollectorStatus defines the observed state of Collector.



_Appears in:_
- [Collector](#collector)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `greInterface` _string_ | GREInterface is the GRE interface name generated. |  | Optional: \{\} <br /> |
| `referenceCount` _integer_ | ReferenceCount is the number of TrafficMirrors referencing this Collector. |  |  |
| `activeNodes` _integer_ | ActiveNodes is the number of nodes where the collector is active. |  |  |
| `nodeAddresses` _object (keys:string, values:string)_ | NodeAddresses maps node name to the loopback source address allocated<br />from spec.mirrorVRF.loopback.subnet. Allocations are persisted across<br />reconciles; an entry is removed only when the corresponding node leaves<br />the cluster. This is the sole source of truth for per-node GRE source<br />IPs — it is intentionally not mirrored to a ConfigMap. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the Collector's state. |  | Optional: \{\} <br /> |


#### Destination



Destination is the Schema for the destinations API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `Destination` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[DestinationSpec](#destinationspec)_ |  |  |  |
| `status` _[DestinationStatus](#destinationstatus)_ |  |  |  |


#### DestinationPort



DestinationPort describes a port (or port range) allowed for traffic
to a Destination's prefixes. Mirrors K8s NetworkPolicy port semantics.
Exactly one of Port or PortRange must be set per entry.



_Appears in:_
- [DestinationSpec](#destinationspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `protocol` _[Protocol](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#protocol-v1-core)_ | Protocol is the network protocol. |  | Enum: [TCP UDP SCTP] <br />Required: \{\} <br /> |
| `port` _integer_ | Port is a single port number (1–65535).<br />Mutually exclusive with PortRange. |  | Maximum: 65535 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `portRange` _[PortRange](#portrange)_ | PortRange specifies a contiguous range of ports.<br />Mutually exclusive with Port. |  | Optional: \{\} <br /> |


#### DestinationSpec



DestinationSpec defines the desired state of Destination.



_Appears in:_
- [Destination](#destination)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `vrfRef` _string_ | References a VRF resource by name. Mutually exclusive with nextHop. |  |  |
| `prefixes` _string array_ | Subnets reachable via this destination (CIDR notation). |  |  |
| `nextHop` _[NextHopConfig](#nexthopconfig)_ | Next-hop addresses for static routing. Mutually exclusive with vrfRef. |  |  |
| `ports` _[DestinationPort](#destinationport) array_ | Port restrictions for egress NetworkPolicy. |  |  |


#### DestinationStatus



DestinationStatus defines the observed state of Destination.



_Appears in:_
- [Destination](#destination)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `referenceCount` _integer_ | How many attachments/connections reference this destination. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Standard Kubernetes conditions. |  | Optional: \{\} <br /> |


#### EthernetConfig



EthernetConfig defines configuration for an ethernet interface.



_Appears in:_
- [InterfaceConfigSpec](#interfaceconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mtu` _integer_ | Mtu is the maximum transmission unit. |  | Maximum: 9000 <br />Minimum: 1000 <br />Optional: \{\} <br /> |
| `virtualFunctionCount` _integer_ | VirtualFunctionCount is the number of SR-IOV virtual functions to create. |  | Minimum: 1 <br />Optional: \{\} <br /> |


#### IPNetwork



IPNetwork describes an IP address pool for a single address family.



_Appears in:_
- [NetworkSpec](#networkspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `cidr` _string_ | CIDR is the IP network in CIDR notation (e.g. "198.51.100.0/24"). |  | Required: \{\} <br /> |
| `prefixLength` _integer_ | PrefixLength is the allocation slice size (e.g. 28 means /28 per consumer).<br />Must be >= the CIDR prefix length. |  | Maximum: 128 <br />Minimum: 1 <br />Optional: \{\} <br /> |


#### Inbound



Inbound allocates IPs from a Network for MetalLB pools and BGP/L2 advertisement.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `Inbound` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[InboundSpec](#inboundspec)_ |  |  |  |
| `status` _[InboundStatus](#inboundstatus)_ |  |  |  |


#### InboundSpec



InboundSpec defines the desired state of Inbound.
Allocates IPs from a Network for MetalLB pools and BGP/L2 advertisement.
Optionally exports IPs as host routes into VRFs (HBN mode).



_Appears in:_
- [Inbound](#inbound)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `networkRef` _string_ | NetworkRef references a Network CRD by name. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `destinations` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#labelselector-v1-meta)_ | Destinations selects Destination CRDs. If omitted, non-HBN mode is used. |  | Optional: \{\} <br /> |
| `count` _integer_ | Count is the number of IPs to allocate. Mutually exclusive with Addresses. |  | Minimum: 1 <br />Optional: \{\} <br /> |
| `addresses` _[AddressAllocation](#addressallocation)_ | Addresses specifies explicit address allocations. Mutually exclusive with Count. |  | Optional: \{\} <br /> |
| `poolName` _string_ | PoolName overrides the MetalLB IPAddressPool name. |  | Optional: \{\} <br /> |
| `tenantLoadBalancerClass` _string_ | TenantLoadBalancerClass specifies the LoadBalancerClass for tenant-managed LB. |  | Optional: \{\} <br /> |
| `advertisement` _[AdvertisementConfig](#advertisementconfig)_ | Advertisement configures the MetalLB advertisement mode (bgp or l2). |  | Required: \{\} <br /> |


#### InboundStatus



InboundStatus defines the observed state of Inbound.



_Appears in:_
- [Inbound](#inbound)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `addresses` _[AddressAllocation](#addressallocation)_ | Addresses holds the allocated IP addresses. |  | Optional: \{\} <br /> |
| `poolName` _string_ | PoolName is the resolved MetalLB IPAddressPool name. |  | Optional: \{\} <br /> |
| `vrfs` _string array_ | VRFs lists the VRF names this Inbound is plumbed into, derived from the<br />matched Destinations (spec.destinations → Destination.spec.vrfRef). Sorted<br />and de-duplicated. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the Inbound's state. |  | Optional: \{\} <br /> |


#### InterfaceConfig



InterfaceConfig is the Schema for the interfaceconfigs API.
It defines node-level interface provisioning (bonds, ethernets).





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `InterfaceConfig` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[InterfaceConfigSpec](#interfaceconfigspec)_ |  |  |  |
| `status` _[InterfaceConfigStatus](#interfaceconfigstatus)_ |  |  |  |


#### InterfaceConfigNodeStatus



InterfaceConfigNodeStatus describes the per-node application status.



_Appears in:_
- [InterfaceConfigStatus](#interfaceconfigstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `node` _string_ | Node is the name of the node. |  |  |
| `applied` _boolean_ | Applied indicates whether the configuration has been applied. |  |  |


#### InterfaceConfigSpec



InterfaceConfigSpec defines the desired state of InterfaceConfig.
The typed fields (Ethernets, Bonds) cover common use cases. For netplan
features not modeled here, use RawNetplan as an escape hatch.



_Appears in:_
- [InterfaceConfig](#interfaceconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `nodeSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#labelselector-v1-meta)_ | NodeSelector selects the target nodes for interface configuration. |  |  |
| `ethernets` _object (keys:string, values:[EthernetConfig](#ethernetconfig))_ | Ethernets maps interface names to ethernet configurations. |  | Optional: \{\} <br /> |
| `bonds` _object (keys:string, values:[BondConfig](#bondconfig))_ | Bonds maps bond names to bond configurations. |  | Optional: \{\} <br /> |
| `rawNetplan` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#rawextension-runtime-pkg)_ | RawNetplan is an optional escape hatch for netplan features not covered<br />by the typed fields above. Contents are merged into the generated netplan<br />config, with typed fields taking precedence on conflict. |  | Type: object <br />Optional: \{\} <br /> |


#### InterfaceConfigStatus



InterfaceConfigStatus defines the observed state of InterfaceConfig.



_Appears in:_
- [InterfaceConfig](#interfaceconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the resource's state. |  | Optional: \{\} <br /> |
| `nodeStatuses` _[InterfaceConfigNodeStatus](#interfaceconfignodestatus) array_ | NodeStatuses lists the per-node application status. |  | Optional: \{\} <br /> |


#### Layer2Attachment



Layer2Attachment attaches a Network as a Layer 2 segment to a set of nodes.
Supports HBN mode (VXLAN + VRF) and non-HBN mode (physical interface).





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `Layer2Attachment` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[Layer2AttachmentSpec](#layer2attachmentspec)_ |  |  |  |
| `status` _[Layer2AttachmentStatus](#layer2attachmentstatus)_ |  |  |  |


#### Layer2AttachmentSpec



Layer2AttachmentSpec defines the desired state of Layer2Attachment.



_Appears in:_
- [Layer2Attachment](#layer2attachment)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `networkRef` _string_ | NetworkRef references a Network CRD by name. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `destinations` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#labelselector-v1-meta)_ | Destinations selects Destination resources by label.<br />If omitted, no VRF plumbing is performed. |  | Optional: \{\} <br /> |
| `nodeSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#labelselector-v1-meta)_ | NodeSelector selects which nodes receive this attachment. |  | Optional: \{\} <br /> |
| `interfaceRef` _string_ | InterfaceRef points to an existing host interface for non-HBN mode.<br />On bare metal: the agent creates a VLAN sub-interface on this NIC/bond.<br />On VMs: the hypervisor provides the NIC; the agent creates a VLAN sub-interface on it.<br />If nil (HBN mode), a VXLAN interface is created automatically. |  | Optional: \{\} <br /> |
| `interfaceName` _string_ | InterfaceName is the interface name suffix. Immutable once set. |  | Optional: \{\} <br /> |
| `mtu` _integer_ | MTU is the interface MTU. |  | Maximum: 9000 <br />Minimum: 1000 <br />Optional: \{\} <br /> |
| `disableAnycast` _boolean_ | DisableAnycast disables the anycast gateway. |  | Optional: \{\} <br /> |
| `disableNeighborSuppression` _boolean_ | DisableNeighborSuppression disables neighbor suppression. |  | Optional: \{\} <br /> |
| `disableSegmentation` _boolean_ | DisableSegmentation disables TX/RX segmentation offload on the interface. |  | Optional: \{\} <br /> |
| `sriov` _[SRIOVConfig](#sriovconfig)_ | SRIOV is the SR-IOV configuration. When set, the CRA agent skips<br />VXLAN/VLAN bridge setup and configures VF passthrough instead. |  | Optional: \{\} <br /> |
| `nodeIPs` _[NodeIPConfig](#nodeipconfig)_ | NodeIPs is the node IP assignment configuration. |  | Optional: \{\} <br /> |


#### Layer2AttachmentStatus



Layer2AttachmentStatus defines the observed state of Layer2Attachment.



_Appears in:_
- [Layer2Attachment](#layer2attachment)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `interfaceName` _string_ | InterfaceName is the interface name the agent creates for this attachment:<br />the spec.interfaceName override when set, otherwise the default<br />"vlan.<vlan>" derived from the referenced Network's VLAN. Empty when no<br />override is set and the Network (or its VLAN) cannot be resolved. |  | Optional: \{\} <br /> |
| `networkIPv4` _string_ | NetworkIPv4 is the IPv4 CIDR of the referenced Network (spec.ipv4.cidr),<br />surfaced on the attachment for convenience. Empty when the Network has no<br />IPv4 pool or cannot be resolved. |  | Optional: \{\} <br /> |
| `networkIPv6` _string_ | NetworkIPv6 is the IPv6 CIDR of the referenced Network (spec.ipv6.cidr).<br />Empty when the Network has no IPv6 pool or cannot be resolved. |  | Optional: \{\} <br /> |
| `vrfs` _string array_ | VRFs lists the VRF names this attachment is plumbed into, derived from the<br />matched Destinations (spec.destinations → Destination.spec.vrfRef). Sorted<br />and de-duplicated. |  | Optional: \{\} <br /> |
| `sriovVlanID` _integer_ | SRIOVVlanID is the VLAN ID assigned for SR-IOV device traffic. |  | Optional: \{\} <br /> |
| `anycast` _[AnycastStatus](#anycaststatus)_ | Anycast holds anycast gateway information. |  | Optional: \{\} <br /> |
| `nodeAddresses` _object (keys:string, values:[AddressAllocation](#addressallocation))_ | NodeAddresses holds per-node IP addresses allocated when nodeIPs.enabled is true.<br />Key is node name, value contains the allocated addresses. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the resource's state. |  | Optional: \{\} <br /> |


#### LoopbackConfig



LoopbackConfig defines a loopback interface whose per-node source address
is allocated from a CIDR by the intent controller. Allocations are
persisted in Collector.status.nodeAddresses and remain stable across
reconciles unless the node is removed from the cluster.



_Appears in:_
- [MirrorVRFRef](#mirrorvrfref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the loopback interface name (e.g. "lo.mir"). |  | MinLength: 1 <br />Required: \{\} <br /> |
| `subnet` _string_ | Subnet is the CIDR from which per-node loopback source addresses are<br />allocated by the intent controller. Each in-scope node receives one<br />host address from this subnet, recorded in Collector.status.nodeAddresses.<br />The subnet must be large enough to accommodate every node in scope; an<br />IPv4 /29 yields 6 usable hosts after skipping network and broadcast. |  | MinLength: 1 <br />Required: \{\} <br /> |


#### MirrorSource



MirrorSource identifies an attachment to mirror traffic from.



_Appears in:_
- [TrafficMirrorSpec](#trafficmirrorspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `kind` _string_ | Kind is the type of attachment. |  | Enum: [Layer2Attachment Inbound Outbound] <br />Required: \{\} <br /> |
| `name` _string_ | Name is the name of the attachment resource. |  | MinLength: 1 <br />Required: \{\} <br /> |


#### MirrorVRFRef



MirrorVRFRef references a VRF CRD for the mirror VRF and specifies its loopback config.



_Appears in:_
- [CollectorSpec](#collectorspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the VRF resource. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `loopback` _[LoopbackConfig](#loopbackconfig)_ | Loopback defines a loopback interface backed by a CAPI IPAM pool for per-node<br />GRE source IP allocation. The controller provisions IPAddressClaims from the<br />referenced pool on each node in scope. |  | Required: \{\} <br /> |


#### Network



Network is the Schema for the networks API.
A Network represents a pure pool definition referenced by name via networkRef
from usage CRDs such as Layer2Attachment, Inbound, Outbound, and PodNetwork.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `Network` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[NetworkSpec](#networkspec)_ |  |  |  |
| `status` _[NetworkStatus](#networkstatus)_ |  |  |  |


#### NetworkSpec



NetworkSpec defines the desired state of Network.
A Network is a pure pool definition — CIDR, VLAN, VNI, allocation properties.
It does not carry VRFs, node scope, or usage semantics.



_Appears in:_
- [Network](#network)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ipv4` _[IPNetwork](#ipnetwork)_ | IPv4 is the IPv4 address pool for this network. The CIDR must be the<br />network address (host bits zero), e.g. "198.51.100.224/27" — not an<br />authored host address like "198.51.100.225/27". The anycast gateway is<br />derived as the first usable host (network address + 1). |  | Optional: \{\} <br /> |
| `ipv6` _[IPNetwork](#ipnetwork)_ | IPv6 is the IPv6 address pool for this network. The CIDR must be the<br />network address (host bits zero), e.g. "2001:db8::/64". |  | Optional: \{\} <br /> |
| `vlan` _integer_ | VLAN is the VLAN ID for this network. |  | Maximum: 4094 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `vni` _integer_ | VNI is the VXLAN Network Identifier for this network.<br />In BM4X (bare-metal) mode without SR-IOV, the VNI is provided by the<br />service integration engineer. The node VTEP IP comes from the underlay,<br />not from this CRD. |  | Maximum: 1.6777215e+07 <br />Minimum: 1 <br />Optional: \{\} <br /> |


#### NetworkStatus



NetworkStatus defines the observed state of Network.



_Appears in:_
- [Network](#network)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `referenceCount` _integer_ | ReferenceCount is the number of usage CRDs that reference this Network. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the Network's state. |  | Optional: \{\} <br /> |


#### NextHopConfig



NextHopConfig specifies next-hop addresses for static routing in non-HBN mode.
At least one of IPv4 or IPv6 must be set.



_Appears in:_
- [DestinationSpec](#destinationspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ipv4` _string_ | IPv4 is the IPv4 next-hop address (e.g. "198.51.100.1"). |  | Optional: \{\} <br /> |
| `ipv6` _string_ | IPv6 is the IPv6 next-hop address (e.g. "2001:db8:100::1"). |  | Optional: \{\} <br /> |


#### NodeAttachment



NodeAttachment attaches cluster nodes to a remote VRF. Node IPs (from
node.status.addresses) are exported as host routes into the VRF, and the
VRF's destination prefixes are imported back for the attached nodes via SBR.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `NodeAttachment` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[NodeAttachmentSpec](#nodeattachmentspec)_ |  |  |  |
| `status` _[NodeAttachmentStatus](#nodeattachmentstatus)_ |  |  |  |


#### NodeAttachmentSpec



NodeAttachmentSpec defines the desired state of NodeAttachment.
A NodeAttachment leaks node IPs into a remote VRF and imports remote prefixes
back into the cluster VRF via source-based routing.



_Appears in:_
- [NodeAttachment](#nodeattachment)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `vrfRef` _string_ | VRFRef references a VRF CRD by name. The VRF provides the L3VNI and<br />route target for the fabric VRF that node IPs will be exported into. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `destinations` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#labelselector-v1-meta)_ | Destinations selects Destination CRDs whose prefixes should be imported<br />back into the cluster VRF (via SBR) for the attached nodes. |  | Required: \{\} <br /> |
| `nodeSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#labelselector-v1-meta)_ | NodeSelector restricts which nodes are attached. If omitted, all nodes<br />are attached to the VRF. |  | Optional: \{\} <br /> |


#### NodeAttachmentStatus



NodeAttachmentStatus defines the observed state of NodeAttachment.



_Appears in:_
- [NodeAttachment](#nodeattachment)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `vrfs` _string array_ | VRFs lists the VRF names whose prefixes are imported by this attachment,<br />derived from the matched Destinations (spec.destinations →<br />Destination.spec.vrfRef). This is distinct from spec.vrfRef (the target<br />VRF the nodes attach to). Sorted and de-duplicated. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the NodeAttachment's state. |  | Optional: \{\} <br /> |


#### NodeIPConfig



NodeIPConfig defines node IP assignment configuration for a Layer2Attachment.



_Appears in:_
- [Layer2AttachmentSpec](#layer2attachmentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether node IPs are assigned from the referenced Network. |  |  |
| `reservedRanges` _string array_ | ReservedRanges lists IP ranges (CIDRs) within the Network that are reserved<br />for pod use and must not be allocated to nodes. Each entry must be a valid<br />CIDR that falls within the parent Network's address space. |  | Optional: \{\} <br /> |


#### NodeInterface



NodeInterface describes a network interface on a node.



_Appears in:_
- [NodeNetworkStatusStatus](#nodenetworkstatusstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the interface name. |  |  |
| `mac` _string_ | Mac is the MAC address of the interface. |  | Optional: \{\} <br /> |
| `mtu` _integer_ | Mtu is the maximum transmission unit. |  | Optional: \{\} <br /> |
| `state` _[NodeInterfaceState](#nodeinterfacestate)_ | State is the link state. |  | Enum: [up down unknown] <br /> |
| `type` _[NodeInterfaceType](#nodeinterfacetype)_ | Type is the interface type. |  | Enum: [physical bond vlan bridge vxlan loopback virtual] <br />Optional: \{\} <br /> |
| `members` _string array_ | Members lists bonded member interfaces (for type=bond). |  | Optional: \{\} <br /> |
| `parent` _string_ | Parent is the parent interface (for type=vlan). |  | Optional: \{\} <br /> |
| `vlanID` _integer_ | VlanID is the VLAN ID (for type=vlan). |  | Optional: \{\} <br /> |
| `addresses` _string array_ | Addresses lists assigned IP addresses. |  | Optional: \{\} <br /> |


#### NodeInterfaceState

_Underlying type:_ _string_

NodeInterfaceState describes the link state of an interface.

_Validation:_
- Enum: [up down unknown]

_Appears in:_
- [NodeInterface](#nodeinterface)

| Field | Description |
| --- | --- |
| `up` |  |
| `down` |  |
| `unknown` |  |


#### NodeInterfaceType

_Underlying type:_ _string_

NodeInterfaceType describes the type of a network interface.

_Validation:_
- Enum: [physical bond vlan bridge vxlan loopback virtual]

_Appears in:_
- [NodeInterface](#nodeinterface)

| Field | Description |
| --- | --- |
| `physical` |  |
| `bond` |  |
| `vlan` |  |
| `bridge` |  |
| `vxlan` |  |
| `loopback` |  |
| `virtual` |  |


#### NodeNetworkStatus



NodeNetworkStatus is the Schema for the nodenetworkstatuses API.
It represents per-node network inventory populated by CRA agents.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `NodeNetworkStatus` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[NodeNetworkStatusSpec](#nodenetworkstatusspec)_ |  |  |  |
| `status` _[NodeNetworkStatusStatus](#nodenetworkstatusstatus)_ |  |  |  |


#### NodeNetworkStatusSpec



NodeNetworkStatusSpec is intentionally empty; the resource is agent-populated.



_Appears in:_
- [NodeNetworkStatus](#nodenetworkstatus)



#### NodeNetworkStatusStatus



NodeNetworkStatusStatus contains the observed network state of a node.



_Appears in:_
- [NodeNetworkStatus](#nodenetworkstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `interfaces` _[NodeInterface](#nodeinterface) array_ | Interfaces lists the network interfaces on the node. |  | Optional: \{\} <br /> |
| `routes` _[NodeRoute](#noderoute) array_ | Routes lists the routing table entries on the node. |  | Optional: \{\} <br /> |
| `lastUpdated` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#time-v1-meta)_ | LastUpdated is the timestamp of the last status update. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the resource's state. |  | Optional: \{\} <br /> |


#### NodeRoute



NodeRoute describes a routing table entry on a node.



_Appears in:_
- [NodeNetworkStatusStatus](#nodenetworkstatusstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `destination` _string_ | Destination is the route destination in CIDR notation. |  |  |
| `gateway` _string_ | Gateway is the next-hop gateway address. |  | Optional: \{\} <br /> |
| `interface` _string_ | Interface is the egress interface for this route. |  |  |
| `table` _string_ | Table is the routing table name. |  | Optional: \{\} <br /> |


#### Outbound



Outbound enables egress via SNAT, allocating IPs from a Network for Coil Egress and Calico pools.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `Outbound` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[OutboundSpec](#outboundspec)_ |  |  |  |
| `status` _[OutboundStatus](#outboundstatus)_ |  |  |  |


#### OutboundSpec



OutboundSpec defines the desired state of Outbound.
Enables egress via SNAT, allocating IPs from a Network for Coil Egress and Calico pools.



_Appears in:_
- [Outbound](#outbound)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `networkRef` _string_ | NetworkRef references a Network CRD by name. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `destinations` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#labelselector-v1-meta)_ | Destinations selects Destination CRDs. |  | Optional: \{\} <br /> |
| `count` _integer_ | Count is the number of IPs to allocate. Mutually exclusive with Addresses. |  | Minimum: 1 <br />Optional: \{\} <br /> |
| `addresses` _[AddressAllocation](#addressallocation)_ | Addresses specifies explicit address allocations. Mutually exclusive with Count. |  | Optional: \{\} <br /> |
| `replicas` _integer_ | Replicas is the number of Coil egress pod replicas. |  | Minimum: 1 <br />Optional: \{\} <br /> |


#### OutboundStatus



OutboundStatus defines the observed state of Outbound.



_Appears in:_
- [Outbound](#outbound)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `addresses` _[AddressAllocation](#addressallocation)_ | Addresses holds the allocated IP addresses. |  | Optional: \{\} <br /> |
| `vrfs` _string array_ | VRFs lists the VRF names this Outbound is plumbed into, derived from the<br />matched Destinations (spec.destinations → Destination.spec.vrfRef). Sorted<br />and de-duplicated. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the Outbound's state. |  | Optional: \{\} <br /> |


#### PodNetwork



PodNetwork is the Schema for the podnetworks API.
It allocates additional pod-level networks from a Network for CNI integration.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `PodNetwork` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[PodNetworkSpec](#podnetworkspec)_ |  |  |  |
| `status` _[PodNetworkStatus](#podnetworkstatus)_ |  |  |  |


#### PodNetworkSpec



PodNetworkSpec defines the desired state of PodNetwork.



_Appears in:_
- [PodNetwork](#podnetwork)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `networkRef` _string_ | NetworkRef is the name of the Network resource this PodNetwork allocates from. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `destinations` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#labelselector-v1-meta)_ | Destinations selects the destination workloads that may use this pod network. |  |  |


#### PodNetworkStatus



PodNetworkStatus defines the observed state of PodNetwork.



_Appears in:_
- [PodNetwork](#podnetwork)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `networkIPv4` _string_ | NetworkIPv4 is the IPv4 CIDR of the referenced Network (spec.ipv4.cidr).<br />Empty when the Network has no IPv4 pool or cannot be resolved. |  | Optional: \{\} <br /> |
| `networkIPv6` _string_ | NetworkIPv6 is the IPv6 CIDR of the referenced Network (spec.ipv6.cidr).<br />Empty when the Network has no IPv6 pool or cannot be resolved. |  | Optional: \{\} <br /> |
| `vrfs` _string array_ | VRFs lists the VRF names this PodNetwork is plumbed into, derived from the<br />matched Destinations (spec.destinations → Destination.spec.vrfRef). Sorted<br />and de-duplicated. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the<br />PodNetwork's current state. |  | Optional: \{\} <br /> |


#### PortRange



PortRange defines an inclusive start–end port range.



_Appears in:_
- [DestinationPort](#destinationport)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `start` _integer_ | Start is the first port in the range (1–65535). |  | Maximum: 65535 <br />Minimum: 1 <br /> |
| `end` _integer_ | End is the last port in the range (≥ Start, 1–65535). |  | Maximum: 65535 <br />Minimum: 1 <br /> |


#### RouteAnnouncementConfig



RouteAnnouncementConfig configures communities for a class of routes.



_Appears in:_
- [AnnouncementPolicySpec](#announcementpolicyspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `communities` _string array_ | Communities lists BGP community strings to attach to these routes. |  | Optional: \{\} <br /> |


#### SRIOVConfig



SRIOVConfig defines SR-IOV configuration for a Layer2Attachment.
When enabled, the agent performs SR-IOV VF passthrough instead of
creating VXLAN/VLAN interfaces via CRA. The CRA bridge and VXLAN
setup is skipped entirely for SR-IOV attachments.



_Appears in:_
- [Layer2AttachmentSpec](#layer2attachmentspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether SR-IOV is active. Immutable once set. |  |  |


#### TrafficMatch



TrafficMatch optionally filters which traffic to mirror.



_Appears in:_
- [TrafficMirrorSpec](#trafficmirrorspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `srcPrefix` _string_ | SrcPrefix filters by source IP prefix in CIDR notation. |  | Optional: \{\} <br /> |
| `dstPrefix` _string_ | DstPrefix filters by destination IP prefix in CIDR notation. |  | Optional: \{\} <br /> |
| `protocol` _string_ | Protocol filters by IP protocol. |  | Enum: [TCP UDP ICMP] <br />Optional: \{\} <br /> |
| `srcPort` _integer_ | SrcPort filters by source port (requires protocol to be tcp or udp). |  | Maximum: 65535 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `dstPort` _integer_ | DstPort filters by destination port (requires protocol to be tcp or udp). |  | Maximum: 65535 <br />Minimum: 1 <br />Optional: \{\} <br /> |


#### TrafficMirror



TrafficMirror declaratively mirrors traffic from an attachment to a Collector.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `TrafficMirror` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[TrafficMirrorSpec](#trafficmirrorspec)_ |  |  |  |
| `status` _[TrafficMirrorStatus](#trafficmirrorstatus)_ |  |  |  |


#### TrafficMirrorSpec



TrafficMirrorSpec defines the desired state of TrafficMirror.



_Appears in:_
- [TrafficMirror](#trafficmirror)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `source` _[MirrorSource](#mirrorsource)_ | Source identifies the attachment to mirror traffic from. |  | Required: \{\} <br /> |
| `collector` _string_ | Collector is the name of the Collector resource. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `direction` _string_ | Direction is the mirror direction. Defaults to "both" to match the underlying<br />NodeNetworkConfig MirrorACL behavior when the field is omitted. | both | Enum: [ingress egress both] <br />Optional: \{\} <br /> |
| `trafficMatch` _[TrafficMatch](#trafficmatch)_ | TrafficMatch optionally filters which traffic to mirror. |  | Optional: \{\} <br /> |


#### TrafficMirrorStatus



TrafficMirrorStatus defines the observed state of TrafficMirror.



_Appears in:_
- [TrafficMirror](#trafficmirror)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `activeNodes` _integer_ | ActiveNodes is the number of nodes where the mirror is active. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the TrafficMirror's state. |  | Optional: \{\} <br /> |


#### VRF



VRF represents a backbone VRF identity — name and overlay parameters (VNI, route target).
Defined once per VRF, referenced by name from Destination resources.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `network-connector.sylvaproject.org/v1alpha1` | | |
| `kind` _string_ | `VRF` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[VRFSpec](#vrfspec)_ |  |  |  |
| `status` _[VRFStatus](#vrfstatus)_ |  |  |  |


#### VRFSpec



VRFSpec defines the desired state of VRF.



_Appears in:_
- [VRF](#vrf)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `vrf` _string_ | VRF is the name of the VRF in the backbone. It may be a readable name; the<br />controller reduces it to a datapath-safe form (<=15 chars). The webhook<br />rejects names that cannot be reduced to fit. The generous upper bound here<br />is only a sanity limit. |  | MaxLength: 63 <br />Required: \{\} <br /> |
| `vni` _integer_ | VNI is the VXLAN Network Identifier. When omitted, the controller resolves it from operator config. |  | Maximum: 1.6777215e+07 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `routeTarget` _string_ | RouteTarget is the BGP route target for the VRF. When omitted, the controller resolves it. |  | Optional: \{\} <br /> |


#### VRFStatus



VRFStatus defines the observed state of VRF.



_Appears in:_
- [VRF](#vrf)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `referenceCount` _integer_ | ReferenceCount is the number of Destinations that reference this VRF. |  |  |
| `references` _string array_ | References lists the names of Destinations that reference this VRF. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions represent the latest available observations of the VRF's state. |  | Optional: \{\} <br /> |


